package wormhole

import (
	"fmt"
	"testing"
)

func TestShallowMetadataLifecycle(t *testing.T) {
	table := newMetaTable[string, string](1)
	keys := []string{""}
	for i := range 256 {
		keys = append(keys, string([]byte{byte(i)}))
	}
	verify := func() {
		t.Helper()
		uncached := *table
		uncached.root, uncached.short = nil, [256]*metadata[string, string]{}
		for _, key := range keys {
			h := crcExtend(crcSeed, key)
			// Wrong hashes exercise fallback rather than returning a node just
			// because its key indexes a populated shallow slot.
			for _, hash := range []uint32{h, h ^ 1, h ^ 0x10000, h ^ 0x01010101} {
				if got, want := table.get(key, hash), uncached.get(key, hash); got != want {
					t.Fatalf("key=%x hash=%08x got=%p want=%p", key, hash, got, want)
				}
			}
			if len(key) == 1 {
				if got, want := table.child("", crcSeed, key[0]), uncached.child("", crcSeed, key[0]); got != want {
					t.Fatalf("child=%x got=%p want=%p", key, got, want)
				}
			}
		}
	}
	verify()
	nodes := make([]*metadata[string, string], len(keys))
	for i, key := range keys {
		nodes[i] = &metadata[string, string]{key: key, hash: crcExtend(crcSeed, key)}
		table.put(nodes[i])
	}
	for i := range 4000 {
		key := fmt.Sprintf("long-prefix-%d", i)
		table.put(&metadata[string, string]{key: key, hash: crcExtend(crcSeed, key)})
	}
	if len(table.buckets) <= 1 {
		t.Fatal("fixture did not expand and relocate buckets")
	}
	verify()
	for _, node := range nodes {
		if !table.erase(node) || table.get(node.key, node.hash) != nil {
			t.Fatalf("erased shallow prefix still present: %x", node.key)
		}
		fresh := &metadata[string, string]{key: node.key, hash: node.hash}
		table.put(fresh)
		if table.erase(node) || table.get(node.key, node.hash) != fresh {
			t.Fatalf("retired pointer erased replacement: %x", node.key)
		}
	}
	verify()
}
