package wormhole

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

func TestLenAcrossRefLifecycle(t *testing.T) {
	m := New[string, string]()
	writer, remover := m.Ref(), m.Ref()
	for i := range 512 {
		if err := writer.Put(fmt.Sprintf("%04d", i), "v"); err != nil {
			t.Fatal(err)
		}
	}
	if n := remover.DeleteRange("0000", "0500"); n != 500 {
		t.Fatalf("removed %d keys, want 500", n)
	}
	if got := m.Len(); got != 12 {
		t.Fatalf("active signed contributions: Len=%d, want 12", got)
	}
	writer.Close()
	if got := m.Len(); got != 12 {
		t.Fatalf("after positive contributor closes: Len=%d", got)
	}
	remover.Close()
	remover.Close()
	if got := m.Len(); got != 12 {
		t.Fatalf("after all contributors close: Len=%d", got)
	}
	for range 40 {
		r := m.Ref()
		if err := r.Put("new", "v"); err != nil {
			t.Fatal(err)
		}
		r.Close()
		r = m.Ref()
		if !r.Delete("new") {
			t.Fatal("new key disappeared")
		}
		r.Close()
		if m.Len() != 12 {
			t.Fatal("reference churn lost a contribution")
		}
	}
}

func TestCounterSnapshotConcurrentTransfers(t *testing.T) {
	for _, sameRef := range []bool{false, true} {
		t.Run(fmt.Sprintf("sameRef=%v", sameRef), func(t *testing.T) {
			var q epochRegistry
			a := q.register()
			defer q.unregister(a)
			b := a
			if !sameRef {
				b = q.register()
				defer q.unregister(b)
			}
			done := make(chan struct{})
			defer func() { <-done }()
			go func() {
				defer close(done)
				for i := range 50000 {
					a.addCount(1)
					b.addCount(-1)
					if i%16 == 0 {
						runtime.Gosched()
					}
				}
			}()
			for {
				// The actual aggregate alternates between zero and one. Reading
				// unrelated shard instants could produce negative or larger results.
				if n := q.length(); n < 0 || n > 1 {
					t.Fatalf("inconsistent snapshot: %d", n)
				}
				select {
				case <-done:
					if n := q.length(); n != 0 {
						t.Fatalf("final aggregate=%d", n)
					}
					return
				default:
				}
			}
		})
	}
}

func TestCounterSnapshotConcurrentRetirement(t *testing.T) {
	var q epochRegistry
	for _, delta := range []int64{1, -1} {
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 250 {
					r := q.register()
					r.addCount(delta)
					q.unregister(r)
				}
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		func() {
			defer func() { <-done }()
			previous := int64(0)
			if delta < 0 {
				previous = 1000
			}
			for {
				n := q.length()
				if n < 0 || n > 1000 || (n-previous)*delta < 0 {
					t.Fatalf("retirement changed count incorrectly: previous=%d current=%d delta=%d", previous, n, delta)
				}
				previous = n
				select {
				case <-done:
					return
				default:
				}
			}
		}()
		want := int64(1000)
		if delta < 0 {
			want = 0
		}
		if n := q.length(); n != want {
			t.Fatalf("after retirement: Len=%d want=%d", n, want)
		}
	}
}
