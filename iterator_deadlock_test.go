package wormhole

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// A positioned iterator excludes writers, and a pending writer excludes new
// readers. Deliberately violate the one-Ref-per-goroutine-per-Map contract to
// demonstrate why bypassing iterator reservation is unsupported. Isolate this
// misuse in a subprocess so its deadlock cannot strand the main test suite.
func TestIteratorUnsupportedCrossRefReentryDeadlocks(t *testing.T) {
	const childEnv = "WORMHOLE_ITERATOR_DEADLOCK_CHILD"
	const ready = "writer reserved the iterator's leaf; starting cross-Ref Get"
	if os.Getenv(childEnv) == "1" {
		runtime.GOMAXPROCS(2)
		m := New[string, string]()
		owner, reader := m.Ref(), m.Ref() // Deliberate contract violation below.
		if err := owner.Put("a", "value"); err != nil {
			t.Fatal(err)
		}
		it := owner.Iterator()
		it.Seek("a")
		go func() {
			writer := m.Ref()
			defer writer.Close()
			_ = writer.Put("b", "other")
		}()
		waitIteratorWriterReservation(t, &m.first.mu)
		fmt.Println(ready)
		reader.Get("a") // Unsupported: this goroutine already uses owner's Ref.
		it.Close()      // Unreachable: Get waits for the writer, which waits for it.
		t.Fatal("Get unexpectedly completed while the iterator kept its lock")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIteratorUnsupportedCrossRefReentryDeadlocks$", "-test.count=1", "-test.timeout=2s")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("child did not reach its test timeout: %v\n%s", ctx.Err(), out)
	}
	if err == nil || !bytes.Contains(out, []byte(ready)) || !bytes.Contains(out, []byte("panic: test timed out")) {
		t.Fatalf("did not reproduce expected deadlock: %v\n%s", err, out)
	}
	if bytes.Contains(out, []byte("DATA RACE")) {
		t.Fatalf("deadlock reproducer reported a data race:\n%s", out)
	}
	for _, frame := range []string{"(*leafRWMutex).rlockSlow(", "(*leafRWMutex).lockSlow("} {
		if !bytes.Contains(out, []byte(frame)) {
			t.Fatalf("missing blocked reader/writer frame %q:\n%s", frame, out)
		}
	}
}

// Each goroutine uses its own Ref. Parking releases the current leaf so the
// pending writer and independent reader can finish before the owner resumes.
func TestIteratorParkAllowsConcurrentCallers(t *testing.T) {
	m := New[string, string]()
	owner := m.Ref()
	defer owner.Close()
	if err := owner.Put("a", "value"); err != nil {
		t.Fatal(err)
	}
	it := owner.Iterator()
	defer it.Close()
	it.Seek("a")

	writerDone := make(chan error, 1)
	go func() {
		writer := m.Ref()
		err := writer.Put("b", "other")
		writer.Close()
		writerDone <- err
	}()
	waitIteratorWriterReservation(t, &m.first.mu)
	it.Park()
	if it.Valid() {
		t.Fatal("Park retained the iterator position")
	}

	readerDone := make(chan bool, 1)
	go func() {
		reader := m.Ref()
		value, ok := reader.Get("a")
		reader.Close()
		readerDone <- ok && value == "value"
	}()
	select {
	case ok := <-readerDone:
		if !ok {
			t.Fatal("read after Park failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader failed to finish after Park")
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer failed to finish after Park")
	}

	it.Seek("b")
	if key, value, ok := it.Peek(); !ok || key != "b" || value != "other" {
		t.Fatalf("Seek after Park returned (%q, %q, %v)", key, value, ok)
	}
}

func waitIteratorWriterReservation(t *testing.T, m *leafRWMutex) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadUint32(&m.state)&leafWriterBit == 0 {
		if time.Now().After(deadline) {
			t.Fatal("writer did not reserve the iterator's leaf")
		}
		runtime.Gosched()
	}
}
