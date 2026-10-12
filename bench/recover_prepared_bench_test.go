package bench

import (
	"cmp"
	wormhole "github.com/glycerine/go-wormhole-kv"
	"testing"
)

// Prepared keys are built before timing. This supplements the comparison
// suite's ordinary Put, since changing Key must also protect PutKey throughput.
func BenchmarkRecoverPrepared(b *testing.B) {
	const n = 65536
	numeric := make([]uint64, 2*n)
	text := make([]string, 2*n)
	for i := range numeric {
		numeric[i] = uint64(i) * 0x9e3779b97f4a7c15
		text[i] = binaryKey(numeric[i])
	}
	b.Run("uint64", func(b *testing.B) { recoverPreparedCases(b, numeric, n) })
	b.Run("string", func(b *testing.B) { recoverPreparedCases(b, text, n) })
}

func recoverPreparedCases[K cmp.Ordered](b *testing.B, keys []K, n int) {
	prepared := make([]wormhole.Key[K], len(keys))
	for i, key := range keys {
		prepared[i] = wormhole.PrepareKey(key)
	}
	for _, fresh := range []bool{false, true} {
		name := "ProbeHit"
		if fresh {
			name = "FreshPut"
		}
		run := func(b *testing.B) {
			var ref *wormhole.Ref[K, uint64]
			load := func() {
				if ref != nil {
					ref.Close()
				}
				m := wormhole.NewWithOptions[K, uint64](wormhole.Options{MetaCache: *wormholeMetaCache})
				ref = m.Ref()
				for i := 0; i < n; i++ {
					if err := ref.Put(keys[i], uint64(i)); err != nil {
						b.Fatal(err)
					}
				}
			}
			load()
			defer func() { ref.Close() }()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				index := i % n
				if fresh {
					if i != 0 && index == 0 {
						b.StopTimer()
						load()
						b.StartTimer()
					}
					if err := ref.PutKey(prepared[n+index], uint64(index)); err != nil {
						b.Fatal(err)
					}
				} else if !ref.ProbeKey(prepared[index]) {
					b.Fatal("missing key")
				}
			}
			b.StopTimer()
		}
		cases := pinComparisonCases([]comparisonBenchmark{{name: name, run: run}})
		b.Run(name, cases[0].run)
	}
}
