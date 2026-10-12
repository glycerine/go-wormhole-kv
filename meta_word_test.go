package wormhole

import "testing"

func TestMetadataShortWordCollisions(t *testing.T) {
	// Put every candidate in the same two buckets with the same tag: the
	// comparison itself must distinguish lengths and every binary byte.
	const hash = uint32(0x00ab0cde)
	keys := []string{"", "\x00", "\x00\x00", "\x00\x00\x00", "\x00\x00\x00\x00",
		"a", "a\x00", "a\x00\x00", "a\x00\x00\x00", "a\x00\x00\xff", "abcd", "abcde"}
	table := newMetaTable[string, string](16)
	want := make(map[string]*metadata[string, string])
	for _, key := range keys {
		m := &metadata[string, string]{key: key, hash: hash}
		table.put(m)
		want[key] = m
	}
	// Exercise bucket comparisons rather than the direct shallow references.
	table.root, table.short = nil, [256]*metadata[string, string]{}
	for _, key := range append(keys, "\xff", "b\x00", "a\xff\x00", "abce", "abcd\x00") {
		if got := table.get(key, hash); got != want[key] {
			t.Fatalf("get(%x)=%p want=%p", key, got, want[key])
		}
	}
}

func TestMetadataShortWordChildren(t *testing.T) {
	for length := 0; length <= 4; length++ {
		prefix := "a\x00\xffb"[:length]
		h := crcExtend(crcSeed, prefix)
		table := newMetaTable[string, string](16)
		want := make(map[byte]*metadata[string, string])
		for _, token := range []byte{0, 1, 127, 255} {
			m := &metadata[string, string]{key: prefix + string([]byte{token}), hash: crcByte(h, token)}
			table.put(m)
			want[token] = m
		}
		table.root, table.short = nil, [256]*metadata[string, string]{}
		for token, node := range want {
			if got := table.child(prefix, h, token); got != node {
				t.Fatalf("child(%x,%x)=%p want=%p", prefix, token, got, node)
			}
			if length > 0 {
				wrong := "\x00" + prefix[1:]
				if got := table.child(wrong, h, token); got != nil {
					t.Fatalf("wrong parent %x matched %x", wrong, got.key)
				}
			}
		}
	}
}
