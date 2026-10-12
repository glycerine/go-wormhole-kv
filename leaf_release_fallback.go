//go:build !amd64 && !arm64

package wormhole

import "sync/atomic"

// The portable store provides at least the required release ordering.
func leafStoreRelease(state *uint32) { atomic.StoreUint32(state, 0) }
