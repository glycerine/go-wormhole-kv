// Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.

package bench

import (
	"context"
	"runtime/pprof"
	"testing"
)

func benchmarkFreshPut(b *testing.B, layout benchPointLayout, entries []benchPair, keys, values []string) {
	idx := loadComparisonPoints(layout, entries)
	defer func() { closeComparison(idx) }()
	n := len(entries)
	inserted := 0
	background := context.Background()
	inserting := pprof.WithLabels(background, pprof.Labels("phase", "fresh-put", "container", layout.name))
	pprof.SetGoroutineLabels(inserting)
	defer pprof.SetGoroutineLabels(background)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if inserted == n {
			b.StopTimer()
			pprof.SetGoroutineLabels(background)
			closeComparison(idx)
			idx = loadComparisonPoints(layout, entries)
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
