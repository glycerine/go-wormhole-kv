//go:build !race

package wormhole

import "unsafe"

// Mirror wh.c's wormhmap_prefetch_pmap and wormleaf_prefetch. These functions
// issue cache hints only; callers still need epochs/locks before reading data.
//
//go:noescape
func prefetchMetaBuckets(a, b unsafe.Pointer)

//go:noescape
func prefetchLeafSlot(tag *uint16, slot unsafe.Pointer)

// Prefetch a prepared write key before cached-leaf locking. PREFETCHT0 is a
// non-faulting hint; it does not read key bytes or replace synchronization.
//
//go:noescape
func prefetchWriteKey(key *byte)

// Prefetch a successor's lock, sorted indices, and bounded record storage.
// These are cache hints; the successor still needs its own read lock.
//
//go:noescape
func prefetchScanLeaf(header, slots, data unsafe.Pointer, size uintptr)
