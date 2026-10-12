// Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.

package bench

import "math/rand"

func nativeBenchKey(rank int) uint64 {
	const mask = uint64(1<<63 - 1)
	x := uint64(rank) & mask
	x ^= x >> 30
	x = x * 0x3f58476d1ce4e5b9 & mask
	x ^= x >> 27
	x = x * 0x14d049bb133111eb & mask
	x ^= x >> 31
	return x << 1
}

type nativeBenchOp struct {
	key    uint64
	length int
}

func nativeBenchTrace(n, count, maxLen int) []nativeBenchOp {
	rng := rand.New(rand.NewSource(2023))
	ops := make([]nativeBenchOp, count)
	for i := range ops {
		rank := rng.Intn(n)
		rng.Intn(100) // Consume the paper benchmarks' mixed-workload roll.
		ops[i] = nativeBenchOp{key: nativeBenchKey(rank), length: rng.Intn(maxLen + 1)}
	}
	return ops
}
