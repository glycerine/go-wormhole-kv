package wormhole

import (
	"fmt"
	"testing"
	"unsafe"
)

var benchmarkLeaf *leaf[string, string]

func BenchmarkMetaJump(b *testing.B) {
	for _, n := range []int{1024, 16384} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			first := &leaf[string, string]{}
			table := newMetaTable[string, string](4096)
			table.put(newMetadata("", crcSeed, first))
			prev := first
			keys := make([]string, n)
			for i := range keys {
				keys[i] = fmt.Sprintf("%08x", i)
				l := &leaf[string, string]{anchor: keys[i], prev: prev}
				prev.next = l
				table.insertAnchor(l)
				prev = l
			}
			b.Run("anchor", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(unsafe.Sizeof(metadata[string, string]{})), "bytes/meta")
				for i := 0; i < b.N; i++ {
					benchmarkLeaf = table.jump(keys[i&(n-1)])
				}
			})
			for i := range keys {
				keys[i] += "value-key-suffix"
			}
			b.Run("extension", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(unsafe.Sizeof(metadata[string, string]{})), "bytes/meta")
				for i := 0; i < b.N; i++ {
					benchmarkLeaf = table.jump(keys[i&(n-1)])
				}
			})
		})
	}
}
