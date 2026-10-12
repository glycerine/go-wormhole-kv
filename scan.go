package wormhole

import "unsafe"

// The current leaf lock keeps its successor linked. Its storage pointers are
// initialized before publication and immutable, so hints need no successor
// lock. Mutable records and indices are read only after lock coupling.
func (l *leaf[K, V]) prefetchSuccessor() {
	if next := l.next; next != nil {
		data := unsafe.Pointer(next.hs)
		size := unsafe.Sizeof(*next.hs)
		if next.dense {
			data = unsafe.Pointer(next.denseStorage)
			size = min(unsafe.Sizeof(next.records), uintptr(1024))
		}
		prefetchScanLeaf(unsafe.Pointer(next), unsafe.Pointer(&next.ss[0]), data, size)
	}
}

// Scan visits up to limit records in key order, beginning at the first key >=
// start. It returns the number visited, including a record whose callback
// returns false. A nonpositive limit visits nothing. NaNs sort last.
//
// Scan holds a leaf read lock while calling visit and locks each successor
// before releasing the current leaf. Like Iterator, it is not a snapshot of
// the whole Map. Returned keys and values follow the usual ownership rules.
// The callback must not use or close this Ref, or acquire leaf locks on this
// Map through another Ref. A callback panic releases the lock and Ref.
func (r *Ref[K, V]) Scan(start K, limit int, visit func(K, V) bool) (count int) {
	r.checkAvailable()
	if limit <= 0 {
		return 0
	}
	l := r.lockLeaf(routingKeyKind(start, &r.routing, r.m.keyKind), 0, false)
	defer func() {
		if l != nil {
			l.mu.RUnlock()
		}
		r.inCallback = false
	}()
	l.syncSortedRead()
	index := l.seek(start)
	r.inCallback = true
	for l != nil {
		end := index + min(limit-count, l.n-index)
		slots := l.ss[index:end]
		if limit-count > len(slots) {
			l.prefetchSuccessor()
		}
		// Hoist the layout choice out of the per-record loop.
		if l.dense {
			data := l.denseStorage
			offset := 0
			// Small records can be loaded in pairs before either callback,
			// overlapping the dependent slot/record loads. Large values keep
			// the single-record path to avoid an extra temporary copy.
			if unsafe.Sizeof(record[K, V]{}) <= 32 {
				for ; offset+1 < len(slots); offset += 2 {
					a, b := data.records[slots[offset]], data.records[slots[offset+1]]
					if !visit(a.key, a.value) {
						return count + offset + 1
					}
					if !visit(b.key, b.value) {
						return count + offset + 2
					}
				}
			}
			for ; offset < len(slots); offset++ {
				slot := slots[offset]
				kv := &data.records[slot]
				if !visit(kv.key, kv.value) {
					return count + offset + 1
				}
			}
		} else {
			for offset, slot := range slots {
				kv := l.hs[slot]
				if !visit(kv.key, kv.value) {
					return count + offset + 1
				}
			}
		}
		count += len(slots)
		if count == limit {
			return count
		}
		next := l.next
		if next != nil {
			next.mu.RLock()
		}
		l.mu.RUnlock()
		l, index = next, 0
		if l != nil {
			l.syncSortedRead()
		}
	}
	return count
}
