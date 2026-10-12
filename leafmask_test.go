package wormhole

import (
	"math/rand/v2"
	"testing"
	"unsafe"
)

func TestLeafTagMasks(t *testing.T) {
	// The leaf's tag array has no SIMD alignment guarantee. Exercise an
	// explicitly non-16-byte-aligned array, including both 64-slot halves.
	var storage [leafCapacity + 8]uint16
	offset := 0
	if uintptr(unsafe.Pointer(&storage[0]))%16 == 0 {
		offset = 1
	}
	tags := (*[leafCapacity]uint16)(unsafe.Pointer(&storage[offset]))
	check := func(tag uint16) {
		t.Helper()
		var wantLo, wantHi uint64
		for i, v := range tags {
			if v == tag {
				if i < 64 {
					wantLo |= uint64(1) << i
				} else {
					wantHi |= uint64(1) << (i - 64)
				}
			}
		}
		lo, hi := matchLeafTags(tags, tag)
		if lo != wantLo || hi != wantHi {
			t.Fatalf("tag %x: got %016x/%016x want %016x/%016x", tag, lo, hi, wantLo, wantHi)
		}
	}
	for _, tag := range []uint16{0, 1, 255, 256, 32768, 65535} {
		for i := range tags {
			tags[i] = tag ^ 65535
		}
		check(tag)
		for i := range tags {
			tags[i] = tag
			check(tag)
		}
	}
	rng := rand.New(rand.NewPCG(42, 91))
	for trial := 0; trial < 1000; trial++ {
		for i := range tags {
			tags[i] = uint16(rng.Uint32())
		}
		tag := uint16(rng.Uint32())
		tags[trial%leafCapacity] = tag
		tags[(trial*17)%leafCapacity] = tag
		check(tag)
	}
}
