package wormhole

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// checkMap requires quiescent callers. It audits both metadata tables, leaf
// intervals, hash and key-order invariants, and the key count independently.
func checkMap(t testing.TB, m *Map[string, string], want map[string]string) {
	t.Helper()
	m.metalock.Lock()
	defer m.metalock.Unlock()
	var leaves []*leaf[string, string]
	seen := map[string]bool{}
	var prev *leaf[string, string]
	for l := m.first; l != nil; l = l.next {
		if l.prev != prev || prev != nil && prev.anchor >= l.anchor {
			t.Fatal("invalid ordered leaf links")
		}
		leaves = append(leaves, l)
		part, tags := map[string]string{}, map[string]uint16{}
		for i, tag := range l.tags {
			if tag == 0 {
				continue
			}
			e := l.recordAtSlot(i)
			k := e.key
			v, ok := want[k]
			if !ok || v != e.value || seen[k] {
				t.Fatalf("unexpected or duplicate key %q", k)
			}
			if k < l.anchor || l.next != nil && k >= l.next.anchor {
				t.Fatalf("key %q outside anchor interval", k)
			}
			part[k], tags[k], seen[k] = v, keyTag(k), true
		}
		checkLeaf(t, l, part, tags)
		prev = l
	}
	if len(seen) != len(want) || m.Len() != int64(len(want)) {
		t.Fatalf("key count=%d Len=%d want=%d", len(seen), m.Len(), len(want))
	}
	for _, table := range m.tables {
		checkMetaIndex(t, table, leaves)
	}
	for r := range m.epochs.refs {
		if r.state.Load() != parkedEpoch {
			t.Fatal("public operation left reference active")
		}
	}
}

func TestMapPointOperations(t *testing.T) {
	t.Run("given arbitrary binary keys when inserting replacing probing and deleting then retain exact values", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		want := map[string]string{}
		for _, k := range []string{"", "\x00", "\x00\x00", "a", "a\x00", "\xff", "\xff\xff"} {
			if r.Probe(k) {
				t.Fatalf("empty map contains %q", k)
			}
			if _, ok := r.Get(k); ok {
				t.Fatalf("empty map get %q", k)
			}
			if r.Delete(k) {
				t.Fatal("deleted absent key")
			}
			if err := r.Put(k, k+"\x00value"); err != nil {
				t.Fatal(err)
			}
			want[k] = k + "\x00value"
		}
		checkMap(t, m, want)
		for k := range want {
			if err := r.Put(k, ""); err != nil {
				t.Fatal(err)
			}
			want[k] = ""
			v, ok := r.Get(k)
			if !ok || v != "" || !r.Probe(k) {
				t.Fatalf("replacement %q", k)
			}
		}
		checkMap(t, m, want)
		for k := range want {
			if !r.Delete(k) || r.Delete(k) {
				t.Fatalf("delete %q", k)
			}
			delete(want, k)
		}
		checkMap(t, m, want)
	})
	t.Run("given a closed reference when used then reject it while other references remain usable", func(t *testing.T) {
		m := New[string, string]()
		closed, live := m.Ref(), m.Ref()
		defer live.Close()
		closed.Close()
		closed.Close()
		if err := live.Put("key", "value"); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if recover() == nil {
				t.Fatal("closed reference accepted")
			}
		}()
		closed.Probe("key")
	})
}

func runMapProperty(t testing.TB, seed uint64, steps int) {
	t.Helper()
	m := New[string, string]()
	r := m.Ref()
	defer r.Close()
	want := map[string]string{}
	rng := rand.New(rand.NewPCG(seed, seed^99))
	keys := make([]string, 1200)
	for i := range keys {
		keys[i] = fmt.Sprintf("%c-prefix-%04d", byte(i), i)
	}
	for i := 0; i < 140; i++ {
		keys[i] = "p" + strings.Repeat("\x00", i)
	}
	keys[140], keys[141] = "", "\xff"
	for step := 0; step < steps; step++ {
		k := keys[rng.IntN(len(keys))]
		switch rng.IntN(4) {
		case 0:
			_, ok := want[k]
			if r.Delete(k) != ok {
				t.Fatalf("seed=%d step=%d delete", seed, step)
			}
			delete(want, k)
		case 1:
			v, ok := want[k]
			got, found := r.Get(k)
			if found != ok || got != v || r.Probe(k) != ok {
				t.Fatalf("seed=%d step=%d get %q", seed, step, k)
			}
		default:
			v := fmt.Sprintf("value-%d", step)
			if err := r.Put(k, v); err != nil {
				t.Fatal(err)
			}
			want[k] = v
		}
		if step%257 == 0 {
			checkMap(t, m, want)
		}
	}
	checkMap(t, m, want)
	// Deletions must exercise rightward merges and repeated table reuse too.
	for _, k := range keys {
		if r.Delete(k) {
			delete(want, k)
		}
	}
	checkMap(t, m, want)
}

