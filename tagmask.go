package wormhole

// scalarTagMask returns two bits per matching slot, matching native
// wormhmap_match_mask's byte mask. Keep this implementation for portable and
// race builds, where ordinary reads allow the detector to observe accesses.
func scalarTagMask(tags *metaTags, tag uint16) uint32 {
	var mask uint32
	for i, v := range tags {
		if v == tag {
			mask |= 3 << (i * 2)
		}
	}
	return mask
}
