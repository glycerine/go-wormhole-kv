package wormhole

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func TestLeafSortedRuns(t *testing.T) {
	for _, mode := range []int{0, 1, 2} {
		mergeOnly := mode != 0
		name := "given a sorted prefix and dirty tail when synchronizing then order every key"
		if mergeOnly {
			name = "given two sorted runs when merging either direction then order every key"
		}
		if mode == 2 {
			name = "given cached key heads when merging either direction then order every key"
		}
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(13, 29))
			var l leaf[string, string]
			l.hs = new([leafCapacity]*record[string, string])
			for i := range l.hs {
				l.hs[i] = &record[string, string]{key: string([]byte{byte(i), 0, 255})}
			}
			for n := 0; n <= leafCapacity; n++ {
				for prefix := 0; prefix <= n; prefix++ {
					l.n, l.sorted = n, prefix
					for i := range l.ss {
						l.ss[i] = 255 // inactive cells must remain untouched
					}
					for i, rank := range rng.Perm(n) {
						l.ss[i] = uint8(rank)
					}
					slices.Sort(l.ss[:prefix])
					if mergeOnly {
						slices.Sort(l.ss[prefix:n])
						if mode == 2 {
							var keys [leafCapacity]string
							var heads [leafCapacity]uint64
							for i := 0; i < n; i++ {
								keys[i] = l.hs[i].key
								heads[i] = keyHead(keys[i])
							}
							l.mergeSortedCached(prefix, &keys, &heads)
						} else {
							l.mergeSorted(prefix)
						}
					} else {
						l.syncSorted()
						if l.sorted != n {
							t.Fatal("sorted count not updated")
						}
					}
					for i, slot := range l.ss {
						want := uint8(255)
						if i < n {
							want = uint8(i)
						}
						if slot != want {
							t.Fatalf("n=%d prefix=%d index=%d got=%d want=%d", n, prefix, i, slot, want)
						}
					}
				}
			}
		})
	}
}

func TestMergeSortedHeadsMatchesCached(t *testing.T) {
	rng := rand.New(rand.NewPCG(137, 139))
	for n := 2; n <= leafCapacity; n++ {
		for split := 1; split < n; split++ {
			var keys [leafCapacity]uint64
			var heads [leafCapacity]uint64
			for i := range n {
				keys[i] = uint64(rng.Uint64() | 1)
				heads[i] = keys[i]
			}
			permutation := rng.Perm(n)
			var cached, direct leaf[uint64, uint64]
			cached.n, direct.n = n, n
			for i, slot := range permutation {
				cached.ss[i] = uint8(slot)
			}
			slices.SortFunc(cached.ss[:split], func(a, b uint8) int { return cmp.Compare(heads[a], heads[b]) })
			slices.SortFunc(cached.ss[split:n], func(a, b uint8) int { return cmp.Compare(heads[a], heads[b]) })
			direct.ss = cached.ss
			cached.mergeSortedCached(split, &keys, &heads)
			direct.mergeSortedHeads(split, &heads)
			if cached.ss != direct.ss {
				t.Fatalf("n=%d split=%d direct merge differs", n, split)
			}
		}
	}
}

func TestLeafSortBinaryPrefixes(t *testing.T) {
	t.Run("given zero padding and long equal heads when sorting then preserve full bytewise order", func(t *testing.T) {
		keys := []string{"", "\x00", "\x00\x00", "\x00a", "a", "a\x00", "a\x00\x00", "a\x00b", "abcdefgh", "abcdefgh\x00", "abcdefgh\x00\x00", "abcdefgh\x00a", "abcdefghi", "abcdefgz", "\x7f", "\x80", "\xff", "\xff\x00", "\xff\xff"}
		want := slices.Clone(keys)
		slices.Sort(want)
		for _, a := range keys {
			for _, b := range keys {
				ha, hb := keyHead(a), keyHead(b)
				if ha < hb && strings.Compare(a, b) >= 0 || ha > hb && strings.Compare(a, b) <= 0 {
					t.Fatalf("head reverses ordering: %q %q", a, b)
				}
			}
		}
		rng := rand.New(rand.NewPCG(47, 61))
		for range 100 {
			var l leaf[string, string]
			for _, i := range rng.Perm(len(keys)) {
				l.insert(&record[string, string]{key: keys[i]}, keyTag(keys[i]))
			}
			l.syncSorted()
			for i, key := range want {
				if l.at(i).key != key {
					t.Fatalf("index=%d got=%q want=%q", i, l.at(i).key, key)
				}
			}
		}
	})
}

