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

func TestMetadataEitherTag(t *testing.T) {
	rng := rand.New(rand.NewPCG(151, 157))
	for step := 0; step < 20000; step++ {
		var a, b metaTags
		for i := range a {
			a[i], b[i] = uint16(rng.Uint32()), uint16(rng.Uint32())
		}
		tag := uint16(rng.Uint32())
		if step%5 == 0 {
			tag = 0
		}
		if step%2 == 0 {
			a[step%8] = tag
		}
		if step%3 == 0 {
			b[step%8] = tag
		}
		if step%7 == 0 {
			for i := range a {
				a[i], b[i] = tag, tag
			}
		}
		want := false
		for i := range a {
			want = want || a[i] == tag || b[i] == tag
		}
		if got := matchEitherTag(&a, &b, tag); got != want {
			t.Fatalf("step=%d tag=%x a=%v b=%v got=%v want=%v", step, tag, a, b, got, want)
		}
		if matchEitherTag(&a, &a, tag) != (scalarTagMask(&a, tag) != 0) {
			t.Fatal("aliased buckets")
		}
	}
	for lane := range 8 {
		var a metaTags
		a[lane] = 0xffff
		// A first-bucket match must not dereference the unused second bucket.
		if !matchEitherTag(&a, nil, 0xffff) {
			t.Fatalf("short circuit lane %d", lane)
		}
	}
}
