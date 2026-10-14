package wormhole

import (
	"cmp"
	"encoding/binary"
	"math/bits"
	"unsafe"
)

const noChild = 256

// childBitmap preserves native half-node storage: no bitmap allocation until
// there are two children, and release the bitmap when one child remains.
type childBitmap struct {
	min, max uint16
	words    *[4]uint64
}

func emptyChildren() childBitmap { return childBitmap{min: noChild, max: noChild} }

func (b *childBitmap) has(id byte) bool {
	if b.min == b.max {
		return b.min == uint16(id)
	}
	return b.words[id>>6]&(uint64(1)<<(id&63)) != 0
}

func (b *childBitmap) set(id byte) {
	if b.has(id) {
		return
	}
	if b.min == noChild {
		b.min, b.max = uint16(id), uint16(id)
		return
	}
	if b.words == nil {
		b.words = new([4]uint64)
		b.words[b.min>>6] = uint64(1) << (b.min & 63)
	}
	b.words[id>>6] |= uint64(1) << (id & 63)
	b.min, b.max = min(b.min, uint16(id)), max(b.max, uint16(id))
}

// less returns the greatest child strictly below id, or noChild.
func (b *childBitmap) less(id int) int {
	if id <= int(b.min) {
		return noChild
	}
	if id > int(b.max) {
		return int(b.max)
	}
	ix := id >> 6
	w := b.words[ix] & ((uint64(1) << (id & 63)) - 1)
	for w == 0 && ix > 0 {
		ix--
		w = b.words[ix]
	}
	if w == 0 {
		return noChild
	}
	return ix*64 + 63 - bits.LeadingZeros64(w)
}

// atOrAbove is inclusive, as is wormmeta_bm_gt despite its native name.
func (b *childBitmap) atOrAbove(id int) int {
	if id > int(b.max) || id == noChild {
		return noChild
	}
	if id <= int(b.min) {
		return int(b.min)
	}
	ix := id >> 6
	w := b.words[ix] & (^uint64(0) << (id & 63))
	for w == 0 && ix < 3 {
		ix++
		w = b.words[ix]
	}
	if w == 0 {
		return noChild
	}
	return ix*64 + bits.TrailingZeros64(w)
}

func (b *childBitmap) clear(id byte) {
	if !b.has(id) {
		return
	}
	if b.min == b.max {
		*b = emptyChildren()
		return
	}
	b.words[id>>6] &^= uint64(1) << (id & 63)
	if b.min == uint16(id) {
		b.min = uint16(b.atOrAbove(int(id) + 1))
	}
	if b.max == uint16(id) {
		b.max = uint16(b.less(int(id)))
	}
	if b.min == b.max {
		b.words = nil
	}
}

type metadata[K cmp.Ordered, V any] struct {
	key                string
	hash               uint32
	word               uint32 // leading key bytes, in prior 64-bit padding
	left, right, lpath *leaf[K, V]
	children           childBitmap
}

func newMetadata[K cmp.Ordered, V any](key string, hash uint32, l *leaf[K, V]) *metadata[K, V] {
	m := &metadata[K, V]{key: key, hash: hash, right: l, children: emptyChildren()}
	m.setLeft(l)
	return m
}

// metaWord packs at most four leading bytes without reading beyond the key.
// Length remains part of every comparison, distinguishing zero-padded keys.
func metaWord(key string) uint32 {
	switch len(key) {
	case 0:
		return 0
	case 1:
		return uint32(key[0])
	case 2:
		return uint32(binary.LittleEndian.Uint16([]byte(key)))
	case 3:
		return uint32(binary.LittleEndian.Uint16([]byte(key))) | uint32(key[2])<<16
	default:
		return binary.LittleEndian.Uint32([]byte(key))
	}
}

func (m *metadata[K, V]) setLeft(l *leaf[K, V]) {
	m.left = l
	if len(l.anchor) == len(m.key) {
		m.lpath = l
	} else {
		m.lpath = l.prev
	}
}

