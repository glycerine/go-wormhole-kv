// Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.

package bench

import (
	"math/rand"
	"testing"
)

func reportIteration(b *testing.B, keys uint64) {
	if keys == 0 {
		return
	}
	b.ReportMetric(float64(keys)/b.Elapsed().Seconds(), "entries/s")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(keys), "iter_ns/key")
	b.ReportMetric(float64(keys)/float64(b.N), "keys/op")
}

func benchLoadSize() int { return loadSize() }

// Keep the key permutation and uniform RNG sequence identical to
// github.com/glycerine/bufftree/bench, so measurements stay comparable.
// Even keys are loaded records; odd keys are disjoint miss keys.
func benchKey(rank int) string   { return binaryKey(permutedKey(rank)) }
func benchValue(rank int) string { return binaryKey(uint64(rank)) }

// Consume every returned value identically, without decoding a numeric value.
func comparisonValueSink(v string) uint64 {
	if len(v) == 0 {
		return 0
	}
	return uint64(v[len(v)-1])
}

type benchPair struct{ key, value string }

func comparisonData(n int) []benchPair {
	entries := make([]benchPair, n)
	for i := range entries {
		entries[i] = benchPair{benchKey(i), benchValue(i)}
	}
	return entries
}

type benchOp struct {
	key       string
	miss      string
	value     string
	alternate string
	length    int
}

func benchTrace(n, count, maxLen int) []benchOp {
	rng := rand.New(rand.NewSource(2023))
	ops := make([]benchOp, count)
	for i := range ops {
		rank := rng.Intn(n)
		rng.Intn(100) // Consume the paper benchmarks' mixed-workload roll.
		ops[i] = benchOp{key: benchKey(rank), miss: binaryKey(permutedKey(rank) | 1), value: benchValue(i), alternate: benchValue(i + count), length: rng.Intn(maxLen + 1)}
	}
	return ops
}
