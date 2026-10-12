// Ported from wh.c, Copyright (c) 2016--2021 Wu, Xingbo <wuxb45@gmail.com>.
// See LICENSE in the repository root. No warranty, explicit or implicit.

package wormhole

// Merge calls fn exactly once under the key's leaf write lock, including when
// the key is absent. If fn returns store=true, its value is inserted/replaced;
// store=false leaves the map unchanged (it does not delete an existing key).
// This corresponds to native wormhole_merge with a non-NULL/NULL return.
//
// Callbacks must not perform operations that acquire leaf locks on this Map.
// Re-entering or closing this Ref from fn panics. A callback panic releases
// the lock and leaves the record unchanged. Stored keys remain immutable.
func (r *Ref[K, V]) Merge(key K, fn func(value V, exists bool) (newValue V, store bool)) error {
	r.checkAvailable()
	route := routingKeyKind(key, &r.routing, r.m.keyKind)
	tag := keyTag(route)
	l := r.lockLeaf(route, tag, true)
	locked := true
	defer func() {
		r.inCallback = false
		if locked {
			l.mu.Unlock()
		}
	}()
	i := l.match(key, tag)
	var value V
	exists := i < leafCapacity
	if exists {
		value = l.recordAtSlot(i).value
	}
	r.inCallback = true
	value, store := fn(value, exists)
	r.inCallback = false
	if !store {
		return nil
	}
	if exists {
		l.recordAtSlot(i).value = value
		return nil
	}
	locked = false
	return r.insertValueLocked(l, key, value, tag)
}

// Inspect calls fn exactly once under a leaf read lock, including for a
// missing key, and returns whether the key exists. This corresponds to native
// wormhole_inpr without exposing mutable record memory. The callback follows
// Merge's non-reentrancy rules; a panic releases the read lock.
func (r *Ref[K, V]) Inspect(key K, fn func(value V, exists bool)) bool {
	r.checkAvailable()
	route := routingKeyKind(key, &r.routing, r.m.keyKind)
	tag := keyTag(route)
	l := r.lockLeaf(route, tag, false)
	defer func() { r.inCallback = false; l.mu.RUnlock() }()
	i := l.match(key, tag)
	var value V
	exists := i < leafCapacity
	if exists {
		value = l.recordAtSlot(i).value
	}
	r.inCallback = true
	fn(value, exists)
	return exists
}

// Update calls fn exactly once under a leaf write lock and replaces an
// existing value with its result. For a missing key fn still runs, but its
// result is ignored and false is returned. This corresponds to native
// wormhole_inpw using immutable value replacement instead of writable bytes.
// It never inserts a key. The callback follows Merge's non-reentrancy rules;
// a panic releases the lock and leaves the record unchanged.
func (r *Ref[K, V]) Update(key K, fn func(value V, exists bool) V) bool {
	r.checkAvailable()
	route := routingKeyKind(key, &r.routing, r.m.keyKind)
	tag := keyTag(route)
	l := r.lockLeaf(route, tag, true)
	defer func() { r.inCallback = false; l.mu.Unlock() }()
	i := l.match(key, tag)
	var value V
	exists := i < leafCapacity
	if exists {
		value = l.recordAtSlot(i).value
	}
	r.inCallback = true
	updated := fn(value, exists)
	if exists {
		l.recordAtSlot(i).value = updated
	}
	return exists
}
