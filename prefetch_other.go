//go:build !amd64 || race

package wormhole

import "unsafe"

func prefetchMetaBuckets(a, b unsafe.Pointer)           {}
func prefetchLeafSlot(tag *uint16, slot unsafe.Pointer) {}

func prefetchWriteKey(key *byte) {}

func prefetchScanLeaf(header, slots, data unsafe.Pointer, size uintptr) {}
