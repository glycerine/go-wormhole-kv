//go:build !amd64 || race

package wormhole

func matchTagMask(tags *metaTags, tag uint16) uint32 { return scalarTagMask(tags, tag) }

func matchEitherTag(a, b *metaTags, tag uint16) bool {
	return matchTagMask(a, tag) != 0 || matchTagMask(b, tag) != 0
}
