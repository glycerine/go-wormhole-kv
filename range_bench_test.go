package wormhole

import (
	"fmt"
	"testing"
)

func BenchmarkIteratorSeek(b *testing.B) {
	m := New[string, string]()
	r := m.Ref()
	defer r.Close()
	const n = 65536
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%08d", i)
		if err := r.Put(keys[i], "value"); err != nil {
			b.Fatal(err)
		}
	}
	it := r.Iterator()
	defer it.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := keys[i&(n-1)]
		it.Seek(key)
		if k, _, ok := it.Peek(); !ok || k != key {
			b.Fatal("seek mismatch")
		}
	}
}

func BenchmarkIteratorScan(b *testing.B) {
	m := New[string, string]()
	r := m.Ref()
	defer r.Close()
	const n = 16384
	for i := 0; i < n; i++ {
		if err := r.Put(fmt.Sprintf("key-%08d", i), "value"); err != nil {
			b.Fatal(err)
		}
	}
	it := r.Iterator()
	defer it.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		it.Seek("")
		count := 0
		for it.Valid() {
			it.Next()
			count++
		}
		if count != n {
			b.Fatal("scan count mismatch")
		}
	}
	b.ReportMetric(n, "keys/scan")
}

// Include repopulation in the timed cycle rather than hiding allocation and
// insertion costs behind StopTimer. Each cycle inserts and removes 1,024 keys.
func BenchmarkDeleteRangeCycle(b *testing.B) {
	m := New[string, string]()
	r := m.Ref()
	defer r.Close()
	const n = 1024
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%08d", i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, key := range keys {
			if err := r.Put(key, "value"); err != nil {
				b.Fatal(err)
			}
		}
		if count := r.DeleteFrom(""); count != n {
			b.Fatal("range count mismatch")
		}
	}
	b.ReportMetric(n, "keys/cycle")
}
