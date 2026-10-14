package wormhole

import (
	"cmp"
	"math/bits"
	"slices"
)

// sortCached partitions a large dirty run by its first differing eight head bits.
// Heads preserve key order across buckets; full comparisons resolve each
// bucket, including zero-padded and long common-prefix keys. Scratch is bounded
// by leaf capacity and no record pointers move until the existing split path.
func sortCached[K cmp.Ordered](slots []uint8, keys *[leafCapacity]K, heads *[leafCapacity]uint64) {
	compare := func(a, b uint8) int {
		if heads[a] < heads[b] {
			return -1
		}
		if heads[a] > heads[b] {
			return 1
		}
		return orderedCompare(keys[a], keys[b])
	}
	if len(slots) < 32 {
		slices.SortFunc(slots, compare)
		return
	}
	first := heads[slots[0]]
	var varying uint64
	for _, slot := range slots {
		varying |= heads[slot] ^ first
	}
	if varying == 0 {
		slices.SortFunc(slots, compare)
		return
	}
	shift := uint(max(0, bits.Len64(varying)-8))
	var counts, next [256]uint8
	for _, slot := range slots {
		counts[byte(heads[slot]>>shift)]++
	}
	sum := 0
	for i, count := range counts {
		next[i] = uint8(sum)
		sum += int(count)
	}
	var tmp [leafCapacity]uint8
	for _, slot := range slots {
		bucket := byte(heads[slot] >> shift)
		tmp[next[bucket]] = slot
		next[bucket]++
	}
	copy(slots, tmp[:len(slots)])
	offset := 0
	for _, count := range counts {
		n := int(count)
		if n > 1 {
			slices.SortFunc(slots[offset:offset+n], compare)
		}
		offset += n
	}
}

// sortCachedOccupied is the deep-string-leaf variant. Deep leaves in the
// word corpus use few, widely separated radix buckets, so visit only buckets
// that received a key. The ordinary sorter remains the default for shallow
// strings and every numeric key type.
func sortCachedOccupied[K cmp.Ordered](slots []uint8, keys *[leafCapacity]K, heads *[leafCapacity]uint64) {
	compare := func(a, b uint8) int {
		if heads[a] < heads[b] {
			return -1
		}
		if heads[a] > heads[b] {
			return 1
		}
		return orderedCompare(keys[a], keys[b])
	}
	if len(slots) < 32 {
		slices.SortFunc(slots, compare)
		return
	}
	first := heads[slots[0]]
	var varying uint64
	for _, slot := range slots {
		varying |= heads[slot] ^ first
	}
	if varying == 0 {
		slices.SortFunc(slots, compare)
		return
	}
	shift := uint(max(0, bits.Len64(varying)-8))
	var counts, next [256]uint8
	var used [4]uint64
	for _, slot := range slots {
		bucket := byte(heads[slot] >> shift)
		if counts[bucket] == 0 {
			used[bucket>>6] |= uint64(1) << (bucket & 63)
		}
		counts[bucket]++
	}
	sum := 0
	for word, mask := range used {
		for mask != 0 {
			bucket := word*64 + bits.TrailingZeros64(mask)
			next[bucket] = uint8(sum)
			sum += int(counts[bucket])
			mask &= mask - 1
		}
	}
	var tmp [leafCapacity]uint8
	for _, slot := range slots {
		bucket := byte(heads[slot] >> shift)
		tmp[next[bucket]] = slot
		next[bucket]++
	}
	copy(slots, tmp[:len(slots)])
	offset := 0
	for word, mask := range used {
		for mask != 0 {
			bucket := word*64 + bits.TrailingZeros64(mask)
			n := int(counts[bucket])
			if n > 1 {
				slices.SortFunc(slots[offset:offset+n], compare)
			}
			offset += n
			mask &= mask - 1
		}
	}
}
