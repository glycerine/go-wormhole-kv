package wormhole

import (
	"math/rand/v2"
	"testing"
)

func TestMetadataTagMasks(t *testing.T) {
	t.Run("given duplicate and binary tags in every lane when comparing then report exactly the matching byte pairs", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(46, 905))
		for step := 0; step < 20000; step++ {
			var tags metaTags
			for i := range tags {
				tags[i] = uint16(rng.Uint32())
			}
			tag := uint16(rng.Uint32())
			if step%2 == 0 {
				tags[step%8] = tag
			}
			if step%3 == 0 {
				for i := range tags {
					tags[i] = tag
				}
			}
			if step%5 == 0 {
				tag = 0
			}
			var want uint32
			for i := range tags {
				if tags[i] == tag {
					want |= 3 << (i * 2)
				}
			}
			if got := matchTagMask(&tags, tag); got != want {
				t.Fatalf("step %d: %x != %x tags=%v tag=%x", step, got, want, tags, tag)
			}
		}
	})
}

var benchmarkTagMask uint32

func BenchmarkMetadataTagMask(b *testing.B) {
	tags := metaTags{1, 2, 3, 4, 5, 6, 7, 8}
	b.Run("scalar", func(b *testing.B) {
		for i := range b.N {
			benchmarkTagMask = scalarTagMask(&tags, uint16(i&15))
		}
	})
	b.Run("dispatched", func(b *testing.B) {
		for i := range b.N {
			benchmarkTagMask = matchTagMask(&tags, uint16(i&15))
		}
	})
}
