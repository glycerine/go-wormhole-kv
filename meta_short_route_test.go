package wormhole

import (
	"math/rand/v2"
	"testing"
)

func TestStringShortMetadataRouting(t *testing.T) {
	anchors := []string{"\x00", "\x00\x00", "\x00\xff", "\x01\x7f", "a", "a\x00", "a\xff", "b\x7f", "\xfe", "\xff", "\xff\x00", "\xff\xff"}
	queries := []string{""}
	for first := range 256 {
		queries = append(queries, string([]byte{byte(first)}))
		for _, second := range []byte{0, 1, 127, 128, 254, 255} {
			queries = append(queries, string([]byte{byte(first), second}))
			queries = append(queries, string([]byte{byte(first), second, 128}))
		}
	}
	for seed := uint64(0); seed < 2; seed++ {
		leaves := []*leaf[string, string]{{}}
		table := newMetaTable[string, string](1)
		table.put(newMetadata("", crcSeed, leaves[0]))
		rng := rand.New(rand.NewPCG(seed, 2033))
		check := func() {
			for _, key := range queries {
				// checkJump compares against independent ordered predecessor
				// search, while longestPrefix remains the general trie reference.
				checkJump(t, table, leaves, key)
			}
		}
		check()
		for _, i := range rng.Perm(len(anchors)) {
			leaves = insertTestAnchor(table, leaves, anchors[i])
			check()
		}
		for len(leaves) > 1 {
			leaves = removeTestAnchor(table, leaves, 1+rng.IntN(len(leaves)-1))
			check()
		}
		// A deeper anchor switches string routing back to optimistic search.
		leaves = insertTestAnchor(table, leaves, "a\x00\x80")
		check()
	}
}
