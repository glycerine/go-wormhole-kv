package wormhole

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestPackedBuildPlacement(t *testing.T) {
	for _, pattern := range []string{"random", "low collision", "high collision", "middle collision"} {
		t.Run(pattern, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(71, 89))
			var entries [leafCapacity]leafEntry[string, string]
			for i := range entries {
				tag := uint16(rng.IntN(65535) + 1)
				switch pattern {
				case "low collision":
					tag = 1
				case "high collision":
					tag = 65535
				case "middle collision":
					tag = 32768
				}
				entries[i] = leafEntry[string, string]{tag: tag, kv: &record[string, string]{key: fmt.Sprintf("%04d", i), value: "v"}}
			}
			for n := 0; n <= leafCapacity; n++ {
				var got, original leaf[string, string]
				want, tags := make(map[string]string), make(map[string]uint16)
				// This is the ordinary insertion path independently checked by
				// the C leaf oracle. The batch transformation must preserve its
				// exact placements, even with maximal collision clusters.
				for _, e := range entries[:n] {
					original.insert(e.kv, e.tag)
					want[e.kv.key], tags[e.kv.key] = e.kv.value, e.tag
				}
				got.buildSortedPacked(entries[:n])
				sameRecords := got.hs == nil && original.hs == nil || got.hs != nil && original.hs != nil && *got.hs == *original.hs
				if got.tags != original.tags || !sameRecords || !slices.Equal(got.ss[:n], original.ss[:n]) {
					t.Fatalf("n=%d: packed rebuilding changed magnet placement or sorted order", n)
				}
				checkLeaf(t, &got, want, tags)
			}
		})
	}
}
