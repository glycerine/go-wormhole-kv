package wormhole

import (
	"cmp"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

// ErrAnchorTooLong means splitting a full leaf would require an anchor longer
// than the native implementation's 16-bit metadata length limit. The attempted
// insertion has no effect. Long keys are otherwise accepted.
var ErrAnchorTooLong = errors.New("wormhole: split anchor exceeds 65535 bytes")

// metaSnapshot is immutable, even when its table is retired and reused. A
// reader can safely retain the version after leaving its metadata epoch.
type metaSnapshot[K cmp.Ordered, V any] struct {
	table   *metaTable[K, V]
	version uint64
}

// Map is a concurrent ordered key/value store. Construct it with New and use
// one Ref per goroutine. Keys use their natural order, with NaNs last. Values may have any type. A Map must not be copied.
type Map[K cmp.Ordered, V any] struct {
	metalock sync.Mutex
	current  atomic.Pointer[metaSnapshot[K, V]]
	tables   [2]*metaTable[K, V]
	epochs   epochRegistry
	first    *leaf[K, V]
	keyKind  reflect.Kind
}

// Options controls construction of a Map. Its zero value uses the lower-memory
// metadata configuration with two-byte metadata caching disabled.
type Options struct {
	// MetaCache enables the numeric two-byte metadata lookup cache and reserves
	// larger initial hash tables. This can improve lookup/write speed at the cost of
	// retained heap and GC scanning. The separate numeric leaf-route cache is
	// enabled in both configurations. Tables always grow automatically.
	MetaCache bool
}

// New creates an empty Wormhole with the lower-memory default Options, two
// metadata tables, and one leaf. The two-byte metadata lookup cache is off.
func New[K cmp.Ordered, V any]() *Map[K, V] {
	return NewWithOptions[K, V](Options{})
}

// NewWithOptions creates an empty Wormhole using options. Options are copied
// at construction and cannot be changed on a live Map.
func NewWithOptions[K cmp.Ordered, V any](options Options) *Map[K, V] {
	buckets := 64
	if options.MetaCache {
		buckets = 4096
	}
	first := &leaf[K, V]{version: 1, dense: true, denseStorage: new(denseStorage[K, V])}
	m := &Map[K, V]{first: first, keyKind: reflect.TypeFor[K]().Kind()}
	for i := range m.tables {
		t := newMetaTable[K, V](buckets)
		if m.keyKind != reflect.String {
			if options.MetaCache {
				t.second = new([256]*[256]*metadata[K, V])
			}
			t.routes = newNumericRoutes(unsafe.Pointer(first))
		}
		t.put(newMetadata("", crcSeed, first))
		m.tables[i] = t
	}
	m.current.Store(&metaSnapshot[K, V]{table: m.tables[0], version: 1})
	return m
}

// Len returns a consistent snapshot of the current number of keys. Its cost
// is proportional to the number of open Refs and it can retry during writes.
func (m *Map[K, V]) Len() int64 { return m.epochs.length() }

// Ref owns a reader epoch registration. Use one Ref per goroutine per Map,
// and use each Ref from one goroutine at a time. Do not copy it, and Close it
// when finished. Operations automatically
// mark the Ref inactive after routing,
// so an idle Ref does not delay reuse of old metadata tables.
//
// An Iterator reserves its Ref until closed, even while parked. Close the
// iterator before resuming point operations on that Ref. Do not use a second
// Ref to bypass iterator reservation or callback restrictions. Other goroutines
// can use their own Refs concurrently; the map's locks coordinate access.
type Ref[K cmp.Ordered, V any] struct {
	m                *Map[K, V]
	epoch            *epochRef
	iterator         *Iterator[K, V]
	inCallback       bool
	putLeaf          *leaf[K, V]
	putVersion       uint64
	putPrefix        uint32
	putCandidate     uint32
	putPrefixChecked bool
	putPrefixCovered bool
	putWhole         bool // cached interval covers the entire key space
	putCandidateSeen bool
	routing          [8]byte // caller-owned numeric routing scratch; never retained by metadata
}

// Ref creates an independent caller handle. Creating handles concurrently is
// safe; the Map must have been constructed with New.
func (m *Map[K, V]) Ref() *Ref[K, V] { return &Ref[K, V]{m: m, epoch: m.epochs.register()} }

// Close unregisters a handle. Repeated Close calls are harmless. It must not
// overlap any operation on the same handle. Other handles remain usable.
func (r *Ref[K, V]) Close() {
	if r.inCallback {
		panic("wormhole: cannot close Ref inside its callback")
	}
	if r.m == nil {
		return
	}
	if r.iterator != nil {
		r.iterator.Close()
	}
	r.m.epochs.unregister(r.epoch)
	r.m, r.epoch = nil, nil
	r.putLeaf = nil
}

func (r *Ref[K, V]) checkAvailable() {
	r.checkOpen()
	if r.inCallback {
		panic("wormhole: callback cannot re-enter its Ref")
	}
	if r.iterator != nil {
		panic("wormhole: Ref is reserved by an Iterator; close it before other operations")
	}
}

func (r *Ref[K, V]) checkOpen() {
	if r.m == nil {
		panic("wormhole: operation on a closed Ref")
	}
}

// protectSnapshot announces a snapshot descriptor loaded while parked, then
// reloads the publication BEFORE reading any mutable table data. If a writer
// already passed our parked state and reused first.table, the reload selects
// its newer publication. Otherwise our older announcement protects the newly
// loaded table from every later retirement until we park. Announcing the newer
// version also lets a writer finish retiring the older table immediately.
// Descriptors are immutable and never reused, so pointer comparison has no ABA.
func (r *Ref[K, V]) protectSnapshot(first *metaSnapshot[K, V]) *metaSnapshot[K, V] {
	r.epoch.announce(first.version)
	s := r.m.current.Load()
	if s != first {
		r.epoch.announce(s.version)
	}
	return s
}

// lockLeaf follows native jump-rw version validation. Once jump has completed,
// no mutable metadata is needed: park BEFORE blocking on the leaf lock. Go GC
// keeps the leaf alive during this interval. A retired/moved leaf has a newer
// version and forces a fresh lookup after acquiring the lock. A zero tag skips
// the hash-slot hint for ordered operations that do not need a full-key hash.
func (r *Ref[K, V]) lockLeaf(key string, tag uint16, write bool) *leaf[K, V] {
	r.checkOpen()
	for {
		s := r.protectSnapshot(r.m.current.Load())
		var l *leaf[K, V]
		if isStringKey[K]() {
			l = s.table.jump(key)
		} else {
			l = s.table.jumpFixed(key)
		}

		r.epoch.park()
		if write {
			l.mu.Lock()
		} else {
			l.mu.RLock()
		}
		if l.version <= s.version {
			return l
		}
		if write {
			l.mu.Unlock()
		} else {
			l.mu.RUnlock()
		}
	}
}

// Get returns the value associated with key and whether the key exists.
func (r *Ref[K, V]) Get(key K) (V, bool) {
	r.checkAvailable()
	if isStringKey[K]() {
		return r.getString(key)
	}
	return r.getNumeric(key)
}

// Probe reports whether key exists without returning its value.
func (r *Ref[K, V]) Probe(key K) bool {
	r.checkAvailable()
	if isStringKey[K]() {
		route := stringKey(key)
		return r.probeString(key, route, keyTag(route))
	}
	route := numericRoutingKey(key, &r.routing, r.m.keyKind)
	return r.probeNumeric(key, route, numericKeyTag(&r.routing))
}

func (r *Ref[K, V]) probe(key K, route string, tag uint16) bool {
	if isStringKey[K]() {
		return r.probeString(key, route, tag)
	}
	return r.probeNumeric(key, route, tag)
}

// Put inserts or replaces a value. A failed insertion leaves all keys and
// values unchanged. Values are stored by assignment.
func (r *Ref[K, V]) Put(key K, value V) error {
	r.checkAvailable()
	if isStringKey[K]() {
		route := stringKey(key)
		return r.putWithPrefixString(key, value, route, keyTag(route), keyPrefix(route))
	}
	route := numericRoutingKey(key, &r.routing, r.m.keyKind)
	return r.putWithPrefixNumeric(key, value, route, numericKeyTag(&r.routing), keyPrefix(route))
}

func (r *Ref[K, V]) put(key K, value V, route string, tag uint16) error {
	return r.putWithPrefix(key, value, route, tag, keyPrefix(route))
}

func (r *Ref[K, V]) putWithPrefix(key K, value V, route string, tag uint16, prefix uint32) error {
	if isStringKey[K]() {
		return r.putWithPrefixString(key, value, route, tag, prefix)
	}
	return r.putWithPrefixNumeric(key, value, route, tag, prefix)
}

// lockPutLeaf attempts a finger only when four leading key bytes match the
// previous routed write, or the cached leaf covers the entire key space.
// Random writes normally skip the extra bounds loads.
// Every split/merge changing a leaf interval changes its version. GC keeps
// even a retired cached leaf alive. Under its lock, validate the version and
// actual interval, or reuse a proof that the entire prefix lies in that interval.
// This makes a hit safe without touching metadata or entering an epoch.
func (r *Ref[K, V]) lockPutLeaf(key string, tag uint16, prefix uint32) *leaf[K, V] {
	if len(key) < 4 && !r.putWhole {
		return r.lockLeaf(key, tag, true)
	}
	if l := r.putLeaf; l != nil && (prefix == r.putPrefix || r.putWhole) {
		l.mu.Lock()
		if l.version == r.putVersion {
			if r.putPrefixCovered {
				return l
			}
			if key >= l.anchor && (l.next == nil || key < l.next.anchor) {
				if !r.putPrefixChecked {
					// All strings with this prefix lie in the leaf only when
					// the lower bound precedes the prefix itself and the
					// exclusive upper bound lies beyond the entire prefix.
					p := key[:4]
					r.putPrefixCovered = l.anchor <= p && (l.next == nil ||
						p < l.next.anchor && !strings.HasPrefix(l.next.anchor, p))
					r.putPrefixChecked = true
				}
				return l
			}
		}
		l.mu.Unlock()
	}
	l := r.lockLeaf(key, tag, true)
	r.putLeaf, r.putVersion, r.putPrefix = l, l.version, prefix
	r.putWhole = l.anchor == "" && l.next == nil
	r.putPrefixChecked, r.putPrefixCovered = r.putWhole, r.putWhole
	return l
}

func (r *Ref[K, V]) insertValueLocked(l *leaf[K, V], key K, value V, tag uint16) error {
	if l.dense && l.n < leafCapacity {
		n := l.n
		l.insertDenseEntry(key, value, tag)
		if n == l.sorted && (n == 0 || orderedCompare(l.at(n-1).key, key) < 0) {
			l.sorted++
		}
		r.epoch.addCount(1)
		l.mu.Unlock()
		return nil
	}
	return r.insertLocked(l, &record[K, V]{key: key, value: value}, tag)
}

// insertLocked requires an absent key and consumes l's write lock. Sharing
// this path with Merge keeps its observed value and insertion atomic.
func (r *Ref[K, V]) insertLocked(l *leaf[K, V], kv *record[K, V], tag uint16) error {
	if l.n < leafCapacity {
		l.insert(kv, tag)
		r.epoch.addCount(1)
		l.mu.Unlock()
		return nil
	}
	right := l.split(kv, tag)
	if right == nil {
		l.mu.Unlock()
		return ErrAnchorTooLong
	}
	right.mu.Lock()
	m := r.m
	m.metalock.Lock() // epoch already parked, so another writer can finish
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

// nextSnapshot requires metalock. The prior writer waited for all readers
// before bringing the inactive table up to date, so it is safe to mutate now.
func (m *Map[K, V]) nextSnapshot() (*metaSnapshot[K, V], *metaSnapshot[K, V]) {
	old := m.current.Load()
	t := m.tables[0]
	if old.table == t {
		t = m.tables[1]
	}
	if old.version == parkedEpoch-1 {
		panic("wormhole: metadata version exhausted")
	}
	return old, &metaSnapshot[K, V]{table: t, version: old.version + 1}
}

// Delete removes key and reports whether it existed. As in the native safe
// API, leaf merges always absorb the right neighbor into the left leaf.
func (r *Ref[K, V]) Delete(key K) bool {
	r.checkAvailable()
	route := routingKeyKind(key, &r.routing, r.m.keyKind)
	tag := keyTag(route)
	l := r.lockLeaf(route, tag, true)
	i := l.match(key, tag)
	if i == leafCapacity {
		l.mu.Unlock()
		return false
	}
	l.remove(i)
	r.epoch.addCount(-1)
	r.tryMerge(l)
	return true
}

// tryMerge consumes the left leaf's write lock. It uses the native safe API's
// rightward merge policy and its 3/4-full heuristic.
func (r *Ref[K, V]) tryMerge(l *leaf[K, V]) {
	right := l.next
	if right == nil || l.n >= leafCapacity*3/4 {
		l.mu.Unlock()
		return
	}
	// Native's unlocked approximate read of the neighbor count is not valid
	// under Go's memory model. Read it only after locking left-to-right.
	right.mu.Lock()
	if l.n+right.n >= leafCapacity*3/4 && l.n != 0 || l.n+right.n > leafCapacity {
		right.mu.Unlock()
		l.mu.Unlock()
		return
	}
	l.mergeRight(right)
	r.m.unlinkRight(l, right, false)
}

// unlinkRight requires both leaf write locks and a parked caller epoch. Data
// must have been moved into left, or right must be empty. Range deletion keeps
// the left lock while unlinking successive emptied neighbors, as in native
// wormhole_meta_merge(..., unlock_leaf1=false).
func (m *Map[K, V]) unlinkRight(l, right *leaf[K, V], keepLeft bool) {
	m.metalock.Lock()
	old, next := m.nextSnapshot()
	l.next = right.next
	if right.next != nil {
		right.next.prev = l
	}
	l.version, right.version = next.version, next.version
	next.table.removeAnchor(right)
	m.current.Store(next)
	if !keepLeft {
		l.mu.Unlock()
	}
	right.mu.Unlock()
	m.epochs.wait(next.version)
	old.table.removeAnchor(right)
	m.metalock.Unlock()
}
