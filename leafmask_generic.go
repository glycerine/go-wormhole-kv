//go:build !amd64 || race

package wormhole

const useLeafSIMD = false

func matchLeafTags(tags *[leafCapacity]uint16, tag uint16) (lo, hi uint64) {
	for i, candidate := range tags {
		if candidate == tag {
			if i < 64 {
				lo |= uint64(1) << i
			} else {
				hi |= uint64(1) << (i - 64)
			}
		}
	}
	return
}
