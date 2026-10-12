package wormhole

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestReplacementRetainedValues(t *testing.T) {
	t.Run("given retained read results when replacing through every write API then preserve previous string bytes", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		const key = "key"
		values := []string{strings.Repeat("a", 4096), "short", strings.Repeat("b", 8192), "", "final"}
		if err := r.Put(key, values[0]); err != nil {
			t.Fatal(err)
		}
		var retained []string
		first, _ := r.Get(key)
		retained = append(retained, first)
		r.Inspect(key, func(v string, exists bool) {
			if !exists {
				t.Fatal("missing inspection")
			}
			retained = append(retained, v)
		})
		it := r.Iterator()
		it.Seek(key)
		_, peek, ok := it.Peek()
		if !ok {
			t.Fatal("missing peek")
		}
		retained = append(retained, peek)
		it.Close()
		if err := r.PutKey(PrepareKey(key), values[1]); err != nil {
			t.Fatal(err)
		}
		second, _ := r.Get(key)
		retained = append(retained, second)
		if err := r.Merge(key, func(v string, exists bool) (string, bool) {
			if !exists || v != values[1] {
				t.Fatal("wrong merge input")
			}
			retained = append(retained, v)
			return values[2], true
		}); err != nil {
			t.Fatal(err)
		}
		third, _ := r.Get(key)
		retained = append(retained, third)
		if !r.Update(key, func(v string, exists bool) string {
			if !exists || v != values[2] {
				t.Fatal("wrong update input")
			}
			retained = append(retained, v)
			return values[3]
		}) {
			t.Fatal("missing update")
		}
		if err := r.Put(key, values[4]); err != nil {
			t.Fatal(err)
		}
		want := []string{values[0], values[0], values[0], values[1], values[1], values[2], values[2]}
		for i, v := range want {
			if retained[i] != v {
				t.Fatalf("retained result %d changed", i)
			}
		}
		if got, ok := r.Get(key); !ok || got != values[4] || m.Len() != 1 {
			t.Fatal("wrong final record/count")
		}
	})
}

func TestConcurrentValueReplacement(t *testing.T) {
	t.Run("given readers and writers on a moving key when replacing and splitting merging then read coherent immutable strings", func(t *testing.T) {
		m := New[string, string]()
		const key = "key-0128"
		values := []string{"", strings.Repeat("A", 4096), strings.Repeat("B", 8192)}
		r := m.Ref()
		if err := r.Put(key, values[0]); err != nil {
			t.Fatal(err)
		}
		r.Close()
		stop := make(chan struct{})
		ready := make(chan struct{}, 4)
		var readers, writers sync.WaitGroup
		for range 4 {
			readers.Go(func() {
				r := m.Ref()
				defer r.Close()
				check := func() {
					v, ok := r.Get(key)
					if !ok || v != values[0] && v != values[1] && v != values[2] {
						t.Errorf("incoherent read: exists=%v length=%d", ok, len(v))
					}
				}
				check()
				ready <- struct{}{}
				for {
					select {
					case <-stop:
						return
					default:
						check()
					}
				}
			})
		}
		for range 4 {
			<-ready
		}
		for worker := range 2 {
			writers.Go(func() {
				r := m.Ref()
				defer r.Close()
				for i := range 3000 {
					value := values[(i+worker)%len(values)]
					switch i % 3 {
					case 0:
						if err := r.Put(key, value); err != nil {
							t.Error(err)
							return
						}
					case 1:
						if err := r.Merge(key, func(_ string, exists bool) (string, bool) {
							if !exists {
								t.Error("merge missed")
							}
							return value, true
						}); err != nil {
							t.Error(err)
							return
						}
					case 2:
						if !r.Update(key, func(_ string, exists bool) string {
							if !exists {
								t.Error("update missed")
							}
							return value
						}) {
							t.Error("update missed")
							return
						}
					}
				}
			})
		}
		writers.Go(func() {
			r := m.Ref()
			defer r.Close()
			for range 8 {
				for i := range 512 {
					if i == 128 {
						continue
					}
					if err := r.Put(fmt.Sprintf("key-%04d", i), "neighbor"); err != nil {
						t.Error(err)
						return
					}
				}
				for i := range 512 {
					if i != 128 {
						r.Delete(fmt.Sprintf("key-%04d", i))
					}
				}
			}
		})
		writers.Wait()
		close(stop)
		readers.Wait()
		if m.Len() != 1 {
			t.Fatal("wrong final count")
		}
	})
}
