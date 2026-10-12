package wormhole

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func checkScan(t testing.TB, r *Ref[string, string], want map[string]string, start string) {
	t.Helper()
	keys := make([]string, 0, len(want))
	for k := range want {
		if k >= start {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	it := r.Iterator()
	defer it.Close()
	it.Seek(start)
	for i, key := range keys {
		if !it.Valid() {
			t.Fatalf("scan exhausted at %d/%d", i, len(keys))
		}
		k, v, ok := it.Peek()
		if !ok || k != key || v != want[key] {
			t.Fatalf("peek at %d got (%q,%q,%v) want (%q,%q)", i, k, v, ok, key, want[key])
		}
		k, v, ok = it.Next()
		if !ok || k != key || v != want[key] {
			t.Fatal("Next differs from Peek")
		}
	}
	if it.Valid() {
		t.Fatal("scan has extra keys")
	}
	if k, v, ok := it.Next(); ok || k != "" || v != "" {
		t.Fatal("invalid Next result")
	}
}

func TestIteratorOrdering(t *testing.T) {
	t.Run("given shuffled binary keys across leaves when seeking and scanning then match sorted reference keys", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		want := map[string]string{"": "empty", "\x00": "zero", "\xff": "high"}
		for i := 0; i < 1000; i++ {
			want[fmt.Sprintf("%c-%04d", byte(i), i)] = fmt.Sprint(i)
		}
		for i := 0; i < 140; i++ {
			want["p"+strings.Repeat("\x00", i)] = fmt.Sprint(i)
		}
		keys := make([]string, 0, len(want))
		for k := range want {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		rng := rand.New(rand.NewPCG(1, 42))
		for _, i := range rng.Perm(len(keys)) {
			if err := r.Put(keys[i], want[keys[i]]); err != nil {
				t.Fatal(err)
			}
		}
		for _, start := range []string{"", "\x00", "\x00-0000", "p\x00\x00", "q", "\xff", "\xff\xff"} {
			checkScan(t, r, want, start)
		}
		it := r.Iterator()
		for _, start := range keys {
			it.Seek(start)
			k, _, ok := it.Peek()
			if !ok || k != start {
				t.Fatalf("exact seek %q got %q", start, k)
			}
		}
		it.Seek("")
		it.Skip(0)
		for i := 0; i < len(keys); i += 17 {
			k, _, ok := it.Peek()
			if !ok || k != keys[i] {
				t.Fatalf("skip at %d", i)
			}
			it.Skip(17)
		}
		if it.Valid() {
			t.Fatal("Skip beyond end did not exhaust")
		}
		it.Seek("")
		it.Skip(^uint(0))
		if it.Valid() {
			t.Fatal("large Skip did not exhaust")
		}
		it.Park()
		it.Close()
		it.Close()
		checkMap(t, m, want)
	})
	t.Run("given empty map when seeking then hold no leaf lock and allow later writes", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		it := r.Iterator()
		it.Seek("")
		if it.Valid() || !m.first.mu.TryLock() {
			t.Fatal("empty iterator retained leaf lock")
		}
		m.first.mu.Unlock()
		it.Close()
		if err := r.Put("a", "value"); err != nil {
			t.Fatal(err)
		}
		checkScan(t, r, map[string]string{"a": "value"}, "")
	})
}

func TestIteratorLifetime(t *testing.T) {
	t.Run("given positioned iterator when parking or closing reference then release the leaf and reserve handle correctly", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		if err := r.Put("a", "value"); err != nil {
			t.Fatal(err)
		}
		it := r.Iterator()
		it.Seek("")
		if m.first.mu.TryLock() {
			m.first.mu.Unlock()
			t.Fatal("positioned iterator did not lock leaf")
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("point operation accepted on iterator's Ref")
				}
			}()
			r.Probe("a")
		}()
		it.Park()
		if it.Valid() || !m.first.mu.TryLock() {
			t.Fatal("Park did not release leaf")
		}
		m.first.mu.Unlock()
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("second iterator accepted")
				}
			}()
			r.Iterator()
		}()
		it.Seek("")
		r.Close()
		if it.Valid() || !m.first.mu.TryLock() {
			t.Fatal("Ref.Close did not close iterator")
		}
		m.first.mu.Unlock()
		it.Close()
	})
}

func TestConcurrentIterators(t *testing.T) {
	t.Run("given dirty leaf indexes when independent iterators seek concurrently then sorting and scans remain consistent", func(t *testing.T) {
		m := New[string, string]()
		writer := m.Ref()
		defer writer.Close()
		const keys = 1500
		rng := rand.New(rand.NewPCG(31, 19))
		for _, i := range rng.Perm(keys) {
			if err := writer.Put(fmt.Sprintf("%04d", i), fmt.Sprint(i)); err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var bad atomic.Bool
		for worker := 0; worker < 8; worker++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				r := m.Ref()
				defer r.Close()
				it := r.Iterator()
				defer it.Close()
				<-start
				for round := 0; round < 8; round++ {
					begin := (worker*73 + round*31) % keys
					it.Seek(fmt.Sprintf("%04d", begin))
					for i := begin; i < keys; i++ {
						k, v, ok := it.Next()
						if !ok || k != fmt.Sprintf("%04d", i) || v != fmt.Sprint(i) {
							bad.Store(true)
							return
						}
					}
					if it.Valid() {
						bad.Store(true)
						return
					}
				}
			}(worker)
		}
		close(start)
		wg.Wait()
		if bad.Load() {
			t.Fatal("concurrent scan mismatch")
		}
	})
	t.Run("given structural writers when scanning then preserve order unique keys and permanent records", func(t *testing.T) {
		m := New[string, string]()
		writer := m.Ref()
		defer writer.Close()
		const permanent = 500
		for i := 0; i < permanent; i++ {
			if err := writer.Put(fmt.Sprintf("%04d-a", i), "stable"); err != nil {
				t.Fatal(err)
			}
		}
		stop, done := make(chan struct{}), make(chan struct{})
		var bad atomic.Bool
		go func() {
			defer close(done)
			r := m.Ref()
			defer r.Close()
			for round := 0; round < 8; round++ {
				for i := 0; i < permanent; i++ {
					if err := r.Put(fmt.Sprintf("%04d-b", i), "transient"); err != nil {
						bad.Store(true)
						return
					}
				}
				for i := 0; i < permanent; i++ {
					if !r.Delete(fmt.Sprintf("%04d-b", i)) {
						bad.Store(true)
						return
					}
				}
			}
			close(stop)
		}()
		r := m.Ref()
		defer r.Close()
		it := r.Iterator()
		defer it.Close()
		for scan := 0; scan < 20; scan++ {
			it.Seek("")
			prev := ""
			stable := 0
			for it.Valid() {
				k, v, _ := it.Next()
				if k <= prev {
					bad.Store(true)
				}
				prev = k
				if strings.HasSuffix(k, "-a") {
					if v != "stable" {
						bad.Store(true)
					}
					stable++
				}
			}
			if stable != permanent {
				bad.Store(true)
			}
			select {
			case <-stop:
				scan = 20
			default:
			}
		}
		it.Park()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("iterator/structural writer stalled")
		}
		if bad.Load() {
			t.Fatal("scan during structural changes violated invariants")
		}
	})
}
