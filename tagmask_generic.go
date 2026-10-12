//go:build !amd64 || race

package wormhole

func matchTagMask(tags *metaTags, tag uint16) uint32 { return scalarTagMask(tags, tag) }
