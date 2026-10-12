//go:build native

package wormhole

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func hexKey(key string) string {
	if key == "" {
		return "-"
	}
	return hex.EncodeToString([]byte(key))
}

func TestNativeMetadataDifferential(t *testing.T) {
	var input, want bytes.Buffer
	leaves := []*leaf[string, string]{{}}
	table := newMetaTable[string, string](4096)
	table.put(newMetadata("", crcSeed, leaves[0]))
	dump := func() {
		var nodes []*metadata[string, string]
		for _, bucket := range table.buckets {
			for _, m := range bucket {
				if m != nil {
					nodes = append(nodes, m)
				}
			}
		}
		slices.SortFunc(nodes, func(a, b *metadata[string, string]) int { return strings.Compare(a.key, b.key) })
		fmt.Fprintf(&want, "T %d %d\n", len(nodes), table.maxPrefix)
		for _, m := range nodes {
			fmt.Fprintf(&want, "M %s %s %s %s %d %d", hexKey(m.key), hexKey(m.left.anchor), hexKey(m.right.anchor), hexKey(m.lpath.anchor), m.children.min, m.children.max)
			for i := 0; i < 256; i++ {
				if m.children.has(byte(i)) {
					fmt.Fprintf(&want, " %d", i)
				}
			}
			want.WriteByte('\n')
		}
	}
	query := func(key string) {
		fmt.Fprintf(&input, "J %s\n", hexKey(key))
		_, n, h := table.longestPrefix(key)
		fmt.Fprintf(&want, "J %s %d %08x\n", hexKey(table.jump(key).anchor), n, h)
	}
	rng := rand.New(rand.NewPCG(77, 42))
	prefix := "common" + strings.Repeat("\x00", 40)
	anchors := []string{"\x00", "\x00\x00", "\x00\x00\x00", "\x00\xff", "a", "a\x00", "aa", "ab", "b", "bb", "\xff", "\xff\xff", prefix + "a", prefix + "a\x00", prefix + "b"}
	for _, i := range rng.Perm(len(anchors)) {
		key := anchors[i]
		fmt.Fprintf(&input, "I %s\n", hexKey(key))
		leaves = insertTestAnchor(table, leaves, key)
		dump()
		for _, a := range anchors {
			query(a)
			query(a + "\x00")
			query(a + "\xff")
		}
	}
	for step := 0; step < 600; step++ {
		if len(leaves) > 1 && (len(leaves) > 40 || rng.IntN(3) == 0) {
			i := 1 + rng.IntN(len(leaves)-1)
			fmt.Fprintf(&input, "D %s\n", hexKey(leaves[i].anchor))
			leaves = removeTestAnchor(table, leaves, i)
		} else {
			buf := make([]byte, 1+rng.IntN(12))
			for i := range buf {
				buf[i] = byte(rng.IntN(8))
			}
			key := string(buf)
			_, found := slices.BinarySearchFunc(leaves, key, func(l *leaf[string, string], k string) int { return strings.Compare(l.anchor, k) })
			if found {
				continue
			}
			fmt.Fprintf(&input, "I %s\n", hexKey(key))
			leaves = insertTestAnchor(table, leaves, key)
		}
		dump()
		query("")
		for _, l := range leaves {
			query(l.anchor)
			query(l.anchor + "\x00")
		}
		for j := 0; j < 6; j++ {
			buf := make([]byte, rng.IntN(16))
			for i := range buf {
				buf[i] = byte(rng.IntN(12))
			}
			query(string(buf))
		}
	}
	out := runNativeOracle(t, "meta_oracle.c", input.Bytes())
	compareNativeTrace(t, out, want.Bytes())
}
