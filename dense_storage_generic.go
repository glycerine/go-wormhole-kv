//go:build !amd64 || race

package wormhole

import "cmp"

// Portable and race builds retain the bucket index. Scalar arrays stay after
// records so they do not extend the GC's pointer scanning range.
type denseStorage[K cmp.Ordered, V any] struct {
	records [leafCapacity]record[K, V]
	buckets [256]uint8
	chain   [leafCapacity]uint8
}