// The two arrays mirror native wmap/pmap. Tags can be probed without loading
// metadata pointers. Buckets are densely packed and use two hash choices.
type metaTags [8]uint16
type metaBucket[K cmp.Ordered, V any] [8]*metadata[K, V]
type metaTable[K cmp.Ordered, V any] struct {
	tags      []metaTags
	buckets   []metaBucket[K, V]
	mask      uint32
	maxPrefix int // a high-water mark, as in the native implementation
	n         int
	root      *metadata[K, V]             // aliases the existing empty-prefix bucket entry
	short     [256]*metadata[K, V]        // existing one-byte prefixes; same epoch protection
	second    *[256]*[256]*metadata[K, V] // sparse two-byte prefix pages for numeric keys
	routes    *numericRoutes
}

func newMetaTable[K cmp.Ordered, V any](bucketCount int) *metaTable[K, V] {
	if bucketCount < 1 || bucketCount&(bucketCount-1) != 0 {
		panic("wormhole: metadata bucket count must be a power of two")
	}
	return &metaTable[K, V]{tags: make([]metaTags, bucketCount), buckets: make([]metaBucket[K, V], bucketCount), mask: uint32(bucketCount - 1)}
}

func (t *metaTable[K, V]) choices(hash uint32) (uint32, uint32) {
	return hash & t.mask, bits.ReverseBytes32(hash) & t.mask
}

func (t *metaTable[K, V]) peek(hash uint32) bool {
	a, b := t.choices(hash)
	tag := hashTag(hash)
	if isStringKey[K]() {
		return matchEitherTag(&t.tags[a], &t.tags[b], tag)
	}
	return matchTagMask(&t.tags[a], tag) != 0 || matchTagMask(&t.tags[b], tag) != 0
}

func (t *metaTable[K, V]) get(key string, hash uint32) *metadata[K, V] {
	if len(key) == 0 {
		if m := t.root; m != nil && m.hash == hash {
			return m
		}
	} else if len(key) == 1 {
		if m := t.short[key[0]]; m != nil && m.hash == hash {
			return m
		}
	}
	if len(key) == 2 && t.second != nil {
		page := t.second[key[0]]
		if page != nil {
			if m := page[key[1]]; m != nil && m.hash == hash {
				return m
			}
		}
		return nil
	}
	a, b := t.choices(hash)
	prefetchMetaBuckets(unsafe.Pointer(&t.buckets[a]), unsafe.Pointer(&t.buckets[b]))
	tag := hashTag(hash)
	for _, i := range [2]uint32{a, b} {
		for mask := matchTagMask(&t.tags[i], tag); mask != 0; {
			j := bits.TrailingZeros32(mask) >> 1
			mask &^= 3 << (j * 2)
			m := t.buckets[i][j]
			if len(m.key) == len(key) {
				if len(key) <= 4 {
					if m.word == metaWord(key) {
						return m
					}
				} else if m.key == key {
					return m
				}
			}
		}
	}
	return nil
}

func crcByte(h uint32, c byte) uint32 { return crcTable[byte(h)^c] ^ h>>8 }

// child avoids allocating the concatenated prefix and child token.
func (t *metaTable[K, V]) child(key string, hash uint32, token byte) *metadata[K, V] {
	h := crcByte(hash, token)
	if len(key) == 0 {
		if m := t.short[token]; m != nil && m.hash == h {
			return m
		}
	}
	if len(key) == 1 && t.second != nil {
		page := t.second[key[0]]
		if page != nil {
			if m := page[token]; m != nil && m.hash == h {
				return m
			}
		}
		return nil
	}
	a, b := t.choices(h)
	prefetchMetaBuckets(unsafe.Pointer(&t.buckets[a]), unsafe.Pointer(&t.buckets[b]))
	tag := hashTag(h)
	for _, i := range [2]uint32{a, b} {
		for mask := matchTagMask(&t.tags[i], tag); mask != 0; {
			j := bits.TrailingZeros32(mask) >> 1
			mask &^= 3 << (j * 2)
			m := t.buckets[i][j]
			if len(m.key) == len(key)+1 {
				if len(key) < 4 {
					if m.word == metaWord(key)|uint32(token)<<(8*len(key)) {
						return m
					}
				} else if m.key[len(key)] == token && m.key[:len(key)] == key {
					return m
				}
			}
		}
	}
	return nil
}

