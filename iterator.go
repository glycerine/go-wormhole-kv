package wormhole

import "cmp"

// Iterator scans in key order, with NaNs last. Seek positions at the first key >= its
// argument; Next returns the current pair and advances. It holds a leaf read
// lock while positioned, blocking writes to that leaf until it moves, parks,
// or closes. Traversal locks the next leaf before releasing the current one.
//
// Iteration is not a snapshot of the whole Map: later leaves may reflect
// concurrent updates. Returned values remain valid after advancing. Use an
// Iterator from one goroutine at a time and Close it when finished.
//
// Use the goroutine's single Ref for this Map. Close the iterator before using
// that Ref for point operations; Park invalidates the position but keeps the
// Ref reserved. Do not use a second Ref to bypass this reservation or callback
// restrictions. Other goroutines can use their own Refs concurrently; leaf
// locks coordinate access without callers tracking each other's positions.
type Iterator[K cmp.Ordered, V any] struct {
	ref   *Ref[K, V]
	leaf  *leaf[K, V]
	index int
}

// Iterator creates an unpositioned iterator and reserves this Ref until the
// iterator is closed. Use Seek to position it. Other Ref operations, including
// creating a second iterator, panic while this iterator exists, even if parked.
// Do not use a second Ref in this goroutine to bypass that reservation.
func (r *Ref[K, V]) Iterator() *Iterator[K, V] {
	r.checkAvailable()
	it := &Iterator[K, V]{ref: r}
	r.iterator = it
	return it
}

// Seek positions at the first key >= key, or exhausts the iterator if none.
func (it *Iterator[K, V]) Seek(key K) {
	it.checkCallback()
	if it.ref == nil {
		panic("wormhole: Seek on a closed Iterator")
	}
	it.Park()
	l := it.ref.lockLeaf(routingKeyKind(key, &it.ref.routing, it.ref.m.keyKind), 0, false)
	l.syncSortedRead()
	it.leaf, it.index = l, l.seek(key)
	it.fix()
}

// fix skips exhausted/empty leaves, preserving the native left-to-right lock
// order. Holding the current leaf prevents its successor from being unlinked.
func (it *Iterator[K, V]) fix() {
	for it.leaf != nil && it.index >= it.leaf.n {
		l := it.leaf
		next := l.next
		if next != nil {
			next.mu.RLock()
		}
		l.mu.RUnlock()
		it.leaf, it.index = next, 0
		if next != nil {
			next.syncSortedRead()
		}
	}
}

// Valid reports whether the iterator is positioned at a key.
func (it *Iterator[K, V]) Valid() bool { return it.leaf != nil }

// Peek returns the current key/value without advancing, or ok=false if invalid.
func (it *Iterator[K, V]) Peek() (key K, value V, ok bool) {
	if it.leaf == nil {
		return key, value, false
	}
	kv := it.leaf.at(it.index)
	return kv.key, kv.value, true
}

// Next returns the current key/value and advances, or ok=false if invalid.
func (it *Iterator[K, V]) Next() (key K, value V, ok bool) {
	it.checkCallback()
	key, value, ok = it.Peek()
	if ok {
		it.index++
		if it.index == it.leaf.n {
			it.fix()
		}
	}
	return
}

// Skip advances by up to n keys, releasing locks when the iterator exhausts.
func (it *Iterator[K, V]) Skip(n uint) {
	it.checkCallback()
	for n != 0 && it.leaf != nil {
		step := min(n, uint(it.leaf.n-it.index))
		it.index += int(step)
		n -= step
		it.fix()
	}
}

// Park releases the current leaf lock and invalidates the position. The
// iterator still reserves its Ref and can be positioned again with Seek.
func (it *Iterator[K, V]) Park() {
	it.checkCallback()
	if it.leaf != nil {
		it.leaf.mu.RUnlock()
		it.leaf = nil
	}
	it.index = 0
}

// Close releases the leaf lock and returns its Ref to point-operation use.
// Repeated Close calls are harmless. Closing the Ref also closes its iterator.
func (it *Iterator[K, V]) Close() {
	it.checkCallback()
	it.Park()
	if it.ref != nil {
		it.ref.iterator = nil
		it.ref = nil
	}
}

func (it *Iterator[K, V]) checkCallback() {
	if it.ref != nil && it.ref.inCallback {
		panic("wormhole: callback cannot move, park, close, or re-enter its Iterator")
	}
}

// Inspect calls fn exactly once with the current key/value, or zero values
// and exists=false when unpositioned, parked, or exhausted. It returns whether
// a record exists without advancing. This corresponds to wormhole_iter_inp.
//
// The iterator keeps its leaf read lock throughout fn and after it returns or
// panics. Peek and Valid are allowed inside fn. Moving, parking, closing, or
// recursively inspecting this iterator, and using/closing its Ref, panic.
// Other operations that acquire leaf locks on this Map must not run in fn.
// Inspect panics if the iterator is closed.
func (it *Iterator[K, V]) Inspect(fn func(key K, value V, exists bool)) bool {
	it.checkCallback()
	if it.ref == nil {
		panic("wormhole: Inspect on a closed Iterator")
	}
	key, value, exists := it.Peek()
	r := it.ref
	r.inCallback = true
	defer func() { r.inCallback = false }()
	fn(key, value, exists)
	return exists
}
