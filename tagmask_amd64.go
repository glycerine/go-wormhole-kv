//go:build !race

package wormhole

// SSE2 is part of the amd64 baseline. The unaligned load accommodates Go's
// allocation alignment while retaining native eight-tag vector comparisons.
//
//go:noescape
func matchTagMask(tags *metaTags, tag uint16) uint32

// matchEitherTag checks the second bucket only if the first does not match.
// Callers retain the same epoch protection as for separate mask lookups.
//
//go:noescape
func matchEitherTag(a, b *metaTags, tag uint16) bool