func TestMapRandomized(t *testing.T) {
	for seed := uint64(0); seed < 8; seed++ {
		t.Run(fmt.Sprintf("given seed %d when mixing point operations then match reference map and structural invariants", seed), func(t *testing.T) { runMapProperty(t, seed, 8000) })
	}
}

func FuzzMapOperations(f *testing.F) {
	for _, seed := range []uint64{0, 1, 42, 65535} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { runMapProperty(t, seed, 1500) })
}

func TestMapSplitFailure(t *testing.T) {
	t.Run("given full leaf with oversized common prefix when split fails then preserve all records", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		prefix := strings.Repeat("x", 65535)
		want := map[string]string{}
		for i := 0; i < leafCapacity; i++ {
			k := prefix + string(byte(i))
			if err := r.Put(k, "value"); err != nil {
				t.Fatal(err)
			}
			want[k] = "value"
		}
		if err := r.Put(prefix+"\xff", "new"); !errors.Is(err, ErrAnchorTooLong) {
			t.Fatalf("split error=%v", err)
		}
		checkMap(t, m, want)
		if err := r.Put(prefix+"\x00", "replacement"); err != nil {
			t.Fatal("replacement should not require splitting")
		}
		if r.Probe(prefix + "\xff") {
			t.Fatal("failed insertion became visible")
		}
	})
}

func TestMapConcurrentStructuralChanges(t *testing.T) {
	t.Run("given disjoint writers and independent readers when splitting and merging then retain records and both metadata tables", func(t *testing.T) {
		m := New[string, string]()
		const writers, keys, rounds = 6, 500, 4
		start, stop := make(chan struct{}), make(chan struct{})
		var wg, readers sync.WaitGroup
		var failure atomic.Bool
		for worker := 0; worker < writers; worker++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				r := m.Ref()
				defer r.Close()
				<-start
				rng := rand.New(rand.NewPCG(uint64(worker), 42))
				for round := 0; round < rounds; round++ {
					for _, i := range rng.Perm(keys) {
						// Interleave workers in lexical order to contend on the
						// same leaves, while key ownership stays independent.
						k := fmt.Sprintf("%04d-%d", i, worker)
						v := fmt.Sprintf("value-%d-%d-%d", worker, round, i)
						if err := r.Put(k, v); err != nil {
							failure.Store(true)
							return
						}
						if got, ok := r.Get(k); !ok || got != v {
							failure.Store(true)
							return
						}
					}
					if round+1 < rounds {
						for _, i := range rng.Perm(keys) {
							if !r.Delete(fmt.Sprintf("%04d-%d", i, worker)) {
								failure.Store(true)
								return
							}
						}
					}
				}
			}(worker)
		}
		for worker := 0; worker < 4; worker++ {
			readers.Add(1)
			go func(worker int) {
				defer readers.Done()
				r := m.Ref()
				defer r.Close()
				<-start
				rng := rand.New(rand.NewPCG(uint64(worker), 99))
				for {
					select {
					case <-stop:
						return
					default:
					}
					w, i := rng.IntN(writers), rng.IntN(keys)
					if value, ok := r.Get(fmt.Sprintf("%04d-%d", i, w)); ok {
						valid := false
						for round := 0; round < rounds; round++ {
							if value == fmt.Sprintf("value-%d-%d-%d", w, round, i) {
								valid = true
								break
							}
						}
						if !valid {
							failure.Store(true)
							return
						}
					}
				}
			}(worker)
		}
		close(start)
		done := make(chan struct{})
		go func() { wg.Wait(); close(stop); readers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("concurrent structural operations stalled")
		}
		if failure.Load() {
			t.Fatal("concurrent key/value mismatch")
		}
		want := map[string]string{}
		for w := 0; w < writers; w++ {
			for i := 0; i < keys; i++ {
				want[fmt.Sprintf("%04d-%d", i, w)] = fmt.Sprintf("value-%d-%d-%d", w, rounds-1, i)
			}
		}
		checkMap(t, m, want)
	})
}

