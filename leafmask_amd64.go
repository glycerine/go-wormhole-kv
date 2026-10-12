//go:build amd64 && !race

package wormhole

const useLeafSIMD = true

// matchLeafTags compares all stable-slot fingerprints using baseline SSE2.
// The caller holds the leaf lock, including for this assembly read.
//
//go:noescape
func matchLeafTags(tags *[leafCapacity]uint16, tag uint16) (lo, hi uint64)
