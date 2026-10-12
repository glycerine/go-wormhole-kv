//go:build !race

package wormhole

// SSE2 is part of the amd64 baseline. The unaligned load accommodates Go's
// allocation alignment while retaining native eight-tag vector comparisons.
//
//go:noescape
func matchTagMask(tags *metaTags, tag uint16) uint32
