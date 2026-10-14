package wormhole

import (
	"encoding/binary"
	"math/bits"
	"reflect"
)

// splitDenseNumeric selects the existing separator directly from sorted slots.
// It needs no K/V scratch array, regardless of V's size or pointer content.
func (l *leaf[K, V]) splitDenseNumeric(key K, value V, tag uint16, kind reflect.Kind) *leaf[K, V] {
	l.syncSorted()
	is := l.seek(key)
	head := func(i int) uint64 {
		if i == is {
			return numericOrderBits(key, kind)
		}
		if i > is {
			i--
		}
		return numericOrderBits(l.records[l.ss[i]].key, kind)
	}
	lo, hi := 0, leafCapacity
	alen := bits.LeadingZeros64(head(lo)^head(hi))/8 + 1
	target := leafCapacity / 2
	if l.next == nil {
		target = leafCapacity * 3 / 4
	}
	check := func(a, b int) bool { shift := uint(64 - alen*8); return byte(head(a)>>shift) != byte(head(b)>>shift) }
	for lo+1 < hi {
		mid := (lo + hi + 1) / 2
		if mid <= target {
			if check(mid, hi) {
				lo = mid
			} else {
				hi = mid
			}
		} else {
			if check(lo, mid) {
				hi = mid
			} else {
				lo = mid
			}
		}
	}
	cut := hi
	alen = bits.LeadingZeros64(head(cut-1)^head(cut))/8 + 1
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], head(cut))
	right := &leaf[K, V]{anchor: string(encoded[:alen]), prev: l, next: l.next, dense: true, denseStorage: new(denseStorage[K, V])}
	l.splitDenseRetainLeft(right, is, cut, key, value, tag)
	return right
}

// Keep surviving numeric records at their stable slots; string splits retain
// their compacting path. All mutations require the owning leaf write lock.
func (l *leaf[K, V]) splitDenseRetainLeft(right *leaf[K, V], is, cut int, key K, value V, tag uint16) {
	// Preserve stable slots on the left. Only records crossing the split are
	// copied out and cleared; the caller already selected the separator.
	// Incoming-right splits leave the complete left sorted prefix unchanged.
	// Preserve a copy only when inserting on the left will mutate that prefix.
	var oldSlots [leafCapacity]uint8
	sourceSlots := &l.ss
	if is < cut {
		oldSlots = l.ss
		sourceSlots = &oldSlots
	}
	rightSlot := 0
	for i := cut; i <= leafCapacity; i++ {
		if i == is {
			right.records[rightSlot] = record[K, V]{key: key, value: value}
			right.tags[rightSlot] = tag
		} else {
			rank := i
			if i > is {
				rank--
			}
			slot := (*sourceSlots)[rank]
			right.records[rightSlot] = l.records[slot]
			right.tags[rightSlot] = l.tags[slot]
			l.records[slot] = record[K, V]{}
			l.tags[slot] = 0
			l.occupied[slot>>6] &^= uint64(1) << (slot & 63)
		}
		right.ss[rightSlot] = uint8(rightSlot)
		right.occupied[rightSlot>>6] |= uint64(1) << (rightSlot & 63)
		rightSlot++
	}
	right.n = rightSlot
	var inserted uint8
	if is < cut {
		l.n = cut - 1
		l.insertDenseEntry(key, value, tag)
		inserted = l.ss[cut-1]
	}
	if is < cut {
		for i := 0; i < cut; i++ {
			if i == is {
				l.ss[i] = inserted
			} else {
				rank := i
				if i > is {
					rank--
				}
				l.ss[i] = oldSlots[rank]
			}
		}
	}
	l.n = cut
	if !useLeafSIMD {
		clear(l.buckets[:])
		clear(l.chain[:])
		for _, slot := range l.ss[:l.n] {
			bucket := byte(l.tags[slot])
			l.chain[slot] = l.buckets[bucket]
			l.buckets[bucket] = slot + 1
		}
		for _, slot := range right.ss[:right.n] {
			bucket := byte(right.tags[slot])
			right.chain[slot] = right.buckets[bucket]
			right.buckets[bucket] = slot + 1
		}
	}
	l.sorted, right.sorted = l.n, right.n
}
