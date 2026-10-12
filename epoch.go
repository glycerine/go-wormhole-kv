package wormhole

import (
	"runtime"
	"sync"
	"sync/atomic"
)

const parkedEpoch = ^uint64(0)

// epochRef belongs to one caller at a time. The atomic state is shared only
// with metadata writers: 0 means entering (snapshot not loaded yet), positive
// values name a snapshot, and parkedEpoch means no metadata is being read.
type epochRef struct {
	state atomic.Uint64
	_     [56]byte // keep independently written states on separate cache lines
	// Only this Ref writes its counter. Separate it from the epoch state and
	// neighboring allocations; metadata writers need only the first line.
	added   atomic.Uint64
	removed atomic.Uint64
	_       [56]byte
}

// epochRegistry provides the native QSBR grace period with Go atomics. Writers
// are serialized externally. Registration is cold; lookup does not lock this
// registry. Go GC replaces manual pointer reclamation, not the grace period.
type epochRegistry struct {
	mu          sync.Mutex
	refs        map[*epochRef]struct{}
	closedCount int64 // accumulated contributions from unregistered Refs; mu
}

func (q *epochRegistry) register() *epochRef {
	r := new(epochRef)
	r.park()
	q.mu.Lock()
	if q.refs == nil {
		q.refs = make(map[*epochRef]struct{})
	}
	q.refs[r] = struct{}{}
	q.mu.Unlock()
	return r
}

func (q *epochRegistry) unregister(r *epochRef) {
	// Park first: a concurrent wait can finish even while holding q.mu.
	r.park()
	q.mu.Lock()
	q.closedCount += int64(r.added.Load() - r.removed.Load())
	delete(q.refs, r)
	q.mu.Unlock()
}

func (r *epochRef) enter()                  { r.state.Store(0) }
func (r *epochRef) announce(version uint64) { r.state.Store(version) }
func (r *epochRef) park()                   { r.state.Store(parkedEpoch) }

// addCount has one writer, the owning Ref. Monotonic tallies avoid ABA during
// Len's double collect without adding version updates to the write path.
func (r *epochRef) addCount(delta int64) {
	if delta > 0 {
		r.added.Add(uint64(delta))
	} else if delta < 0 {
		r.removed.Add(uint64(-delta))
	}
}

// length takes an atomic snapshot of the per-Ref signed contributions. A
// plain sum would not be linearizable when increments and decrements overlap.
// Registration/retirement is frozen by mu. Compare the aggregate additions and
// removals from two successive passes, not their difference: both aggregates
// are monotonic, so equality means every individual tally remained unchanged
// between its two reads. Their intervals overlap at the end of the first pass.
// This avoids an allocated per-Ref snapshot. As with epoch versions, assume
// fewer than 2^64 tally increments occur between passes, ruling out wrap ABA.
func (q *epochRegistry) length() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.refs) <= 1 {
		for r := range q.refs {
			for {
				a := r.added.Load()
				d := r.removed.Load()
				if r.added.Load() == a {
					return q.closedCount + int64(a-d)
				}
				runtime.Gosched()
			}
		}
		return q.closedCount
	}
	a, d := q.tallies()
	for {
		nextA, nextD := q.tallies()
		if nextA == a && nextD == d {
			return q.closedCount + int64(a-d)
		}
		a, d = nextA, nextD
		runtime.Gosched()
	}
}

// tallies requires mu; owning Refs can still update their atomic tallies.
func (q *epochRegistry) tallies() (added, removed uint64) {
	for r := range q.refs {
		added += r.added.Load()
		removed += r.removed.Load()
	}
	return
}

// wait must follow publication of target and precede reuse of the old table.
// Before reading mutable metadata a caller must either enter before loading
// the publication, or announce then reload it as protectSnapshot does. Thus
// a caller observed parked can only read this target or a newer publication
// when it next becomes active. Observing a new state is likewise sufficient.
func (q *epochRegistry) wait(target uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for r := range q.refs {
		// Metadata readers normally park after a short lookup. Poll briefly
		// before handing this P to the scheduler: an immediate Gosched can
		// delay publication and wake pinned writers blocked on metalock.
		// Bound the polling so a preempted reader still gets time to run,
		// including when only one P is available.
		for spins := 0; r.state.Load() < target; spins++ {
			if spins == 1024 {
				runtime.Gosched()
				spins = 0
			}
		}
	}
}