func (t *metaTable[K, V]) bucketLen(i uint32) int {
	for j, v := range t.tags[i] {
		if v == 0 {
			return j
		}
	}
	return 8
}

type cuckooPath struct {
	bucket uint32
	prev   *cuckooPath
}

func (t *metaTable[K, V]) cuckoo(i uint32, m *metadata[K, V], tag uint16, depth int, path *cuckooPath) bool {
	// Returning displaced entries home does not consume native search depth.
	// Two full buckets can therefore form a cycle; do not revisit a bucket on
	// the current relocation path. Failed searches leave the table unchanged.
	for p := path; p != nil; p = p.prev {
		if p.bucket == i {
			return false
		}
	}
	n := t.bucketLen(i)
	if n < 8 {
		t.tags[i][n], t.buckets[i][n] = tag, m
		return true
	}
	if depth == 0 {
		return false
	}
	here := cuckooPath{bucket: i, prev: path}
	for j, victim := range t.buckets[i] {
		a, b := t.choices(victim.hash)
		other := a
		if a == i {
			other = b
		}
		if other == i {
			continue
		}
		d := depth
		if other != a {
			d--
		} // no depth penalty for returning home
		if t.cuckoo(other, victim, t.tags[i][j], d, &here) {
			t.tags[i][j], t.buckets[i][j] = tag, m
			return true
		}
	}
	return false
}

// put inserts a new prefix. Duplicate prefixes must be handled by the caller.
func (t *metaTable[K, V]) put(m *metadata[K, V]) {
	m.word = metaWord(m.key)
	for {
		a, b := t.choices(m.hash)
		tag := hashTag(m.hash)
		if t.cuckoo(a, m, tag, 1, nil) || t.cuckoo(b, m, tag, 1, nil) || t.cuckoo(a, m, tag, 2, nil) {
			t.n++
			if len(m.key) == 0 {
				t.root = m
			} else if len(m.key) == 1 {
				t.short[m.key[0]] = m
			} else if len(m.key) == 2 && t.second != nil {
				page := t.second[m.key[0]]
				if page == nil {
					page = new([256]*metadata[K, V])
					t.second[m.key[0]] = page
				}
				page[m.key[1]] = m
			}
			return
		}
		t.expand()
	}
}

func (t *metaTable[K, V]) erase(m *metadata[K, V]) bool {
	a, b := t.choices(m.hash)
	for _, i := range [2]uint32{a, b} {
		for j, v := range t.buckets[i] {
			if v == m {
				n := t.bucketLen(i) - 1
				t.tags[i][j], t.buckets[i][j] = t.tags[i][n], t.buckets[i][n]
				t.tags[i][n], t.buckets[i][n] = 0, nil
				t.n--
				if len(m.key) == 0 {
					t.root = nil
				} else if len(m.key) == 1 {
					t.short[m.key[0]] = nil
				} else if len(m.key) == 2 && t.second != nil {
					t.second[m.key[0]][m.key[1]] = nil
				}
				return true
			}
		}
	}
	return false
}

