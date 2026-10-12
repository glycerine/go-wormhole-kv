package wormhole

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCallbacks(t *testing.T) {
	t.Run("given absent and existing keys when merging inspecting and updating then invoke once with native no-op semantics", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		calls := 0
		if err := r.Merge("k", func(v string, ok bool) (string, bool) {
			calls++
			if v != "" || ok {
				t.Fatal("absent merge input")
			}
			return "ignored", false
		}); err != nil {
			t.Fatal(err)
		}
		if r.Probe("k") || m.Len() != 0 || calls != 1 {
			t.Fatal("merge no-op inserted record")
		}
		if err := r.Merge("k", func(v string, ok bool) (string, bool) { calls++; return "value", true }); err != nil {
			t.Fatal(err)
		}
		if err := r.Merge("k", func(v string, ok bool) (string, bool) {
			calls++
			if v != "value" || !ok {
				t.Fatal("existing merge input")
			}
			return "ignored", false
		}); err != nil {
			t.Fatal(err)
		}
		if !r.Inspect("k", func(v string, ok bool) {
			calls++
			if v != "value" || !ok {
				t.Fatal("inspect input")
			}
		}) {
			t.Fatal("inspect missed key")
		}
		if r.Inspect("absent", func(v string, ok bool) {
			calls++
			if v != "" || ok {
				t.Fatal("absent inspect input")
			}
		}) {
			t.Fatal("inspect found absent key")
		}
		if r.Update("absent", func(v string, ok bool) string {
			calls++
			if v != "" || ok {
				t.Fatal("absent update input")
			}
			return "ignored"
		}) {
			t.Fatal("update inserted absent key")
		}
		if !r.Update("k", func(v string, ok bool) string { calls++; return v + "\x00longer" }) {
			t.Fatal("update missed key")
		}
		if calls != 7 {
			t.Fatalf("callback count=%d", calls)
		}
		checkMap(t, m, map[string]string{"k": "value\x00longer"})
	})
	t.Run("given a full leaf when Merge inserts then split through the ordinary insertion path", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		want := map[string]string{}
		for i := 0; i < 1000; i++ {
			k, v := fmt.Sprintf("%04d", i), fmt.Sprint(i)
			if err := r.Merge(k, func(_ string, _ bool) (string, bool) { return v, true }); err != nil {
				t.Fatal(err)
			}
			want[k] = v
		}
		checkMap(t, m, want)
		checkScan(t, r, want, "")
	})
	t.Run("given excessive split anchor when Merge inserts then report failure after one callback", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		prefix := strings.Repeat("x", 65535)
		want := map[string]string{}
		for i := 0; i < leafCapacity; i++ {
			k := prefix + string(byte(i))
			if err := r.Put(k, "old"); err != nil {
				t.Fatal(err)
			}
			want[k] = "old"
		}
		calls := 0
		err := r.Merge(prefix+"\xff", func(_ string, _ bool) (string, bool) { calls++; return "new", true })
		if !errors.Is(err, ErrAnchorTooLong) || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
		checkMap(t, m, want)
	})
}

func TestCallbackPanicSafety(t *testing.T) {
	t.Run("given panicking or reentrant callbacks when unwinding then release locks and preserve values", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		if err := r.Put("k", "old"); err != nil {
			t.Fatal(err)
		}
		operations := []func(){
			func() { _ = r.Merge("k", func(string, bool) (string, bool) { panic("merge") }) },
			func() { r.Inspect("k", func(string, bool) { panic("inspect") }) },
			func() { r.Update("k", func(string, bool) string { panic("update") }) },
			func() { _ = r.Merge("k", func(string, bool) (string, bool) { r.Probe("k"); return "bad", true }) },
			func() { r.Update("k", func(string, bool) string { r.Close(); return "bad" }) },
		}
		for _, op := range operations {
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("callback did not panic")
					}
				}()
				op()
			}()
			if r.inCallback || !m.first.mu.TryLock() {
				t.Fatal("callback retained flag or lock")
			}
			m.first.mu.Unlock()
			if value, ok := r.Get("k"); !ok || value != "old" {
				t.Fatal("panicking callback changed record")
			}
		}
	})
}

func TestConcurrentMerge(t *testing.T) {
	t.Run("given concurrent callback increments when updating a shared key then lose no increments", func(t *testing.T) {
		m := New[string, string]()
		var wg sync.WaitGroup
		var bad atomic.Bool
		const workers, rounds = 8, 1000
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := m.Ref()
				defer r.Close()
				for i := 0; i < rounds; i++ {
					if err := r.Merge("counter", func(v string, exists bool) (string, bool) {
						n := 0
						if exists {
							var err error
							n, err = strconv.Atoi(v)
							if err != nil {
								bad.Store(true)
							}
						}
						return strconv.Itoa(n + 1), true
					}); err != nil {
						bad.Store(true)
					}
				}
			}()
		}
		wg.Wait()
		if bad.Load() {
			t.Fatal("merge error")
		}
		checkMap(t, m, map[string]string{"counter": strconv.Itoa(workers * rounds)})
	})
}
