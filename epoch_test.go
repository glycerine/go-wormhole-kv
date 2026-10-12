package wormhole

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustReceive(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for test participant")
	}
}

func TestEpochGracePeriod(t *testing.T) {
	for _, entering := range []bool{false, true} {
		name := "given old reader when publishing then wait for quiescence"
		if entering {
			name = "given reader before snapshot load when publishing then wait for announcement"
		}
		t.Run(name, func(t *testing.T) {
			var q epochRegistry
			r := q.register()
			defer q.unregister(r)
			r.enter()
			if !entering {
				r.announce(1)
			}
			started, done := make(chan struct{}), make(chan struct{})
			go func() { close(started); q.wait(2); close(done) }()
			mustReceive(t, started)
			// Wait for the writer to hold the registry lock: this confirms it
			// actually reached the grace-period scan rather than relying on sleep.
			deadline := time.Now().Add(5 * time.Second)
			for q.mu.TryLock() {
				q.mu.Unlock()
				if time.Now().After(deadline) {
					t.Fatal("writer did not enter scan")
				}
				runtime.Gosched()
			}
			select {
			case <-done:
				t.Fatal("writer passed an old/entering reader")
			default:
			}
			if entering {
				r.announce(2)
			} else {
				r.park()
			}
			mustReceive(t, done)
		})
	}
	t.Run("given parked and new readers when waiting then neither blocks reuse", func(t *testing.T) {
		var q epochRegistry
		parked, newer := q.register(), q.register()
		defer q.unregister(parked)
		defer q.unregister(newer)
		newer.enter()
		newer.announce(3)
		done := make(chan struct{})
		go func() { q.wait(2); close(done) }()
		mustReceive(t, done)
	})
	t.Run("given old reader when closing during wait then unregister completes", func(t *testing.T) {
		var q epochRegistry
		r := q.register()
		r.enter()
		r.announce(1)
		done, closed := make(chan struct{}), make(chan struct{})
		go func() { q.wait(2); close(done) }()
		go func() { q.unregister(r); close(closed) }()
		mustReceive(t, done)
		mustReceive(t, closed)
		if len(q.refs) != 0 {
			t.Fatal("closed reference retained")
		}
	})
}

func TestEpochDoubleBuffer(t *testing.T) {
	t.Run("given concurrent readers when repeatedly reusing two buffers then observe consistent published contents", func(t *testing.T) {
		type buffer struct {
			value   uint64
			inverse uint64
		}
		type publication struct {
			data    *buffer
			version uint64
		}
		buffers := [2]buffer{{1, ^uint64(1)}, {1, ^uint64(1)}}
		var current atomic.Pointer[publication]
		current.Store(&publication{&buffers[0], 1})
		var q epochRegistry
		var wg sync.WaitGroup
		var bad atomic.Bool
		stop := make(chan struct{})
		ready := make(chan struct{}, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := q.register()
				defer q.unregister(r)
				ready <- struct{}{}
				for {
					select {
					case <-stop:
						return
					default:
					}
					r.enter()
					s := current.Load()
					r.announce(s.version)
					a := s.data.value
					runtime.Gosched() // enlarge the interval before the second read
					b := s.data.inverse
					if a != s.version || b != ^a {
						bad.Store(true)
					}
					r.park()
				}
			}()
		}
		for i := 0; i < 6; i++ {
			<-ready
		}
		for version := uint64(2); version <= 2000; version++ {
			old := current.Load()
			next := &buffers[(version-1)&1]
			next.value, next.inverse = version, ^version
			current.Store(&publication{next, version})
			q.wait(version)
			// Mutating the retired buffer immediately tests the safety of wait.
			old.data.value, old.data.inverse = version, ^version
		}
		close(stop)
		wg.Wait()
		if bad.Load() {
			t.Fatal("reader observed overwritten snapshot")
		}
	})
}