func (t *metaTable[K, V]) expand() {
	oldMask, oldTags, oldBuckets := t.mask, t.tags, t.buckets
	if oldMask == ^uint32(0) {
		panic("wormhole: metadata hash table exhausted")
	}
	t.mask = oldMask*2 + 1
	t.tags = make([]metaTags, len(oldTags)*2)
	t.buckets = make([]metaBucket[K, V], len(oldBuckets)*2)
	// Each old bucket partitions into two; no destination can overflow.
	for i, bucket := range oldBuckets {
		for j, m := range bucket {
			if m == nil {
				break
			}
			h := m.hash
			if h&oldMask != uint32(i) {
				h = bits.ReverseBytes32(h)
			}
			to := h & t.mask
			n := t.bucketLen(to)
			t.tags[to][n], t.buckets[to][n] = oldTags[i][j], m
		}
	}
	// Squeeze displaced entries back home whenever expansion made room.
	for i := range t.buckets {
		n := t.bucketLen(uint32(i))
		for j := n - 1; j >= 0; j-- {
			m := t.buckets[i][j]
			home := m.hash & t.mask
			if home == uint32(i) {
				continue
			}
			k := t.bucketLen(home)
			if k == 8 {
				continue
			}
			t.tags[home][k], t.buckets[home][k] = t.tags[i][j], m
			n--
			t.tags[i][j], t.buckets[i][j] = t.tags[i][n], t.buckets[i][n]
			t.tags[i][n], t.buckets[i][n] = 0, nil
		}
	}
}

// longestPrefix first trusts tag hits, using incremental hashing. A false hit
// can only overestimate the match length. Validate the final prefix, then
// repeat with full comparisons below that bound if necessary (paper 3.1).
func (t *metaTable[K, V]) longestPrefix(key string) (*metadata[K, V], int, uint32) {
	gap := min(t.maxPrefix, len(key)) + 1
	lo, h := 0, crcSeed
	for gap > 1 {
		inc := gap / 2
		if gap > 7 {
			inc = gap >> 3 << 2
		} // native x4 incremental-hash step
		hash := crcExtend(h, key[lo:lo+inc])
		if t.peek(hash) {
			lo += inc
			h = hash
			gap -= inc
		} else {
			gap = inc
		}
	}
	if m := t.get(key[:lo], h); m != nil {
		return m, lo, h
	}
	// Native's exact retry is biased toward the optimistic upper bound. After
	// a confirmed prefix, its bitmap proves either that one more byte exists
	// or that this is already the LPM, avoiding another hash-table probe.
	gap = lo
	lo, h = 0, crcSeed
	m := t.get("", h)
	for gap > 1 {
		inc := gap * 3 / 4
		hash := crcExtend(h, key[lo:lo+inc])
		if candidate := t.get(key[:lo+inc], hash); candidate != nil {
			lo, h, m = lo+inc, hash, candidate
			gap -= inc
			if !m.children.has(key[lo]) {
				break
			}
			h = crcByte(h, key[lo])
			lo++
			gap--
			m = nil
		} else {
			gap = inc
		}
	}
	if m == nil {
		m = t.get(key[:lo], h)
	}
	return m, lo, h
}

func (t *metaTable[K, V]) jump(key string) *leaf[K, V] {
	if isStringKey[K]() && t.maxPrefix <= 2 && len(key) != 0 {
		return t.jumpFixed(key)
	}
	m, n, h := t.longestPrefix(key)
	if n == len(key) || int(m.children.min) > int(key[n]) {
		return m.lpath
	}
	if int(m.children.max) < int(key[n]) {
		return m.right
	}
	sibling := byte(m.children.less(int(key[n])))
	return t.child(key[:n], h, sibling).right
}

