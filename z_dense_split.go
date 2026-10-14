package wormhole

// Dense numeric leaves own their record storage, so pass a new record by value
// into the split instead of allocating a temporary record on the heap. Keep
// this cold structural helper after the generated point paths in source order.
func (r *Ref[K, V]) insertSplitNumericLocked(l *leaf[K, V], key K, value V, tag uint16) error {
	if !l.dense {
		return r.insertLocked(l, &record[K, V]{key: key, value: value}, tag)
	}
	return r.publishDenseSplitLocked(l, l.splitDenseNumeric(key, value, tag, r.m.keyKind))
}

// publishDenseSplitLocked consumes l's write lock and links an unpublished
// right leaf into both metadata snapshots.
func (r *Ref[K, V]) publishDenseSplitLocked(l, right *leaf[K, V]) error {
	if right == nil {
		l.mu.Unlock()
		return ErrAnchorTooLong
	}
	right.mu.Lock()
	m := r.m
	m.metalock.Lock()
	old, next := m.nextSnapshot()
	l.next = right
	if right.next != nil {
		right.next.prev = right
	}
	l.version, right.version = next.version, next.version
	next.table.insertAnchor(right)
	r.epoch.addCount(1)
	m.current.Store(next)
	l.mu.Unlock()
	right.mu.Unlock()
	m.epochs.wait(next.version)
	old.table.insertAnchor(right)
	m.metalock.Unlock()
	return nil
}

// Dense string leaves own their record storage, so keep the generated string
// split path independent of the shared runtime kind dispatch in leaf.split.
// Keep this after the accepted numeric helpers to preserve their source order.
func (r *Ref[K, V]) insertSplitStringLocked(l *leaf[K, V], key K, value V, tag uint16) error {
	if !l.dense {
		return r.insertLocked(l, &record[K, V]{key: key, value: value}, tag)
	}
	l.prefetchStringSplit()
	if len(l.anchor) >= 3 {
		l.syncSortedOccupied()
	}
	return r.publishDenseSplitLocked(l, l.splitDense(key, value, tag))
}
