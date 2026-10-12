package main

import (
	"slices"
	"testing"

	"github.com/glycerine/insdict"
)

func TestScanAdapters(t *testing.T) {
	const n = 257
	keys := make([]uint64, n)
	values := make(map[uint64]uint64, n)
	for i := range keys {
		keys[i] = key(i)
		values[keys[i]] = uint64(i)
	}
	slices.Sort(keys)
	for name, m := range map[string]scanner{
		"insdict": dictScan{insdict.NewDictSize[uint64, uint64](n)},
		"tidwall": &treeScan{},
	} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < n; i++ {
				m.Put(key(i), uint64(i))
			}
			for _, start := range []uint64{0, keys[80], keys[80] + 1, keys[n-1], ^uint64(0)} {
				for _, length := range []int{0, 1, 17, n + 1} {
					for _, stop := range []int{1, n + 1} {
						var want, got []uint64
						for _, k := range keys {
							if k >= start && len(want) < length && len(want) < stop {
								want = append(want, k)
							}
						}
						m.Scan(start, length, func(k, v uint64) bool {
							if values[k] != v {
								t.Errorf("key %d: value %d, want %d", k, v, values[k])
							}
							got = append(got, k)
							return len(got) < stop
						})
						if !slices.Equal(got, want) {
							t.Fatalf("start=%d length=%d stop=%d: got %v, want %v", start, length, stop, got, want)
						}
					}
				}
			}
		})
	}
}

func TestFullTraversal(t *testing.T) {
	for name, m := range map[string]scanner{
		"insdict": dictScan{insdict.NewDictSize[uint64, uint64](257)},
		"tidwall": &treeScan{},
	} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 257; i++ {
				m.Put(key(i), uint64(i))
			}
			for _, ordered := range []bool{false, true} {
				sum, count := fullScans(m, ordered, 3)
				if count != 257*3 || sum != 257*256/2*3 {
					t.Fatalf("fullScans: sum=%d visits=%d", sum, count)
				}
				seen := map[uint64]bool{}
				var previous uint64
				m.Iterate(ordered, func(k, v uint64) bool {
					if v >= 257 || k != key(int(v)) || seen[k] {
						t.Fatalf("invalid or duplicate pair (%d,%d)", k, v)
					}
					if ordered && len(seen) > 0 && k <= previous {
						t.Fatal("keys out of order")
					}
					previous = k
					seen[k] = true
					return true
				})
				if len(seen) != 257 {
					t.Fatalf("visited %d keys", len(seen))
				}
				visited := 0
				m.Iterate(ordered, func(k, v uint64) bool { visited++; return visited < 3 })
				if visited != 3 {
					t.Fatalf("early stop: got %d visits", visited)
				}
			}
		})
	}
}
