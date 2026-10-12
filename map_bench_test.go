package wormhole

import (
	"fmt"
	"github.com/glycerine/go-wormhole-kv/internal/cpuaffinity"
	"sync/atomic"
	"testing"
)

func BenchmarkMapProbe(b *testing.B) {
	for _, n := range []int{1024, 65536} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			m := New[string, string]()
			r := m.Ref()
			defer r.Close()
			keys := make([]string, n)
			for i := range keys {
				keys[i] = fmt.Sprintf("key-%08d", i)
				if err := r.Put(keys[i], "value"); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !r.Probe(keys[i&(n-1)]) {
					b.Fatal("missing key")
				}
			}
		})
	}
}

func BenchmarkMapProbeParallel(b *testing.B) {
	m := New[string, string]()
	r := m.Ref()
	const n = 65536
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%08d", i)
		if err := r.Put(keys[i], "value"); err != nil {
			b.Fatal(err)
		}
	}
	r.Close()
	cpus, err := cpuaffinity.CPUs()
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("parallel probe workers pin in order to CPUs %v", cpus)
	var worker atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := worker.Add(1) - 1
		restore, err := cpuaffinity.Pin(cpus[id%uint64(len(cpus))])
		if err != nil {
			b.Error(err)
			return
		}
		defer func() {
			if err := restore(); err != nil {
				b.Error(err)
			}
		}()
		r := m.Ref()
		defer r.Close()
		i := int(id+1) * 997
		for pb.Next() {
			if !r.Probe(keys[i&(n-1)]) {
				b.Error("missing key")
			}
			i += 17
		}
	})
}
