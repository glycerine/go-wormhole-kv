package wormhole

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func checkBitmap(t testing.TB, b *childBitmap, want *[256]bool) {
	t.Helper()
	lo, hi, count := noChild, noChild, 0
	for i, v := range want {
		if b.has(byte(i)) != v {
			t.Fatalf("bitmap membership at %d", i)
		}
		if v {
			lo = min(lo, i)
			hi = i
			count++
		}
	}
	if int(b.min) != lo || int(b.max) != hi {
		t.Fatalf("bitmap extrema (%d,%d), want (%d,%d)", b.min, b.max, lo, hi)
	}
	if (b.words != nil) != (count > 1) {
		t.Fatalf("bitmap allocation with %d children", count)
	}
	var less, above [257]int
	prev := noChild
	for i := 0; i <= noChild; i++ {
		less[i] = prev
		if i < noChild && want[i] {
			prev = i
		}
	}
	next := noChild
	for i := noChild; i >= 0; i-- {
		if i < noChild && want[i] {
			next = i
		}
		above[i] = next
	}
	for i := 0; i <= noChild; i++ {
		if got := b.less(i); got != less[i] {
			t.Fatalf("less(%d)=%d want %d", i, got, less[i])
		}
		if got := b.atOrAbove(i); got != above[i] {
			t.Fatalf("atOrAbove(%d)=%d want %d", i, got, above[i])
		}
	}
}

func TestChildBitmap(t *testing.T) {
	t.Run("given empty bitmap when adding and clearing every token then preserve boundaries and compact storage", func(t *testing.T) {
		b := emptyChildren()
		var want [256]bool
		checkBitmap(t, &b, &want)
		for i := 0; i < 256; i++ {
			b.set(byte(i))
			b.set(byte(i))
			want[i] = true
			checkBitmap(t, &b, &want)
		}
		for i := 0; i < 256; i++ {
			b.clear(byte(i))
			b.clear(byte(i))
			want[i] = false
			checkBitmap(t, &b, &want)
		}
		for i := 255; i >= 0; i-- {
			b.set(byte(i))
			want[i] = true
			checkBitmap(t, &b, &want)
		}
		for i := 255; i >= 0; i-- {
			b.clear(byte(i))
			want[i] = false
			checkBitmap(t, &b, &want)
		}
	})
	t.Run("given random child mutations when finding siblings then match boolean array", func(t *testing.T) {
		b := emptyChildren()
		var want [256]bool
		rng := rand.New(rand.NewPCG(42, 17))
		for i := 0; i < 1500; i++ {
			x := rng.IntN(256)
			if rng.IntN(2) == 0 {
				b.set(byte(x))
				want[x] = true
			} else {
				b.clear(byte(x))
				want[x] = false
			}
			checkBitmap(t, &b, &want)
		}
	})
}

func checkMetaTable(t testing.TB, table *metaTable[string, string], want map[string]*metadata[string, string]) {
	t.Helper()
	seen := map[*metadata[string, string]]bool{}
	for i, bucket := range table.buckets {
		empty := false
		for j, m := range bucket {
			if m == nil {
				empty = true
				if table.tags[i][j] != 0 {
					t.Fatal("vacant slot retains tag")
				}
				continue
			}
			if empty {
				t.Fatal("hole in dense metadata bucket")
			}
			if seen[m] {
				t.Fatal("duplicate metadata pointer")
			}
			seen[m] = true
			if want[m.key] != m {
				t.Fatalf("unexpected prefix %q", m.key)
			}
			if table.tags[i][j] != hashTag(m.hash) {
				t.Fatal("tag mismatch")
			}
			a, b := table.choices(m.hash)
			if uint32(i) != a && uint32(i) != b {
				t.Fatal("entry in wrong bucket")
			}
		}
	}
	if len(seen) != len(want) || table.n != len(want) {
		t.Fatalf("table entries=%d n=%d want=%d", len(seen), table.n, len(want))
	}
	for key, m := range want {
		if table.get(key, m.hash) != m || !table.peek(m.hash) {
			t.Fatalf("missing prefix %q", key)
		}
	}
}

