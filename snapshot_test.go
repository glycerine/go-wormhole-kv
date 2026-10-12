package wormhole

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotAnnouncementHandshake(t *testing.T) {
	for _, publications := range []int{1, 2} {
		t.Run(fmt.Sprintf("given parked descriptor before %d publications when announcing then reload before reading reused data", publications), func(t *testing.T) {
			m := New[string, string]()
			writer, reader := m.Ref(), m.Ref()
			defer writer.Close()
			defer reader.Close()
			for i := range leafCapacity {
				if err := writer.Put(fmt.Sprintf("%04d", i), "v"); err != nil {
					t.Fatal(err)
				}
			}
			first := m.current.Load() // reader is still parked
			if err := writer.Put("9999", "new"); err != nil {
				t.Fatal(err)
			}
			if publications == 2 {
				for i := 0; m.current.Load().version < first.version+2; i++ {
					if i > leafCapacity*2 {
						t.Fatal("second split did not happen")
					}
					if err := writer.Put(fmt.Sprintf("z%04d", i), "tail"); err != nil {
						t.Fatal(err)
					}
				}
				if m.current.Load().table != first.table {
					t.Fatal("setup did not reuse the physical table")
				}
			}
			s := reader.protectSnapshot(first)
			if s == first || s != m.current.Load() || reader.epoch.state.Load() != s.version {
				t.Fatal("retained retired publication")
			}
			l := s.table.jump("9999")
			reader.epoch.park()
			l.mu.RLock()
			if l.version > s.version || l.match("9999", keyTag("9999")) == leafCapacity {
				t.Error("new route missing")
			}
			l.mu.RUnlock()
		})
	}
	t.Run("given an old announcement when writer waits then forwarding to current lets it retire the old table", func(t *testing.T) {
		m := New[string, string]()
		writer, reader := m.Ref(), m.Ref()
		defer writer.Close()
		defer reader.Close()
		for i := range leafCapacity {
			if err := writer.Put(fmt.Sprintf("%04d", i), "v"); err != nil {
				t.Fatal(err)
			}
		}
		first := m.current.Load()
		reader.epoch.announce(first.version)
		done := make(chan struct{})
		go func() {
			if err := writer.Put("9999", "new"); err != nil {
				t.Error(err)
			}
			close(done)
		}()
		// Wait for the registry scan, proving publication has happened and the
		// writer is waiting for this reader rather than relying on a sleep.
		deadline := time.Now().Add(5 * time.Second)
		for m.epochs.mu.TryLock() {
			m.epochs.mu.Unlock()
			if time.Now().After(deadline) {
				t.Fatal("writer did not enter its grace-period scan")
			}
			runtime.Gosched()
		}
		select {
		case <-done:
			t.Fatal("writer passed old announcement")
		default:
		}
		s := reader.protectSnapshot(first)
		mustReceive(t, done)
		if s.version != first.version+1 || s.table.jump("9999") == m.first {
			t.Fatal("did not read new table while announced")
		}
		reader.epoch.park()
	})
}

func TestSnapshotHandshakeDoubleBuffer(t *testing.T) {
	t.Run("given six yielding readers when repeatedly publishing and reusing tables then announced contents stay coherent", func(t *testing.T) {
		// Use ordinary mutable table fields, so the race detector observes both
		// sides of the exact production protectSnapshot/grace-period protocol.
		m := &Map[string, string]{tables: [2]*metaTable[string, string]{{n: 1, maxPrefix: ^1}, {n: 1, maxPrefix: ^1}}}
		m.current.Store(&metaSnapshot[string, string]{m.tables[0], 1})
		var wg sync.WaitGroup
		var bad atomic.Bool
		var reads atomic.Uint64
		stop, ready := make(chan struct{}), make(chan struct{}, 6)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := m.Ref()
				defer r.Close()
				ready <- struct{}{}
				for {
					select {
					case <-stop:
						return
					default:
					}
					first := m.current.Load()
					runtime.Gosched() // allow retirement while still parked
					s := r.protectSnapshot(first)
					a := s.table.n
					runtime.Gosched() // allow publication before the second read
					b := s.table.maxPrefix
					if uint64(a) != s.version || b != ^a {
						bad.Store(true)
					}
					r.epoch.park()
					reads.Add(1)
				}
			}()
		}
		for range 6 {
			<-ready
		}
		for version := uint64(2); version <= 2000; version++ {
			old := m.current.Load()
			next := m.tables[(version-1)&1]
			next.n, next.maxPrefix = int(version), ^int(version)
			m.current.Store(&metaSnapshot[string, string]{next, version})
			m.epochs.wait(version)
			old.table.n, old.table.maxPrefix = int(version), ^int(version)
		}
		close(stop)
		wg.Wait()
		if bad.Load() || reads.Load() == 0 {
			t.Fatal("inconsistent or missing protected reads")
		}
	})
}
