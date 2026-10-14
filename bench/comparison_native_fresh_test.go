// Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.

package bench

import (
	"context"
	"runtime/pprof"
	"testing"

	"github.com/glycerine/bufftree"
)

func benchmarkNativeWormholeFreshPut(b *testing.B, n int, keys []uint64) {
	idx := newNativeWormhole()
	for i := 0; i < n; i++ {
		idx.Put(nativeBenchKey(i), uint64(i))
	}
	defer func() { idx.Close() }()
	values := make([]uint64, len(keys))
	for i := range values {
		values[i] = uint64(n + i)
	}
	inserted := 0
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", "Wormhole"))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			idx.Close()
			idx = newNativeWormhole()
			for j := 0; j < n; j++ {
				idx.Put(nativeBenchKey(j), uint64(j))
			}
			inserted = 0
			pprof.SetGoroutineLabels(inserting)
			b.StartTimer()
		}
		// Unique odd keys are absent from the initial even-key load. Include
		// insertion/growth from n to 2*n records; reload outside the timer.
		idx.Put(keys[inserted], values[inserted])
		inserted++
	}
	b.StopTimer()
}

func benchmarkNativeTurtleFreshPut(b *testing.B, n int, keys []uint64) {
	idx := newNativeTurtle()
	for i := 0; i < n; i++ {
		idx.Put(nativeBenchKey(i), uint64(i))
	}
	values := make([]uint64, len(keys))
	for i := range values {
		values[i] = uint64(n + i)
	}
	inserted := 0
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", "Turtle"))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			idx = newNativeTurtle()
			for j := 0; j < n; j++ {
				idx.Put(nativeBenchKey(j), uint64(j))
			}
			inserted = 0
			pprof.SetGoroutineLabels(inserting)
			b.StartTimer()
		}
		// Unique odd keys are absent from the initial even-key load. Include
		// insertion/growth from n to 2*n records; reload outside the timer.
		idx.Put(keys[inserted], values[inserted])
		inserted++
	}
	b.StopTimer()
}

func benchmarkNativeTreeFreshPut(b *testing.B, n int, keys []uint64) {
	idx := bufftree.NewBPTree[uint64, uint64](nil)
	for i := 0; i < n; i++ {
		idx.Put(nativeBenchKey(i), uint64(i))
	}
	values := make([]uint64, len(keys))
	for i := range values {
		values[i] = uint64(n + i)
	}
	inserted := 0
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", "Tree"))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			idx = bufftree.NewBPTree[uint64, uint64](nil)
			for j := 0; j < n; j++ {
				idx.Put(nativeBenchKey(j), uint64(j))
			}
			inserted = 0
			pprof.SetGoroutineLabels(inserting)
			b.StartTimer()
		}
		// Unique odd keys are absent from the initial even-key load. Include
		// insertion/growth from n to 2*n records; reload outside the timer.
		idx.Put(keys[inserted], values[inserted])
		inserted++
	}
	b.StopTimer()
}

func benchmarkNativeGoMapFreshPut(b *testing.B, n int, keys []uint64) {
	idx := make(nativeUint64Map, n)
	for i := 0; i < n; i++ {
		idx.Put(nativeBenchKey(i), uint64(i))
	}
	values := make([]uint64, len(keys))
	for i := range values {
		values[i] = uint64(n + i)
	}
	inserted := 0
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", "GoMap"))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			idx = make(nativeUint64Map, n)
			for j := 0; j < n; j++ {
				idx.Put(nativeBenchKey(j), uint64(j))
			}
			inserted = 0
			pprof.SetGoroutineLabels(inserting)
			b.StartTimer()
		}
		// Unique odd keys are absent from the initial even-key load. Include
		// insertion/growth from n to 2*n records; reload outside the timer.
		idx.Put(keys[inserted], values[inserted])
		inserted++
	}
	b.StopTimer()
}

func benchmarkNativeTidwallFreshPut(b *testing.B, n int, keys []uint64) {
	idx := &nativeTidwallMap{}
	for i := 0; i < n; i++ {
		idx.Put(nativeBenchKey(i), uint64(i))
	}
	values := make([]uint64, len(keys))
	for i := range values {
		values[i] = uint64(n + i)
	}
	inserted := 0
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", "Tidwall"))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			idx = &nativeTidwallMap{}
			for j := 0; j < n; j++ {
				idx.Put(nativeBenchKey(j), uint64(j))
			}
			inserted = 0
			pprof.SetGoroutineLabels(inserting)
			b.StartTimer()
		}
		// Unique odd keys are absent from the initial even-key load. Include
		// insertion/growth from n to 2*n records; reload outside the timer.
		idx.Put(keys[inserted], values[inserted])
		inserted++
	}
	b.StopTimer()
}

func benchmarkNativeRBTreeFreshPut(b *testing.B, n int, keys []uint64) {
	idx := newNativeRBTree()
	for i := 0; i < n; i++ {
		idx.Put(nativeBenchKey(i), uint64(i))
	}
	values := make([]uint64, len(keys))
	for i := range values {
		values[i] = uint64(n + i)
	}
	inserted := 0
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", "RBTree"))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			idx = newNativeRBTree()
			for j := 0; j < n; j++ {
				idx.Put(nativeBenchKey(j), uint64(j))
			}
			inserted = 0
			pprof.SetGoroutineLabels(inserting)
			b.StartTimer()
		}
		// Unique odd keys are absent from the initial even-key load. Include
		// insertion/growth from n to 2*n records; reload outside the timer.
		idx.Put(keys[inserted], values[inserted])
		inserted++
	}
	b.StopTimer()
}

func benchmarkNativeInsdictFreshPut(b *testing.B, n int, keys []uint64) {
	idx := newComparisonInsdict[uint64, uint64](n)
	for i := 0; i < n; i++ {
		idx.Put(nativeBenchKey(i), uint64(i))
	}
	values := make([]uint64, len(keys))
	for i := range values {
		values[i] = uint64(n + i)
	}
	inserted := 0
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", "Insdict"))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			idx = newComparisonInsdict[uint64, uint64](n)
			for j := 0; j < n; j++ {
				idx.Put(nativeBenchKey(j), uint64(j))
			}
			inserted = 0
			pprof.SetGoroutineLabels(inserting)
			b.StartTimer()
		}
		// Unique odd keys are absent from the initial even-key load. Include
		// insertion/growth from n to 2*n records; reload outside the timer.
		idx.Put(keys[inserted], values[inserted])
		inserted++
	}
	b.StopTimer()
}