func TestMapStaleRoute(t *testing.T) {
	t.Run("given a route to a retired right leaf when merging then reject its stale value", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		for i := 0; i < leafCapacity; i++ {
			if err := r.Put(fmt.Sprintf("%04d", i), "old"); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Put("9999", "old-right"); err != nil {
			t.Fatal(err)
		}
		s := m.current.Load()
		retired := s.table.jump("9999")
		if retired == m.first {
			t.Fatal("setup did not create right leaf")
		}
		for i := 0; i < 34; i++ {
			if !r.Delete(fmt.Sprintf("%04d", i)) {
				t.Fatal("setup delete failed")
			}
		}
		retired.mu.RLock()
		if retired.version <= s.version {
			t.Fatal("retired route would pass version validation")
		}
		retired.mu.RUnlock()
		if err := r.Put("9999", "updated-left"); err != nil {
			t.Fatal(err)
		}
		if value, ok := r.Get("9999"); !ok || value != "updated-left" {
			t.Fatal("read stale retired record")
		}
	})
	t.Run("given a routed reader waiting on a leaf when splitting then retry on the new leaf version", func(t *testing.T) {
		m := New[string, string]()
		writer := m.Ref()
		defer writer.Close()
		for i := 0; i < leafCapacity; i++ {
			if err := writer.Put(fmt.Sprintf("%04d", i), "old"); err != nil {
				t.Fatal(err)
			}
		}
		reader := m.Ref()
		defer reader.Close()
		l := m.first
		l.mu.Lock()
		// Directly execute the same routing/epoch steps as lockLeaf, then
		// hold the physical pointer while the writer publishes a split.
		old := reader.protectSnapshot(m.current.Load())
		routed := old.table.jump("9999")
		reader.epoch.park()
		if routed != l {
			t.Fatal("initial route mismatch")
		}
		l.mu.Unlock()
		if err := writer.Put("9999", "new"); err != nil {
			t.Fatal(err)
		}
		routed.mu.RLock()
		if routed.version <= old.version {
			t.Fatal("stale route would pass validation")
		}
		routed.mu.RUnlock()
		if value, ok := reader.Get("9999"); !ok || value != "new" {
			t.Fatal("reader did not reach new leaf")
		}
	})
	t.Run("given an old metadata reader when a split publishes then only table reuse waits", func(t *testing.T) {
		m := New[string, string]()
		writer := m.Ref()
		defer writer.Close()
		for i := 0; i < leafCapacity; i++ {
			if err := writer.Put(fmt.Sprintf("%04d", i), "old"); err != nil {
				t.Fatal(err)
			}
		}
		oldReader := m.Ref()
		defer oldReader.Close()
		fresh := m.Ref()
		defer fresh.Close()
		oldReader.epoch.enter()
		old := m.current.Load()
		oldReader.epoch.announce(old.version)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := writer.Put("9999", "new"); err != nil {
				t.Error(err)
			}
		}()
		deadline := time.Now().Add(5 * time.Second)
		for m.current.Load() == old {
			if time.Now().After(deadline) {
				oldReader.epoch.park()
				t.Fatal("split did not publish")
			}
			runtime.Gosched()
		}
		select {
		case <-done:
			t.Fatal("writer reused table before old reader parked")
		default:
		}
		// Old metadata remains intact. A new reader can use the published table
		// and the leaf locks have already been released before the grace period.
		if old.table.jump("9999") != m.first {
			t.Fatal("old metadata mutated prematurely")
		}
		if value, ok := fresh.Get("9999"); !ok || value != "new" {
			t.Fatal("published leaf was not readable during grace period")
		}
		oldReader.epoch.park()
		mustReceive(t, done)
	})
}
