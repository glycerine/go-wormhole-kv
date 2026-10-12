package wormhole

import (
	"fmt"
	"testing"
)

func BenchmarkMapLen(b *testing.B) {
	for _, n := range []int{0, 1, 4, 64} {
		b.Run(fmt.Sprintf("Refs%d", n), func(b *testing.B) {
			m := New[string, string]()
			b.ReportAllocs()
			for range n {
				r := m.Ref()
				defer r.Close()
			}
			b.ResetTimer()
			for range b.N {
				if m.Len() != 0 {
					b.Fatal("empty map has keys")
				}
			}
		})
	}
}
