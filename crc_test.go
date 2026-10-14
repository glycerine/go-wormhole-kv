package wormhole

import (
	"encoding/binary"
	"math/rand/v2"
	"strconv"
	"testing"
)

func TestCRCUnalignedExtensions(t *testing.T) {
	t.Run("given binary strings at every short length and alignment when extending CRC then match the portable checksum", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(42, 966))
		data := make([]byte, 4104)
		for i := range data {
			data[i] = byte(rng.Uint32())
		}
		text := string(data)
		for offset := range 8 {
			for n := 0; n <= 4096; n++ {
				key := text[offset : offset+n]
				seed := rng.Uint32()
				if got, want := crcExtend(seed, key), crcGeneric(seed, key); got != want {
					t.Fatalf("offset=%d len=%d: %08x != %08x", offset, n, got, want)
				}
				split := n / 2
				if crcExtend(crcExtend(seed, key[:split]), key[split:]) != crcGeneric(seed, key) {
					t.Fatalf("incremental mismatch len=%d", n)
				}
			}
		}
	})
}

func BenchmarkCRCShortLengths(b *testing.B) {
	text := "abcdefghijklmnopqrstuvwxyz0123456789"
	for _, n := range []int{1, 2, 3, 4, 5, 8, 12, 16, 32} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			key := text[:n]
			var h uint32 = crcSeed
			for range b.N {
				h = crcExtend(h, key)
			}
			if h == 0 {
				b.Log(h)
			}
		})
	}
}

func TestNumericKeyTagMatchesRoute(t *testing.T) {
	rng := rand.New(rand.NewPCG(19, 73))
	for range 100000 {
		var route [8]byte
		binary.BigEndian.PutUint64(route[:], rng.Uint64())
		if got, want := numericKeyTag(&route), keyTag(string(route[:])); got != want {
			t.Fatalf("tag %04x != %04x for %x", got, want, route)
		}
	}
}

func BenchmarkCRCShort(b *testing.B) {
	key := "abcdefgh"
	b.Run("portable", func(b *testing.B) {
		var h uint32 = crcSeed
		for range b.N {
			h = crcGeneric(h, key)
		}
		if h == 0 {
			b.Log(h)
		}
	})
	b.Run("dispatched", func(b *testing.B) {
		var h uint32 = crcSeed
		for range b.N {
			h = crcExtend(h, key)
		}
		if h == 0 {
			b.Log(h)
		}
	})
	b.Run("fixed8", func(b *testing.B) {
		var route [8]byte
		binary.BigEndian.PutUint64(route[:], 0x6162636465666768)
		var tag uint16
		for range b.N {
			tag = numericKeyTag(&route)
		}
		if tag == 0 {
			b.Log(tag)
		}
	})
}
