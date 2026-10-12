//go:build !race

package wormhole

// shiftIndices mirrors native wormleaf_shift_inc/dec with baseline SSE2.
// n is at most leafCapacity; from/count describe a slot interval in [0,128),
// and delta is +1 or -1. Like native, the last vector may repair inactive
// padding in ss. Only ss[:n] is live, and insertion overwrites the next cell.
//
//go:noescape
func shiftIndices(ss *[leafCapacity]uint8, n, from, count, delta int)
