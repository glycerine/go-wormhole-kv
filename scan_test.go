package wormhole

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"testing"
)

func exerciseScan[K cmp.Ordered](t *testing.T, keys []K) {
	t.Helper()
	m := New[K, []int]()
	r := m.Ref()
	defer r.Close()
	for _, i := range rand.New(rand.NewPCG(41, 73)).Perm(len(keys)) {
		if err := r.Put(keys[i], []int{i}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < len(keys); i += 4 {
		r.Delete(keys[i])
	}
	// Exercise reuse of deleted slots and sorted-prefix invalidation.
	for i := 0; i < len(keys); i += 8 {
		if err := r.Put(keys[i], []int{i + len(keys)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, start := range []K{keys[0], keys[len(keys)/2], keys[len(keys)-1]} {
		for _, limit := range []int{-1, 0, 1, 127, 128, 129, len(keys) * 2} {
			var wantKeys []K
			var wantValues []int
			it := r.Iterator()
			it.Seek(start)
			for i := 0; i < limit; i++ {
				k, v, ok := it.Next()
				if !ok {
					break
				}
				wantKeys = append(wantKeys, k)
				wantValues = append(wantValues, v[0])
			}
			it.Close()
			for _, stop := range []int{1, 173, len(keys) * 2} {
				seen := 0
				count := r.Scan(start, limit, func(k K, v []int) bool {
					if seen >= len(wantKeys) || orderedCompare(k, wantKeys[seen]) != 0 || len(v) != 1 || v[0] != wantValues[seen] {
						t.Fatal("scan differs from iterator", start, limit, seen)
					}
					seen++
					return seen < stop
				})
				if want := min(stop, len(wantKeys)); count != want || seen != want {
					t.Fatal("wrong visited count", count, seen, want)
				}
			}
		}
	}
	if allocs := testing.AllocsPerRun(10, func() { r.Scan(keys[0], len(keys), func(K, []int) bool { return true }) }); allocs != 0 {
		t.Fatal("scan allocates", allocs)
	}
}

func TestScanOrderedTypes(t *testing.T) {
	t.Run("uint64", func(t *testing.T) {
		keys := make([]uint64, 2000)
		for i := range keys {
			keys[i] = uint64(i) * 0x9e3779b97f4a7c15
		}
		exerciseScan(t, keys)
	})
	t.Run("int", func(t *testing.T) {
		keys := make([]int, 2000)
		for i := range keys {
			keys[i] = i - 1000
		}
		exerciseScan(t, keys)
	})
	t.Run("string", func(t *testing.T) {
		keys := make([]string, 2000)
		for i := range keys {
			keys[i] = fmt.Sprintf("key-%04d", i)
		}
		exerciseScan(t, keys)
	})
	t.Run("float64", func(t *testing.T) {
		keys := make([]float64, 2000)
		for i := range keys {
			keys[i] = float64(i) - 1000
		}
		keys[0] = math.Inf(-1)
		keys[len(keys)-2] = math.Inf(1)
		keys[len(keys)-1] = math.NaN()
		exerciseScan(t, keys)
	})
}

func TestScanPanicAndReentry(t *testing.T) {
	m := New[uint64, uint64]()
	r := m.Ref()
	defer r.Close()
	for i := uint64(0); i < 1000; i++ {
		if err := r.Put(i, i); err != nil {
			t.Fatal(err)
		}
	}
	actions := []func(){func() { panic("visit") }, func() { r.Get(0) }, func() { r.Close() }, func() { r.Iterator() }, func() { r.Scan(0, 1, func(uint64, uint64) bool { return true }) }}
	for _, action := range actions {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("expected callback panic")
				}
			}()
			r.Scan(0, 1000, func(k, v uint64) bool {
				if k == 300 {
					action()
				}
				return true
			})
		}()
		if r.inCallback {
			t.Fatal("callback flag retained")
		}
		for l := m.first; l != nil; l = l.next {
			if !l.mu.TryLock() {
				t.Fatal("leaf lock retained")
			}
			l.mu.Unlock()
		}
		if v, ok := r.Get(300); !ok || v != 300 {
			t.Fatal("Ref unusable after panic")
		}
	}
	it := r.Iterator()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("scan allowed reserved Ref")
			}
		}()
		r.Scan(0, 1, func(uint64, uint64) bool { return true })
	}()
	it.Close()
	r.Close()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("scan allowed closed Ref")
			}
		}()
		r.Scan(0, 1, func(uint64, uint64) bool { return true })
	}()
}

func TestScanConcurrentMutation(t *testing.T) {
	m := New[uint64, uint64]()
	r := m.Ref()
	defer r.Close()
	for i := uint64(0); i < 2000; i++ {
		key := i * 2
		if err := r.Put(key, key); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Go(func() {
			w := m.Ref()
			defer w.Close()
			for i := 0; i < 2000; i++ {
				key := (uint64(i*3+worker) * 0x9e3779b97f4a7c15) | 1
				if err := w.Put(key, key); err != nil {
					t.Error(err)
					return
				}
				if i%2 == 0 {
					w.Delete(key)
				}
			}
		})
	}
	for pass := 0; pass < 50; pass++ {
		var previous uint64
		seen, persistent := 0, 0
		n := r.Scan(0, 20000, func(k, v uint64) bool {
			if seen > 0 && k <= previous || k != v {
				t.Error("scan order/value during mutation")
			}
			previous = k
			seen++
			if k&1 == 0 {
				persistent++
			}
			return true
		})
		if n != seen || persistent != 2000 {
			t.Fatal("lost persistent records during mutation", n, seen, persistent)
		}
	}
	wg.Wait()
}

func TestScanLargeValues(t *testing.T) {
	m := New[uint64, [64]uint64]()
	r := m.Ref()
	defer r.Close()
	for key := uint64(0); key < 300; key++ {
		var value [64]uint64
		for i := range value {
			value[i] = key + uint64(i)
		}
		if err := r.Put(key, value); err != nil {
			t.Fatal(err)
		}
	}
	seen := uint64(100)
	count := r.Scan(100, 201, func(key uint64, value [64]uint64) bool {
		if key != seen {
			t.Fatal("large-value scan order", key, seen)
		}
		for i, v := range value {
			if v != key+uint64(i) {
				t.Fatal("large-value scan contents", key, i)
			}
		}
		seen++
		return true
	})
	if count != 200 || seen != 300 {
		t.Fatal("large-value scan count", count, seen)
	}
}
