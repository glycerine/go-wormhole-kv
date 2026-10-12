//go:build amd64 || arm64

package wormhole

// leafStoreRelease publishes all protected writes before releasing the lock.
// The matching state CAS/load operations acquire that publication. This uses
// the same release ordering as the native leaf lock; Go's public atomic Store
// emits a stronger, more expensive exchange on amd64. Race synchronization is
// supplied explicitly by the leaf lock, as for sync.RWMutex.
//
//go:noescape
func leafStoreRelease(state *uint32)
