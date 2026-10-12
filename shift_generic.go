//go:build !amd64 || race

package wormhole

// Race builds keep every access visible to the race detector.
func shiftIndices(ss *[leafCapacity]uint8, n, from, count, delta int) {
	scalarShiftIndices(ss, n, from, count, delta)
}
