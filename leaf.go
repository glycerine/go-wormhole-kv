// Ported from wh.c, Copyright (c) 2016--2021 Wu, Xingbo <wuxb45@gmail.com>.
// See LICENSE in the repository root. No warranty, explicit or implicit.

package wormhole

import (
	"cmp"
	"encoding/binary"
	"hash/crc32"
	"math/bits"
	"reflect"
	"sync"
	"unsafe"
)

const (
	leafCapacity = 128 // WH_KPN in wh.c
	hashDiv      = 1 << 16 / leafCapacity
	crcSeed      = uint32(0xdeadbeef)
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// crcGeneric uses the native implementation's raw CRC32C convention, rather
// than the complemented convention used by hash/crc32.
func crcGeneric(seed uint32, key string) uint32 {
	// Update reads but does not retain or modify its input. A temporary read-only
	// view preserves the immutable string's lifetime and avoids allocating a
	// byte copy for every prefix probe. Never expose this slice to writers.
	bytes := unsafe.Slice(unsafe.StringData(key), len(key))
	return ^crc32.Update(^seed, crcTable, bytes)
}

func keyTag(key string) uint16 {
	return hashTag(crcExtend(crcSeed, key))
}

func hashTag(h uint32) uint16 {
	t := uint16(h) ^ uint16(h>>16)
	if t == 0 {
		return 1 // zero denotes a vacant slot
	}
	return t
}

// Keys are immutable after publication. Values may be replaced under the
// owning leaf write lock; readers copy values under its read lock.
type record[K cmp.Ordered, V any] struct {
	key   K
	value V
}

type leafEntry[K cmp.Ordered, V any] struct {
	tag uint16
	kv  *record[K, V]
}

// leaf uses stable record slots, short tag-bucket chains and a lazy key-order
// array (ss). Callers must synchronize access. Tags track slot occupancy;
// ss indexes records in key order. The legacy magnet layout remains available
// internally for native leaf differential tests. Keep pointers before the
// trailing scalar arrays so GC can skip those arrays.
type leaf[K cmp.Ordered, V any] struct {
	mu         leafRWMutex
	sortMu     sync.Mutex // protects lazy key sorting among read-locked iterators
	version    uint64     // protected by mu; checked after a metadata lookup
	anchor     string
	prev, next *leaf[K, V]
	hs         *[leafCapacity]*record[K, V]
	*denseStorage[K, V]
	tags      [leafCapacity]uint16
	ss        [leafCapacity]uint8
	n, sorted int
	occupied  [2]uint64 // tag occupancy, protected by mu
	dense     bool
}

// syncSortedRead requires mu.RLock. Every iterator takes sortMu, including
// when the leaf is already sorted, so the initial sort's writes happen before
// subsequent readers inspect ss and sorted. Point readers inspect tags, bucket chains and records.
func (l *leaf[K, V]) syncSortedRead() {
	l.sortMu.Lock()
	l.syncSorted()
	l.sortMu.Unlock()
}

func (l *leaf[K, V]) recordAtSlot(slot int) *record[K, V] {
	if l.dense {
		return &l.records[slot]
	}
	if l.hs == nil {
		return nil
	}
	return l.hs[slot]
}
func (l *leaf[K, V]) at(i int) *record[K, V] { return l.recordAtSlot(int(l.ss[i])) }

func (l *leaf[K, V]) entry(i int) leafEntry[K, V] {
	return leafEntry[K, V]{tag: l.tags[i], kv: l.recordAtSlot(i)}
}

// match filters by hash tag, then compares full keys. Dense amd64 leaves
// scan contiguous tags; other layouts use their bucket or magnet index.
func (l *leaf[K, V]) match(key K, tag uint16) int {
	if l.dense && useLeafSIMD {
		lo, hi := matchLeafTags(&l.tags, tag)
		for lo != 0 {
			i := bits.TrailingZeros64(lo)
			if orderedCompare(l.records[i].key, key) == 0 {
				return i
			}
			lo &= lo - 1
		}
		for hi != 0 {
			i := 64 + bits.TrailingZeros64(hi)
			if orderedCompare(l.records[i].key, key) == 0 {
				return i
			}
			hi &= hi - 1
		}
		return leafCapacity
	}
	if l.dense && !useLeafSIMD {
		for link := l.buckets[byte(tag)]; link != 0; link = l.chain[link-1] {
			i := int(link) - 1
			if l.tags[i] == tag && orderedCompare(l.records[i].key, key) == 0 {
				return i
			}
		}
		return leafCapacity
	}
	i0 := int(tag) / hashDiv
	t := l.tags[i0]
	if t == tag && orderedCompare(l.hs[i0].key, key) == 0 {
		return i0
	}
	if t == 0 {
		return leafCapacity
	}
	for i := i0 - 1; i >= 0; i-- {
		t = l.tags[i]
		if t == tag {
			if orderedCompare(l.hs[i].key, key) == 0 {
				return i
			}
		} else if t < tag {
			break
		}
	}
	for i := i0 + 1; i < leafCapacity; i++ {
		t = l.tags[i]
		if t == tag {
			if orderedCompare(l.hs[i].key, key) == 0 {
				return i
			}
		} else if t == 0 || t > tag {
			break
		}
	}
	return leafCapacity
}

func (l *leaf[K, V]) shift(from, count, delta int) {
	copy(l.hs[from+delta:from+delta+count], l.hs[from:from+count])
	copy(l.tags[from+delta:from+delta+count], l.tags[from:from+count])
	shiftIndices(&l.ss, l.n, from, count, delta)
}

func scalarShiftIndices(ss *[leafCapacity]uint8, n, from, count, delta int) {
	for i := 0; i < n; i++ {
		s := int(ss[i])
		if s >= from && s < from+count {
			ss[i] = uint8(s + delta)
		}
	}
}

// insertEntry requires a vacant slot and a key not already in the leaf.
func (l *leaf[K, V]) insertEntry(e leafEntry[K, V]) {
	if l.dense {
		l.insertDenseEntry(e.kv.key, e.kv.value, e.tag)
		return
	}

	if l.hs == nil {
		l.hs = new([leafCapacity]*record[K, V])
	}
	i0 := int(e.tag) / hashDiv
	pos, hole := i0, i0
	if l.tags[i0] != 0 {
		i := i0
		for i > 0 && l.tags[i-1] != 0 && l.tags[i-1] >= e.tag {
			i--
		}
		for i < leafCapacity && l.tags[i] != 0 && l.tags[i] < e.tag {
			i++
		}
		il := i - 1
		el := min(il, i0-1)
		if el >= 0 {
			half := el >> 6
			free := ^l.occupied[half] << (63 - (el & 63))
			if free != 0 {
				el -= bits.LeadingZeros64(free)
			} else if half == 1 {
				el = 63 - bits.LeadingZeros64(^l.occupied[0])
			} else {
				el = -1
			}
		}
		ir := il + 1
		for ir < leafCapacity && l.tags[ir] == e.tag {
			ir++
		}
		er := max(ir, i0+1)
		if er < leafCapacity {
			half := er >> 6
			free := ^l.occupied[half] >> (er & 63)
			if free != 0 {
				er += bits.TrailingZeros64(free)
			} else if half == 0 {
				er = 64 + bits.TrailingZeros64(^l.occupied[1])
			} else {
				er = leafCapacity
			}
		}
		dl, dr := leafCapacity, leafCapacity
		if el >= 0 {
			dl = il - el
		}
		if er < leafCapacity {
			dr = er - ir
		}
		if dl <= dr {
			if dl > 0 {
				l.shift(el+1, dl, -1)
			}
			pos, hole = il, el
		} else {
			if dr > 0 {
				l.shift(ir, dr, 1)
			}
			pos, hole = ir, er
		}
	}
	l.tags[pos], l.hs[pos] = e.tag, e.kv
	l.occupied[hole>>6] |= uint64(1) << (hole & 63)
	l.ss[l.n] = uint8(pos)
	l.n++
}

// insertDenseEntry gives leaves stable record addresses and avoids
// both magnet shifts and an individual heap allocation per inserted record.
func (l *leaf[K, V]) insertDenseEntry(key K, value V, tag uint16) {
	if l.denseStorage == nil {
		l.allocateDenseStorage()
	}
	half := 0
	if l.occupied[0] == ^uint64(0) {
		half = 1
	}
	pos := half*64 + bits.TrailingZeros64(^l.occupied[half])
	if !useLeafSIMD {
		bucket := byte(tag)
		l.chain[pos] = l.buckets[bucket]
		l.buckets[bucket] = uint8(pos + 1)
	}
	l.records[pos] = record[K, V]{key: key, value: value}
	l.tags[pos] = tag
	l.occupied[half] |= uint64(1) << (pos & 63)
	l.ss[l.n] = uint8(pos)
	l.n++
}

// buildSortedPacked constructs an empty leaf from entries in key order.
// Simulate the identical magnet insertions using tag/rank words: shifts move
// only integers, without pointer barriers or sorted-index repairs. Materialize
// GC-visible pointers and the key-order index once after placement is final.
func (l *leaf[K, V]) buildSortedPacked(entries []leafEntry[K, V]) {
	if l.dense {
		for _, e := range entries {
			l.insertEntry(e)
		}
		l.sorted = l.n
		return
	}

	if len(entries) > 0 && l.hs == nil {
		l.hs = new([leafCapacity]*record[K, V])
	}
	var slots [leafCapacity]uint32
	var occupied [2]uint64
	for rank, e := range entries {
		i0 := int(e.tag) / hashDiv
		pos, hole := i0, i0
		if uint16(slots[i0]) != 0 {
			i := i0
			for i > 0 && uint16(slots[i-1]) != 0 && uint16(slots[i-1]) >= e.tag {
				i--
			}
			for i < leafCapacity && uint16(slots[i]) != 0 && uint16(slots[i]) < e.tag {
				i++
			}
			il := i - 1
			el := min(il, i0-1)
			if el >= 0 {
				half := el >> 6
				free := ^occupied[half] << (63 - (el & 63))
				if free != 0 {
					el -= bits.LeadingZeros64(free)
				} else if half == 1 {
					el = 63 - bits.LeadingZeros64(^occupied[0])
				} else {
					el = -1
				}
			}
			ir := il + 1
			for ir < leafCapacity && uint16(slots[ir]) == e.tag {
				ir++
			}
			er := max(ir, i0+1)
			if er < leafCapacity {
				half := er >> 6
				free := ^occupied[half] >> (er & 63)
				if free != 0 {
					er += bits.TrailingZeros64(free)
				} else if half == 0 {
					er = 64 + bits.TrailingZeros64(^occupied[1])
				} else {
					er = leafCapacity
				}
			}
			dl, dr := leafCapacity, leafCapacity
			if el >= 0 {
				dl = il - el
			}
			if er < leafCapacity {
				dr = er - ir
			}
			if dl <= dr {
				if dl == 1 {
					slots[el] = slots[el+1]
				} else if dl > 1 {
					copy(slots[el:el+dl], slots[el+1:el+1+dl])
				}
				pos, hole = il, el
			} else {
				if dr == 1 {
					slots[ir+1] = slots[ir]
				} else if dr > 1 {
					copy(slots[ir+1:ir+1+dr], slots[ir:ir+dr])
				}
				pos, hole = ir, er
			}
		}
		slots[pos] = uint32(rank+1)<<16 | uint32(e.tag)
		// Shifts leave every old slot occupied. Each insertion fills its
		// selected gap, which need not be the new entry's final position.
		occupied[hole>>6] |= uint64(1) << (hole & 63)
	}
	for half, word := range occupied {
		for word != 0 {
			slot := half*64 + bits.TrailingZeros64(word)
			word &= word - 1
			rank := int(slots[slot]>>16) - 1
			e := entries[rank]
			l.tags[slot], l.hs[slot] = e.tag, e.kv
			l.ss[rank] = uint8(slot)
		}
	}
	l.n, l.sorted = len(entries), len(entries)
	l.occupied = occupied
}

func (l *leaf[K, V]) insert(kv *record[K, V], tag uint16) {
	n := l.n
	l.insertEntry(leafEntry[K, V]{tag: tag, kv: kv})
	if n == l.sorted && (n == 0 || orderedCompare(l.at(n-1).key, kv.key) < 0) {
		l.sorted++
	}
}

func (l *leaf[K, V]) remove(slot int) record[K, V] {
	i := 0
	for int(l.ss[i]) != slot {
		i++
	}
	l.ss[i] = l.ss[l.n-1]
	l.sorted = min(l.sorted, i)
	l.n--
	l.ss[l.n] = 0
	old := *l.recordAtSlot(slot)
	oldTag := l.tags[slot]
	l.tags[slot] = 0
	if l.dense {
		l.records[slot] = record[K, V]{}
		if !useLeafSIMD {
			bucket := byte(oldTag)
			link := &l.buckets[bucket]
			for int(*link)-1 != slot {
				link = &l.chain[*link-1]
			}
			*link = l.chain[slot]
			l.chain[slot] = 0
		}
		l.occupied[slot>>6] &^= uint64(1) << (slot & 63)
		return old
	}
	l.hs[slot] = nil
	// Pull entries toward their ideal positions so match may still terminate
	// at a gap. This is wormleaf_pull_ih, including its leftward preference.
	i = slot - 1
	for i >= 0 && l.tags[i] != 0 && int(l.tags[i])/hashDiv > i {
		i--
	}
	i++
	if i < slot {
		l.shift(i, slot-i, 1)
		l.tags[i], l.hs[i] = 0, nil
		l.occupied[i>>6] &^= uint64(1) << (i & 63)
		return old
	}
	i = slot + 1
	for i < leafCapacity && l.tags[i] != 0 && int(l.tags[i])/hashDiv < i {
		i++
	}
	i--
	if i > slot {
		l.shift(slot+1, i-slot, -1)
		l.tags[i], l.hs[i] = 0, nil
		l.occupied[i>>6] &^= uint64(1) << (i & 63)
		return old
	}
	l.occupied[slot>>6] &^= uint64(1) << (slot & 63)
	return old
}

func (l *leaf[K, V]) syncSorted() {
	if l.sorted == l.n {
		return
	}
	s := l.sorted
	if l.n-s > 1 {
		var keys [leafCapacity]K
		var heads [leafCapacity]uint64
		kind := reflect.TypeFor[K]().Kind()
		for _, slot := range l.ss[:l.n] {
			keys[slot] = l.recordAtSlot(int(slot)).key
			if kind != reflect.String {
				heads[slot] = numericOrderBits(keys[slot], kind)
			} else {
				heads[slot] = keyHead(*(*string)(unsafe.Pointer(&keys[slot])))
			}
		}
		sortCached(l.ss[s:l.n], &keys, &heads)
		if kind == reflect.String {
			l.mergeSortedCached(s, &keys, &heads)
		} else {
			l.mergeSortedHeads(s, &heads)
		}
	} else {
		l.mergeSorted(s)
	}
	l.sorted = l.n
}

// mergeSortedHeads merges numeric runs using their complete encoded order.
// Equal numeric heads identify the same map key, so a live fresh-insert run
// needs no fallback K comparison.
func (l *leaf[K, V]) mergeSortedHeads(s int, heads *[leafCapacity]uint64) {
	tail := l.n - s
	if s == 0 || tail == 0 {
		return
	}
	var tmp [leafCapacity / 2]uint8
	if s <= tail {
		copy(tmp[:s], l.ss[:s])
		a, b, out := 0, s, 0
		for a < s && b < l.n {
			if heads[tmp[a]] < heads[l.ss[b]] {
				l.ss[out] = tmp[a]
				a++
			} else {
				l.ss[out] = l.ss[b]
				b++
			}
			out++
		}
		copy(l.ss[out:b], tmp[a:s])
		return
	}
	copy(tmp[:tail], l.ss[s:l.n])
	a, b, out := s-1, tail-1, l.n-1
	for a >= 0 && b >= 0 {
		if heads[l.ss[a]] < heads[tmp[b]] {
			l.ss[out] = tmp[b]
			b--
		} else {
			l.ss[out] = l.ss[a]
			a--
		}
		out--
	}
	copy(l.ss[a+1:out+1], tmp[:b+1])
}

// syncSortedOccupied prepares a deep string leaf immediately before a split.
// Keeping this separate leaves the ordinary synchronization path unchanged.
func (l *leaf[K, V]) syncSortedOccupied() {
	if l.sorted == l.n {
		return
	}
	s := l.sorted
	if l.n-s > 1 {
		var keys [leafCapacity]K
		var heads [leafCapacity]uint64
		for _, slot := range l.ss[:l.n] {
			keys[slot] = l.recordAtSlot(int(slot)).key
			heads[slot] = keyHead(*(*string)(unsafe.Pointer(&keys[slot])))
		}
		sortCachedOccupied(l.ss[s:l.n], &keys, &heads)
		l.mergeSortedCached(s, &keys, &heads)
	} else {
		l.mergeSorted(s)
	}
	l.sorted = l.n
}

func cachedKeyLess[K cmp.Ordered](a, b uint8, keys *[leafCapacity]K, heads *[leafCapacity]uint64) bool {
	if heads[a] != heads[b] {
		return heads[a] < heads[b]
	}
	return orderedCompare(keys[a], keys[b]) < 0
}

// Keep the same smaller-run merge with cached comparison inputs. The direct
// version below avoids gathering all key headers for a one-key dirty tail.
func (l *leaf[K, V]) mergeSortedCached(s int, keys *[leafCapacity]K, heads *[leafCapacity]uint64) {
	tail := l.n - s
	if s == 0 || tail == 0 {
		return
	}
	var tmp [leafCapacity / 2]uint8
	if s <= tail {
		copy(tmp[:s], l.ss[:s])
		a, b, out := 0, s, 0
		for a < s && b < l.n {
			if cachedKeyLess(tmp[a], l.ss[b], keys, heads) {
				l.ss[out] = tmp[a]
				a++
			} else {
				l.ss[out] = l.ss[b]
				b++
			}
			out++
		}
		copy(l.ss[out:b], tmp[a:s])
	} else {
		copy(tmp[:tail], l.ss[s:l.n])
		a, b, out := s-1, tail-1, l.n-1
		for a >= 0 && b >= 0 {
			if cachedKeyLess(l.ss[a], tmp[b], keys, heads) {
				l.ss[out] = tmp[b]
				b--
			} else {
				l.ss[out] = l.ss[a]
				a--
			}
			out--
		}
		copy(l.ss[a+1:out+1], tmp[:b+1])
	}
}

// A zero-padded big-endian head preserves bytewise ordering whenever heads
// differ. Equal heads still require the full string comparison: short keys
// with trailing zero bytes and long keys sharing eight bytes can collide.
func keyHead(key string) uint64 {
	if len(key) >= 8 {
		return binary.BigEndian.Uint64([]byte(key[:8]))
	}
	var head [8]byte
	copy(head[:], key)
	return binary.BigEndian.Uint64(head[:])
}

// mergeSorted ports wormleaf_sort_m2: copy only the smaller sorted run and
// merge toward it, leaving the untouched remainder of the other run in place.
func (l *leaf[K, V]) mergeSorted(s int) {
	tail := l.n - s
	if s == 0 || tail == 0 {
		return
	}
	var tmp [leafCapacity / 2]uint8
	if s <= tail {
		copy(tmp[:s], l.ss[:s])
		a, b, out := 0, s, 0
		for a < s && b < l.n {
			if orderedCompare(l.recordAtSlot(int(tmp[a])).key, l.recordAtSlot(int(l.ss[b])).key) < 0 {
				l.ss[out] = tmp[a]
				a++
			} else {
				l.ss[out] = l.ss[b]
				b++
			}
			out++
		}
		copy(l.ss[out:b], tmp[a:s])
	} else {
		copy(tmp[:tail], l.ss[s:l.n])
		a, b, out := s-1, tail-1, l.n-1
		for a >= 0 && b >= 0 {
			if orderedCompare(l.recordAtSlot(int(l.ss[a])).key, l.recordAtSlot(int(tmp[b])).key) < 0 {
				l.ss[out] = tmp[b]
				b--
			} else {
				l.ss[out] = l.ss[a]
				a--
			}
			out--
		}
		copy(l.ss[a+1:out+1], tmp[:b+1])
	}
}

// seek requires the key-order array to be fully sorted.
func (l *leaf[K, V]) seek(key K) int {
	lo, hi := 0, l.n
	for lo < hi {
		m := (lo + hi) / 2
		if orderedCompare(l.at(m).key, key) < 0 {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

// deleteSortedRange removes [start,end) from a fully sorted leaf. Remove in
// reverse order because remove replaces ss[i] with its last active index.
func (l *leaf[K, V]) deleteSortedRange(start, end int) {
	for i := end - 1; i >= start; i-- {
		l.remove(int(l.ss[i]))
	}
}

func commonPrefix(a, b string) int {
	i := 0
	for i < min(len(a), len(b)) && a[i] == b[i] {
		i++
	}
	return i
}

// split adds kv to a full leaf and returns an unlinked right leaf. The caller
// publishes links and metadata while holding the structural locks. As in
// wormhole_split_cut_search1, minimize anchor length first, then favor the
// midpoint (or 3/4 for the last leaf to accommodate sequential inserts).
func (l *leaf[K, V]) split(kv *record[K, V], tag uint16) *leaf[K, V] {
	if l.dense {
		if kind := reflect.TypeFor[K]().Kind(); kind != reflect.String {
			return l.splitDenseNumeric(kv.key, kv.value, tag, kind)
		}
		l.prefetchStringSplit()
		return l.splitDense(kv.key, kv.value, tag)
	}
	l.syncSorted()
	var entries [leafCapacity + 1]leafEntry[K, V]
	is := l.seek(kv.key)
	for i := 0; i < is; i++ {
		entries[i] = l.entry(int(l.ss[i]))
	}
	entries[is] = leafEntry[K, V]{tag: tag, kv: kv}
	for i := is; i < l.n; i++ {
		entries[i+1] = l.entry(int(l.ss[i]))
	}
	// Numeric keys use splitDense above. String keys already own their route
	// bytes; inspect only the boundary keys needed by separator selection.
	text := func(i int) string { return *(*string)(unsafe.Pointer(&entries[i].kv.key)) }
	lo, hi := 0, l.n
	alen := commonPrefix(text(lo), text(hi)) + 1
	if alen > 65535 {
		return nil
	} // native metadata stores 16-bit lengths
	target := leafCapacity / 2
	if l.next == nil {
		target = leafCapacity * 3 / 4
	}
	check := func(a, b int) bool {
		ka, kb := text(a), text(b)
		return len(ka) < alen || ka[alen-1] != kb[alen-1]
	}
	for lo+1 < hi {
		m := (lo + hi + 1) / 2
		if m <= target {
			if check(m, hi) {
				lo = m
			} else {
				hi = m
			}
		} else {
			if check(lo, m) {
				hi = m
			} else {
				lo = m
			}
		}
	}
	cut := hi
	alen = commonPrefix(text(cut-1), text(cut)) + 1
	right := &leaf[K, V]{anchor: text(cut)[:alen], prev: l, next: l.next, dense: l.dense}
	l.tags = [leafCapacity]uint16{}
	*l.hs = [leafCapacity]*record[K, V]{}
	l.ss = [leafCapacity]uint8{}
	l.n, l.sorted = 0, 0
	l.occupied = [2]uint64{}
	l.buildSortedPacked(entries[:cut])
	right.buildSortedPacked(entries[cut:])
	return right
}

// splitDense rebuilds compact string records after choosing the separator.
func (l *leaf[K, V]) splitDense(key K, value V, tag uint16) *leaf[K, V] {
	l.syncSorted()
	var values [leafCapacity + 1]record[K, V]
	var tags [leafCapacity + 1]uint16
	is := l.seek(key)
	for i := 0; i < is; i++ {
		slot := l.ss[i]
		values[i] = l.records[slot]
		tags[i] = l.tags[slot]
	}
	values[is] = record[K, V]{key: key, value: value}
	tags[is] = tag
	for i := is; i < l.n; i++ {
		slot := l.ss[i]
		values[i+1] = l.records[slot]
		tags[i+1] = l.tags[slot]
	}
	var cut int
	var anchor string
	text := func(i int) string { return *(*string)(unsafe.Pointer(&values[i].key)) }
	lo, hi := 0, leafCapacity
	alen := commonPrefix(text(lo), text(hi)) + 1
	if alen > 65535 {
		return nil
	}
	target := leafCapacity / 2
	if l.next == nil {
		target = leafCapacity * 3 / 4
	}
	check := func(a, b int) bool {
		ka, kb := text(a), text(b)
		return len(ka) < alen || ka[alen-1] != kb[alen-1]
	}
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
	cut = hi
	alen = commonPrefix(text(cut-1), text(cut)) + 1
	anchor = text(cut)[:alen]

	right := &leaf[K, V]{anchor: anchor, prev: l, next: l.next, dense: true, denseStorage: new(denseStorage[K, V])}
	l.buildDenseCompact(values[:cut], tags[:cut])
	right.buildDenseCompactNew(values[cut:], tags[cut:])
	return right
}

// buildDenseCompact rebuilds an allocated dense leaf from sorted scratch.
// Copies finish before clearing inactive slots so live pointer values remain
// reachable. The caller owns the leaf write lock (or an unpublished leaf).
func (l *leaf[K, V]) buildDenseCompact(values []record[K, V], tags []uint16) {
	n := len(values)
	copy(l.records[:], values)
	clear(l.records[n:])
	copy(l.tags[:], tags)
	clear(l.tags[n:])
	for i := 0; i < n; i++ {
		l.ss[i] = uint8(i)
	}
	clear(l.ss[n:])
	l.n, l.sorted = n, n
	l.occupied[0] = ^uint64(0) >> (64 - min(n, 64))
	l.occupied[1] = ^uint64(0) >> (64 - max(n-64, 0))
	if !useLeafSIMD {
		clear(l.buckets[:])
		clear(l.chain[:])
		for i, tag := range tags {
			bucket := byte(tag)
			l.chain[i] = l.buckets[bucket]
			l.buckets[bucket] = uint8(i + 1)
		}
	}
}

// buildDenseCompactNew initializes a freshly allocated unpublished leaf. Its
// inactive records, tags, indices and portable bucket arrays are already zero.
func (l *leaf[K, V]) buildDenseCompactNew(values []record[K, V], tags []uint16) {
	n := len(values)
	copy(l.records[:], values)
	copy(l.tags[:], tags)
	for i := 0; i < n; i++ {
		l.ss[i] = uint8(i)
	}
	l.n, l.sorted = n, n
	l.occupied[0] = ^uint64(0) >> (64 - min(n, 64))
	l.occupied[1] = ^uint64(0) >> (64 - max(n-64, 0))
	if !useLeafSIMD {
		for i, tag := range tags {
			bucket := byte(tag)
			l.chain[i] = l.buckets[bucket]
			l.buckets[bucket] = uint8(i + 1)
		}
	}
}

// mergeRight moves an adjacent right leaf into l; callers remove its anchor
// and links separately. Rightward merges preserve the iterator lock order.
func (l *leaf[K, V]) mergeRight(right *leaf[K, V]) {
	wasSorted := l.sorted == l.n
	for i := 0; i < right.n; i++ {
		l.insertEntry(right.entry(int(right.ss[i])))
	}
	if wasSorted {
		l.sorted += right.sorted
	}
}

//go:noinline
func (l *leaf[K, V]) allocateDenseStorage() {
	l.denseStorage = new(denseStorage[K, V])
}