// jumpFixed follows confirmed child bits for numeric encodings and shallow
// string tries. Cached prefix nodes avoid speculative cuckoo probes for
// prefixes the bitmap already proves absent.
func (t *metaTable[K, V]) jumpFixed(key string) *leaf[K, V] {
	if t.routes != nil {
		if l := t.routes.lookup(key); l != nil {
			return (*leaf[K, V])(l)
		}
	}
	m := t.short[key[0]]
	if m == nil {
		root := t.root
		if int(root.children.min) > int(key[0]) {
			return root.lpath
		}
		if int(root.children.max) < int(key[0]) {
			return root.right
		}
		return t.short[byte(root.children.less(int(key[0])))].right
	}
	n := 1
	if t.second != nil {
		page := t.second[key[0]]
		if page == nil {
			return m.lpath
		}
		child := page[key[1]]
		if child == nil {
			if int(m.children.min) > int(key[1]) {
				return m.lpath
			}
			if int(m.children.max) < int(key[1]) {
				return m.right
			}
			return page[byte(m.children.less(int(key[1])))].right
		}
		m, n = child, 2
		if m.children.min == noChild {
			return m.lpath
		}
	}
	h := m.hash // cached nodes already confirm exactly key[:n]
	for n < len(key) && m.children.has(key[n]) {
		token := key[n]
		m = t.child(key[:n], h, token)
		h = crcByte(h, token)
		n++
	}
	if n == len(key) || int(m.children.min) > int(key[n]) {
		return m.lpath
	}
	if int(m.children.max) < int(key[n]) {
		return m.right
	}
	return t.child(key[:n], h, byte(m.children.less(int(key[n])))).right
}

// insertAnchor is wormmeta_split. The leaf must already be linked into the
// list. Only prefixes whose extrema or child set can change are touched.
func (t *metaTable[K, V]) insertAnchor(l *leaf[K, V]) {
	if t.routes != nil {
		next := ""
		if l.next != nil {
			next = l.next.anchor
		}
		t.routes.assign(l.anchor, next, unsafe.Pointer(l))
	}
	i := 0
	if l.next != nil {
		i = commonPrefix(l.prev.anchor, l.next.anchor)
	}
	h := crcExtend(crcSeed, l.anchor[:i])
	for {
		key := l.anchor[:i]
		m := t.get(key, h)
		if m == nil {
			m = newMetadata(key, h, l)
			t.put(m)
		} else {
			if m.left == l.next {
				m.setLeft(l)
			} else if m.right == l.prev {
				m.right = l
			}
		}
		if i < len(l.anchor) {
			m.children.set(l.anchor[i])
		} else {
			break
		}
		h = crcByte(h, l.anchor[i])
		i++
	}
	t.maxPrefix = max(t.maxPrefix, i)
	if l.next != nil {
		t.updateLeftPath(l.anchor, l.next.anchor, l)
	}
}

func (t *metaTable[K, V]) updateLeftPath(a, next string, lpath *leaf[K, V]) {
	i := commonPrefix(a, next) + 1
	if i >= len(next) {
		return
	}
	h := crcExtend(crcSeed, next[:i])
	for i < len(next) {
		t.get(next[:i], h).lpath = lpath
		h = crcByte(h, next[i])
		i++
	}
}

// removeAnchor is wormmeta_merge. The removed leaf retains its old neighbors;
// those neighbors must already bypass it. The first (empty-anchor) leaf stays.
func (t *metaTable[K, V]) removeAnchor(l *leaf[K, V]) {
	if t.routes != nil {
		next := ""
		if l.next != nil {
			next = l.next.anchor
		}
		t.routes.assign(l.anchor, next, unsafe.Pointer(l.prev))
	}
	i := 0
	if l.prev != nil && l.next != nil {
		i = commonPrefix(l.prev.anchor, l.next.anchor)
	}
	h := crcExtend(crcSeed, l.anchor[:i])
	var parent *metadata[K, V]
	for {
		m := t.get(l.anchor[:i], h)
		if m.left == m.right {
			t.erase(m)
			if parent != nil {
				parent.children.clear(l.anchor[i-1])
				parent = nil
			}
			if m.children.min == noChild {
				break
			}
		} else {
			if m.left == l {
				m.setLeft(l.next)
			} else if m.right == l {
				m.right = l.prev
			}
			parent = m
		}
		if i >= len(l.anchor) {
			break
		}
		h = crcByte(h, l.anchor[i])
		i++
	}
	if l.next != nil {
		t.updateLeftPath(l.anchor, l.next.anchor, l.prev)
	}
}
