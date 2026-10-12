// Ported from wh.c, Copyright (c) 2016--2021 Wu, Xingbo <wuxb45@gmail.com>.
// See LICENSE in the repository root. No warranty, explicit or implicit.

package wormhole

// DeleteRange removes keys in [start,end) and returns the number removed.
// An end <= start removes nothing. NaNs are ordered last.
// Like native wormhole_delr, this is not an atomic transaction across leaves;
// concurrent operations may interleave as leaf locks are acquired/released.
func (r *Ref[K, V]) DeleteRange(start, end K) int64 {
	r.checkAvailable()
	if orderedCompare(end, start) <= 0 {
		return 0
	}
	return r.deleteRange(start, &end)
}

// DeleteFrom removes all keys >= start, with no upper bound.
func (r *Ref[K, V]) DeleteFrom(start K) int64 {
	r.checkAvailable()
	return r.deleteRange(start, nil)
}

func (r *Ref[K, V]) deleteRange(start K, end *K) int64 {
	l := r.lockLeaf(routingKeyKind(start, &r.routing, r.m.keyKind), 0, true)
	l.syncSorted()
	begin, finish := l.seek(start), l.n
	if end != nil {
		finish = l.seek(*end)
	}
	deleted := int64(finish - begin)
	l.deleteSortedRange(begin, finish)
	r.epoch.addCount(-deleted)
	if l.n > begin {
		r.tryMerge(l)
		return deleted
	}
	for l.next != nil {
		right := l.next
		right.mu.Lock()
		right.syncSorted()
		finish = right.n
		if end != nil {
			finish = right.seek(*end)
		}
		deleted += int64(finish)
		right.deleteSortedRange(0, finish)
		r.epoch.addCount(-int64(finish))
		if right.n == 0 {
			// Keep the left leaf locked while removing fully deleted nodes.
			r.m.unlinkRight(l, right, true)
			continue
		}
		// The upper bound stopped in this leaf. Native attempts a final merge
		// whenever the remaining pair fits, independent of the point heuristic.
		if l.n+right.n <= leafCapacity {
			l.mergeRight(right)
			r.m.unlinkRight(l, right, false)
		} else {
			right.mu.Unlock()
			l.mu.Unlock()
		}
		return deleted
	}
	l.mu.Unlock()
	return deleted
}