func TestMetaHashTable(t *testing.T) {
	t.Run("given full buckets with reciprocal displaced entries when relocation revisits a bucket then fail without mutation", func(t *testing.T) {
		table := newMetaTable[string, string](8)
		want := map[string]*metadata[string, string]{}
		for _, bucket := range []int{2, 6} {
			for j := 0; j < 8; j++ {
				hash := uint32(0x02000006)
				if bucket == 6 {
					hash = 0x06000002
				}
				m := &metadata[string, string]{key: fmt.Sprintf("%d-%d", bucket, j), hash: hash}
				m.word = metaWord(m.key) // this fixture bypasses put's initialization
				table.tags[bucket][j], table.buckets[bucket][j] = hashTag(hash), m
				want[m.key] = m
				table.n++
			}
		}
		beforeTags, beforeBuckets := slices.Clone(table.tags), slices.Clone(table.buckets)
		if table.cuckoo(2, &metadata[string, string]{key: "new", hash: 0x02000006}, hashTag(0x02000006), 1, nil) {
			t.Fatal("relocated into full cycle")
		}
		if !slices.Equal(table.tags, beforeTags) || !slices.Equal(table.buckets, beforeBuckets) {
			t.Fatal("failed relocation mutated table")
		}
		checkMetaTable(t, table, want)
	})
	t.Run("given a tiny table when growing and deleting then retain all prefixes through cuckoo relocations", func(t *testing.T) {
		table := newMetaTable[string, string](1)
		want := map[string]*metadata[string, string]{}
		rng := rand.New(rand.NewPCG(1, 99))
		for i := 0; i < 4000; i++ {
			key := fmt.Sprintf("prefix-%08x-%d", rng.Uint32(), i)
			m := &metadata[string, string]{key: key, hash: crcExtend(crcSeed, key)}
			table.put(m)
			want[key] = m
			if i%137 == 0 {
				checkMetaTable(t, table, want)
			}
		}
		checkMetaTable(t, table, want)
		if len(table.tags) <= 1 {
			t.Fatal("table did not grow")
		}
		keys := make([]string, 0, len(want))
		for k := range want {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, i := range rng.Perm(len(keys)) {
			m := want[keys[i]]
			if !table.erase(m) || table.erase(m) {
				t.Fatal("erase result mismatch")
			}
			delete(want, m.key)
			if len(want)%137 == 0 {
				checkMetaTable(t, table, want)
			}
		}
		checkMetaTable(t, table, want)
	})
	t.Run("given matching tags and hash choices when comparing keys then reject false matches", func(t *testing.T) {
		table := newMetaTable[string, string](1)
		for _, key := range []string{"", "a", "aa", "\x00", "\xff"} {
			table.put(&metadata[string, string]{key: key, hash: crcSeed})
		}
		for _, key := range []string{"", "a", "aa", "\x00", "\xff"} {
			if m := table.get(key, crcSeed); m == nil || m.key != key {
				t.Fatalf("missing %q", key)
			}
		}
		if !table.peek(crcSeed) || table.get("absent", crcSeed) != nil {
			t.Fatal("tag accepted as full key match")
		}
	})
}

// This oracle constructs each prefix's descendants directly from the sorted
// anchor list. It does not repeat incremental split/merge update logic.
func checkMetaIndex(t testing.TB, table *metaTable[string, string], leaves []*leaf[string, string]) {
	t.Helper()
	want := map[string]*metadata[string, string]{}
	for _, l := range leaves {
		for n := 0; n <= len(l.anchor); n++ {
			key := l.anchor[:n]
			if _, ok := want[key]; ok {
				continue
			}
			m := &metadata[string, string]{key: key, hash: crcExtend(crcSeed, key), children: emptyChildren()}
			for _, descendant := range leaves {
				if !strings.HasPrefix(descendant.anchor, key) {
					continue
				}
				if m.left == nil {
					m.setLeft(descendant)
				}
				m.right = descendant
				if len(descendant.anchor) > n {
					m.children.set(descendant.anchor[n])
				}
			}
			want[key] = m
		}
	}
	actual := map[string]*metadata[string, string]{}
	for _, bucket := range table.buckets {
		for _, m := range bucket {
			if m == nil {
				break
			}
			w := want[m.key]
			if w == nil {
				t.Fatalf("stale metadata %q", m.key)
			}
			if m.hash != w.hash || m.left != w.left || m.right != w.right || m.lpath != w.lpath {
				t.Fatalf("metadata %q extrema/path mismatch: left=%q right=%q path=%q want left=%q right=%q path=%q", m.key,
					m.left.anchor, m.right.anchor, m.lpath.anchor, w.left.anchor, w.right.anchor, w.lpath.anchor)
			}
			var children [256]bool
			for i := range children {
				children[i] = w.children.has(byte(i))
			}
			checkBitmap(t, &m.children, &children)
			actual[m.key] = m
		}
	}
	if len(actual) != len(want) {
		t.Fatalf("prefix count=%d want %d", len(actual), len(want))
	}
	checkMetaTable(t, table, actual)
}

func insertTestAnchor(table *metaTable[string, string], leaves []*leaf[string, string], key string) []*leaf[string, string] {
	i, _ := slices.BinarySearchFunc(leaves, key, func(l *leaf[string, string], k string) int { return strings.Compare(l.anchor, k) })
	l := &leaf[string, string]{anchor: key, prev: leaves[i-1]}
	if i < len(leaves) {
		l.next = leaves[i]
		l.next.prev = l
	}
	l.prev.next = l
	leaves = slices.Insert(leaves, i, l)
	table.insertAnchor(l)
	return leaves
}

func removeTestAnchor(table *metaTable[string, string], leaves []*leaf[string, string], i int) []*leaf[string, string] {
	l := leaves[i]
	l.prev.next = l.next
	if l.next != nil {
		l.next.prev = l.prev
	}
	table.removeAnchor(l)
	return slices.Delete(leaves, i, i+1)
}

func checkJump(t testing.TB, table *metaTable[string, string], leaves []*leaf[string, string], key string) {
	t.Helper()
	i, found := slices.BinarySearchFunc(leaves, key, func(l *leaf[string, string], k string) int { return strings.Compare(l.anchor, k) })
	if !found {
		i--
	}
	if got := table.jump(key); got != leaves[i] {
		t.Fatalf("jump(%q)=%q want %q", key, got.anchor, leaves[i].anchor)
	}
	m, length, h := table.longestPrefix(key)
	wantLen := 0
	for _, l := range leaves {
		wantLen = max(wantLen, commonPrefix(l.anchor, key))
	}
	if length != wantLen || m.key != key[:wantLen] || h != crcExtend(crcSeed, key[:wantLen]) {
		t.Fatalf("longestPrefix(%q)=(%q,%d,%x) want length %d", key, m.key, length, h, wantLen)
	}
}

func TestMetaAnchors(t *testing.T) {
	t.Run("given binary and prefix anchors when inserting and removing then retain exact metadata and predecessor lookup", func(t *testing.T) {
		anchors := []string{"\x00", "\x00\x00", "\x00\x00\x00", "\x00\xff", "a", "a\x00", "aa", "ab", "b", "bb", "\xff", "\xff\xff"}
		for seed := uint64(0); seed < 6; seed++ {
			leaves := []*leaf[string, string]{{}}
			table := newMetaTable[string, string](1)
			table.put(newMetadata("", crcSeed, leaves[0]))
			rng := rand.New(rand.NewPCG(seed, 31))
			for _, i := range rng.Perm(len(anchors)) {
				leaves = insertTestAnchor(table, leaves, anchors[i])
				checkMetaIndex(t, table, leaves)
				for _, a := range append(anchors, "", "\x80", "az", "\xff\xff\xff") {
					checkJump(t, table, leaves, a)
				}
			}
			for len(leaves) > 1 {
				leaves = removeTestAnchor(table, leaves, 1+rng.IntN(len(leaves)-1))
				checkMetaIndex(t, table, leaves)
				for _, a := range append(anchors, "", "\x80", "az", "\xff\xff\xff") {
					checkJump(t, table, leaves, a)
				}
			}
		}
	})
}

func runMetaProperty(t testing.TB, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed^99))
	leaves := []*leaf[string, string]{{}}
	table := newMetaTable[string, string](2)
	table.put(newMetadata("", crcSeed, leaves[0]))
	for step := 0; step < steps; step++ {
		if len(leaves) > 1 && (len(leaves) > 40 || rng.IntN(3) == 0) {
			leaves = removeTestAnchor(table, leaves, 1+rng.IntN(len(leaves)-1))
		} else {
			buf := make([]byte, 1+rng.IntN(12))
			for i := range buf {
				buf[i] = byte(rng.IntN(8))
			}
			key := string(buf)
			_, found := slices.BinarySearchFunc(leaves, key, func(l *leaf[string, string], k string) int { return strings.Compare(l.anchor, k) })
			if !found {
				leaves = insertTestAnchor(table, leaves, key)
			}
		}
		if step%31 == 0 {
			checkMetaIndex(t, table, leaves)
		}
		for _, l := range leaves {
			checkJump(t, table, leaves, l.anchor)
			checkJump(t, table, leaves, l.anchor+"\x00")
		}
		for j := 0; j < 8; j++ {
			buf := make([]byte, rng.IntN(15))
			for i := range buf {
				buf[i] = byte(rng.IntN(12))
			}
			checkJump(t, table, leaves, string(buf))
		}
	}
	checkMetaIndex(t, table, leaves)
}

