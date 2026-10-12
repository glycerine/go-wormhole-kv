package wormhole

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func deleteReference(want map[string]string, start string, end *string) int64 {
	var n int64
	for k := range want {
		if k >= start && (end == nil || k < *end) {
			delete(want, k)
			n++
		}
	}
	return n
}

func TestDeleteRangeBounds(t *testing.T) {
	for _, bounds := range [][2]string{{"", ""}, {"z", "a"}, {"", "\x00"}, {"\x00", "\xff"}, {"0100", "0101"}, {"0100", "0500"}, {"0000", "9999"}, {"not-present", "z"}, {"\xff", "\xff\xff"}} {
		t.Run(fmt.Sprintf("given keys across leaves when deleting [%q,%q) then preserve exclusive endpoint", bounds[0], bounds[1]), func(t *testing.T) {
			m := New[string, string]()
			r := m.Ref()
			defer r.Close()
			want := map[string]string{"": "empty", "\x00": "zero", "\xff": "high", "\xff\xff": "last"}
			for i := 0; i < 1000; i++ {
				want[fmt.Sprintf("%04d", i)] = fmt.Sprint(i)
			}
			for k, v := range want {
				if err := r.Put(k, v); err != nil {
					t.Fatal(err)
				}
			}
			n := deleteReference(want, bounds[0], &bounds[1])
			if got := r.DeleteRange(bounds[0], bounds[1]); got != n {
				t.Fatalf("removed=%d want %d", got, n)
			}
			checkMap(t, m, want)
			checkScan(t, r, want, "")
			if got := r.DeleteRange(bounds[0], bounds[1]); got != 0 {
				t.Fatal("second deletion removed keys")
			}
			checkMap(t, m, want)
		})
	}
	t.Run("given empty and unbounded ranges when deleting from a key then distinguish no bound from empty bound", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		if r.DeleteFrom("") != 0 {
			t.Fatal("nonzero empty-map count")
		}
		for _, k := range []string{"", "a", "a\x00", "b", "\xff"} {
			if err := r.Put(k, k); err != nil {
				t.Fatal(err)
			}
		}
		if r.DeleteRange("", "") != 0 || r.DeleteFrom("b") != 2 || r.DeleteFrom("") != 3 {
			t.Fatal("incorrect bounded/unbounded counts")
		}
		checkMap(t, m, map[string]string{})
		checkScan(t, r, map[string]string{}, "")
	})
}

func runRangeProperty(t testing.TB, seed uint64, steps int) {
	t.Helper()
	m := New[string, string]()
	r := m.Ref()
	defer r.Close()
	want := map[string]string{}
	rng := rand.New(rand.NewPCG(seed, seed^31))
	keys := make([]string, 1800)
	for i := range keys {
		keys[i] = fmt.Sprintf("%c-%04d", byte(i), i)
	}
	for i := 0; i < 140; i++ {
		keys[i] = "p" + strings.Repeat("\x00", i)
	}
	keys[140], keys[141] = "", "\xff"
	for _, i := range rng.Perm(600) {
		if err := r.Put(keys[i], "initial"); err != nil {
			t.Fatal(err)
		}
		want[keys[i]] = "initial"
	}
	for step := 0; step < steps; step++ {
		k := keys[rng.IntN(len(keys))]
		switch rng.IntN(20) {
		case 0:
			end := keys[rng.IntN(len(keys))]
			n := deleteReference(want, k, &end)
			if got := r.DeleteRange(k, end); got != n {
				t.Fatalf("seed=%d step=%d range count=%d want %d", seed, step, got, n)
			}
		case 1:
			n := deleteReference(want, k, nil)
			if got := r.DeleteFrom(k); got != n {
				t.Fatalf("seed=%d step=%d unbounded count=%d want %d", seed, step, got, n)
			}
		case 2, 3:
			_, exists := want[k]
			if r.Delete(k) != exists {
				t.Fatal("point delete mismatch")
			}
			delete(want, k)
		case 4:
			checkScan(t, r, want, k)
		default:
			v := fmt.Sprint(step)
			if err := r.Put(k, v); err != nil {
				t.Fatal(err)
			}
			want[k] = v
		}
		if step%137 == 0 {
			checkMap(t, m, want)
			checkScan(t, r, want, "")
		}
	}
	checkMap(t, m, want)
	checkScan(t, r, want, "")
	n := int64(len(want))
	if got := r.DeleteFrom(""); got != n {
		t.Fatalf("final clear=%d want %d", got, n)
	}
	checkMap(t, m, map[string]string{})
	if m.first.next != nil {
		t.Fatal("unbounded clear retained successor leaves")
	}
}

func TestDeleteRangeRandomized(t *testing.T) {
	for seed := uint64(0); seed < 12; seed++ {
		t.Run(fmt.Sprintf("given seed %d when mixing range deletion point updates and scans then match reference model", seed), func(t *testing.T) { runRangeProperty(t, seed, 5000) })
	}
}

func FuzzRangeOperations(f *testing.F) {
	for _, seed := range []uint64{0, 1, 42, 65535} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { runRangeProperty(t, seed, 1500) })
}

func TestConcurrentRangeDeletion(t *testing.T) {
	t.Run("given disjoint range owners and scanning readers when removing whole leaf spans then preserve permanent keys", func(t *testing.T) {
		m := New[string, string]()
		loader := m.Ref()
		defer loader.Close()
		const workers, keys = 4, 500
		want := map[string]string{}
		for w := 0; w < workers; w++ {
			permanent := fmt.Sprintf("%d-stable", w)
			want[permanent] = "stable"
			if err := loader.Put(permanent, "stable"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < keys; i++ {
				if err := loader.Put(fmt.Sprintf("%d-range-%04d", w, i), "transient"); err != nil {
					t.Fatal(err)
				}
			}
		}
		var writers, readers sync.WaitGroup
		var bad atomic.Bool
		start, stop := make(chan struct{}), make(chan struct{})
		for w := 0; w < workers; w++ {
			writers.Add(1)
			go func(w int) {
				defer writers.Done()
				r := m.Ref()
				defer r.Close()
				<-start
				for round := 0; round < 6; round++ {
					if round != 0 {
						for i := 0; i < keys; i++ {
							if err := r.Put(fmt.Sprintf("%d-range-%04d", w, i), "transient"); err != nil {
								bad.Store(true)
								return
							}
						}
					}
					if n := r.DeleteRange(fmt.Sprintf("%d-range-", w), fmt.Sprintf("%d-range.", w)); n != keys {
						bad.Store(true)
						return
					}
				}
			}(w)
		}
		for w := 0; w < 3; w++ {
			readers.Add(1)
			go func() {
				defer readers.Done()
				r := m.Ref()
				defer r.Close()
				it := r.Iterator()
				defer it.Close()
				<-start
				for {
					select {
					case <-stop:
						return
					default:
					}
					it.Seek("")
					previous, permanent := "", 0
					for it.Valid() {
						k, v, _ := it.Next()
						if k <= previous {
							bad.Store(true)
						}
						previous = k
						if strings.HasSuffix(k, "-stable") {
							if v != "stable" {
								bad.Store(true)
							}
							permanent++
						}
					}
					if permanent != workers {
						bad.Store(true)
					}
				}
			}()
		}
		close(start)
		done := make(chan struct{})
		go func() { writers.Wait(); close(stop); readers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("range/iterator operations stalled")
		}
		if bad.Load() {
			t.Fatal("concurrent range or scan mismatch")
		}
		checkMap(t, m, want)
		checkScan(t, loader, want, "")
	})
}
