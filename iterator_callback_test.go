package wormhole

import "testing"

func TestIteratorInspect(t *testing.T) {
	t.Run("given positioned and invalid iterators when inspecting then invoke once without advancing", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		if err := r.Put("\x00key", "\xffvalue"); err != nil {
			t.Fatal(err)
		}
		it := r.Iterator()
		defer it.Close()
		check := func(wantKey, wantValue string, wantExists bool) {
			t.Helper()
			beforeKey, beforeValue, beforeExists := it.Peek()
			calls := 0
			gotExists := it.Inspect(func(key, value string, exists bool) {
				calls++
				if key != wantKey || value != wantValue || exists != wantExists || it.Valid() != wantExists {
					t.Fatal("unexpected callback record", key, value, exists)
				}
				if k, v, ok := it.Peek(); k != key || v != value || ok != exists {
					t.Fatal("read-only Peek changed inside callback")
				}
			})
			if calls != 1 || gotExists != wantExists {
				t.Fatal("wrong callback count or result")
			}
			if key, value, exists := it.Peek(); key != beforeKey || value != beforeValue || exists != beforeExists {
				t.Fatal("Inspect changed the position")
			}
		}
		check("", "", false) // newly created
		it.Seek("")
		check("\x00key", "\xffvalue", true)
		check("\x00key", "\xffvalue", true)
		if m.first.mu.TryLock() {
			m.first.mu.Unlock()
			t.Fatal("Inspect released the iterator lock")
		}
		it.Skip(1)
		check("", "", false) // exhausted
		it.Seek("")
		it.Park()
		check("", "", false) // parked
		it.Close()
		if value, ok := r.Get("\x00key"); !ok || value != "\xffvalue" {
			t.Fatal("iterator failed to return its Ref")
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("Inspect accepted a closed iterator")
				}
			}()
			it.Inspect(func(string, string, bool) { t.Fatal("closed callback invoked") })
		}()
	})
	t.Run("given panicking or reentrant inspection when unwinding then preserve position lock and usability", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		if err := r.Put("k", "v"); err != nil {
			t.Fatal(err)
		}
		it := r.Iterator()
		defer it.Close()
		it.Seek("")
		operations := []func(){
			func() { panic("callback") },
			func() { it.Seek("k") },
			func() { it.Next() },
			func() { it.Skip(0) },
			func() { it.Park() },
			func() { it.Close() },
			func() { r.Close() },
			func() { r.Get("k") },
			func() { it.Inspect(func(string, string, bool) {}) },
		}
		for _, op := range operations {
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("callback operation did not panic")
					}
				}()
				it.Inspect(func(string, string, bool) { op() })
			}()
			if r.inCallback || it.ref != r || r.iterator != it {
				t.Fatal("callback flag or ownership corrupted")
			}
			if key, value, ok := it.Peek(); !ok || key != "k" || value != "v" {
				t.Fatal("callback panic changed position")
			}
			if m.first.mu.TryLock() {
				m.first.mu.Unlock()
				t.Fatal("callback panic released iterator lock")
			}
			it.Inspect(func(string, string, bool) {})
		}
		it.Park()
		if !m.first.mu.TryLock() {
			t.Fatal("Park failed to release iterator lock")
		}
		m.first.mu.Unlock()
	})
}