func TestMetaRandomized(t *testing.T) {
	for seed := uint64(0); seed < 12; seed++ {
		t.Run(fmt.Sprintf("given seed %d when splitting and merging anchor list then match rebuilt trie and sorted predecessor", seed), func(t *testing.T) { runMetaProperty(t, seed, 300) })
	}
}

func FuzzMetadata(f *testing.F) {
	for _, seed := range []uint64{0, 1, 42, 65535} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { runMetaProperty(t, seed, 100) })
}

func TestMetaOptimisticFallback(t *testing.T) {
	t.Run("given false positive prefix tags when optimistic LPM overshoots then retry with full comparisons", func(t *testing.T) {
		leaves := []*leaf[string, string]{{}}
		table := newMetaTable[string, string](1)
		table.put(newMetadata("", crcSeed, leaves[0]))
		leaves = insertTestAnchor(table, leaves, "aaaa")
		key := "abcd"
		// Force tag hits for missing prefixes without inserting those prefixes.
		// Both choices are bucket zero; the fake metadata keys fail exact match.
		for _, prefix := range []string{"ab", "abc", "abcd"} {
			table.put(&metadata[string, string]{key: "fake-" + prefix, hash: crcExtend(crcSeed, prefix)})
		}
		if !table.peek(crcExtend(crcSeed, "ab")) || table.get("ab", crcExtend(crcSeed, "ab")) != nil {
			t.Fatal("failed to create tag false positive")
		}
		checkJump(t, table, leaves, key)
	})
	t.Run("given false tags above every possible divergence when retrying then find the exact prefix through biased steps and bitmap advancement", func(t *testing.T) {
		const length = 96
		anchor := strings.Repeat("a", length)
		for common := 0; common < length; common++ {
			leaves := []*leaf[string, string]{{}}
			table := newMetaTable[string, string](64)
			table.put(newMetadata("", crcSeed, leaves[0]))
			leaves = insertTestAnchor(table, leaves, anchor)
			key := anchor[:common] + "x" + anchor[common+1:]
			for n := common + 1; n <= length; n++ {
				prefix := key[:n]
				table.put(&metadata[string, string]{key: "fake-" + prefix, hash: crcExtend(crcSeed, prefix)})
			}
			m, n, h := table.longestPrefix(key)
			if m == nil || n != common || m.key != key[:common] || h != crcExtend(crcSeed, key[:common]) {
				t.Fatalf("common=%d: metadata=%v len=%d hash=%08x", common, m, n, h)
			}
			checkJump(t, table, leaves, key)
		}
	})
}
