//go:build !amd64

package wormhole

func crcExtend(seed uint32, key string) uint32 { return crcGeneric(seed, key) }
