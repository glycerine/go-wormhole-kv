//go:build !amd64

package wormhole

import "unsafe"

func crcExtend(seed uint32, key string) uint32 { return crcGeneric(seed, key) }

func numericKeyTag(route *[8]byte) uint16 {
	return keyTag(unsafe.String(&route[0], len(route)))
}
