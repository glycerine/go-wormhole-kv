//go:build amd64 && !race

package wormhole

import "cmp"

// SIMD reads tags directly from the leaf and needs no bucket index. The empty
// arrays keep shared portable index code type-correct; constant guards remove
// it from this build. Put empty fields first to avoid Go's zero-sized tail
// padding, preserving record offset zero and a records-only allocation.
// Pointer-free K/V still make this object invisible to the GC pointer scanner.
type denseStorage[K cmp.Ordered, V any] struct {
	buckets [0]uint8
	chain   [0]uint8
	records [leafCapacity]record[K, V]
}