func TestLeafSortSharedHeads(t *testing.T) {
	t.Run("given shared head bytes with padding and prefix exceptions when sorting then retain full key order", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(83, 97))
		for _, fixture := range []struct {
			prefix  string
			outside []string
		}{
			{"shared\x00\xff", nil},
			{"shared\x00\xff", []string{"a"}},
			{"shared\x00\xff", []string{"z"}},
			{"shared\x00\xff", []string{""}},
			{strings.Repeat("x", 16), nil},
		} {
			keys := slices.Clone(fixture.outside)
			for _, suffix := range []string{"", "\x00", "\x00\x00", "\x00a", "a", "a\x00", "\x7f", "\x80", "\xff", "abcdefgh", "abcdefgh\x00"} {
				keys = append(keys, fixture.prefix+suffix)
			}
			want := slices.Clone(keys)
			slices.Sort(want)
			for range 100 {
				var l leaf[string, string]
				// Keep exceptions in the sorted prefix. The dirty tail alone
				// sharing eight bytes is insufficient for a cached-run merge.
				for _, key := range fixture.outside {
					l.insert(&record[string, string]{key: key}, keyTag(key))
				}
				for _, i := range rng.Perm(len(keys) - len(fixture.outside)) {
					key := keys[len(fixture.outside)+i]
					l.insert(&record[string, string]{key: key}, keyTag(key))
				}
				l.syncSorted()
				for i, key := range want {
					if l.at(i).key != key {
						t.Fatalf("prefix=%q outside=%q index=%d got=%q want=%q", fixture.prefix, fixture.outside, i, l.at(i).key, key)
					}
				}
			}
		}
	})
}

func TestLeafSortHeadBuckets(t *testing.T) {
	t.Run("given skewed head buckets and zero-padded ties when sorting dirty runs then match full string order", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(101, 103))
		for _, prefix := range []string{"", "abcdefgh", strings.Repeat("z", 16)} {
			for trial := range 100 {
				unique := map[string]bool{}
				keys := make([]string, 0, leafCapacity)
				for len(keys) < leafCapacity {
					data := make([]byte, rng.IntN(24))
					for i := range data {
						data[i] = byte(rng.IntN(4))
					}
					// Most keys share their first differing byte; others occupy far
					// apart buckets. Short keys and zero suffixes collide in heads.
					if len(data) > 0 && rng.IntN(8) == 0 {
						data[0] = byte(rng.IntN(256))
					}
					key := prefix + string(data)
					if !unique[key] {
						unique[key] = true
						keys = append(keys, key)
					}
				}
				want := slices.Clone(keys)
				slices.Sort(want)
				for _, dirty := range []int{31, 32, 33, 64, 96, 128} {
					var l leaf[string, string]
					for _, i := range rng.Perm(leafCapacity) {
						l.insert(&record[string, string]{key: keys[i]}, keyTag(keys[i]))
					}
					l.sorted = leafCapacity - dirty
					slices.SortFunc(l.ss[:l.sorted], func(a, b uint8) int { return strings.Compare(l.hs[a].key, l.hs[b].key) })
					if prefix == "" {
						l.syncSorted()
					} else {
						l.syncSortedOccupied()
					}
					for i, key := range want {
						if l.at(i).key != key {
							t.Fatalf("prefix=%q trial=%d dirty=%d index=%d got=%q want=%q", prefix, trial, dirty, i, l.at(i).key, key)
						}
					}
				}
			}
		}
	})
}
