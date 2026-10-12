package wormhole

import (
	"cmp"
	"testing"
)

type splitNamedUint uint64

func TestNumericDenseSplitPreservesLeftSlots(t *testing.T) {
	t.Run("uint64", func(t *testing.T) { checkNumericSplitSlots(t, func(i int) uint64 { return uint64(i+1) * 17 }) })
	t.Run("named uint64", func(t *testing.T) {
		checkNumericSplitSlots(t, func(i int) splitNamedUint { return splitNamedUint(i+1) * 17 })
	})
	t.Run("signed", func(t *testing.T) { checkNumericSplitSlots(t, func(i int) int64 { return int64(i-64) * 17 }) })
	t.Run("float", func(t *testing.T) { checkNumericSplitSlots(t, func(i int) float64 { return float64(i-64) / 3 }) })
}

func checkNumericSplitSlots[K cmp.Ordered](t *testing.T, key func(int) K) {
	for _, middle := range []bool{false, true} {
		for added := 0; added <= leafCapacity; added++ {
			l := &leaf[K, *int]{dense: true, denseStorage: new(denseStorage[K, *int])}
			if middle {
				l.next = &leaf[K, *int]{anchor: "z"}
			}
			slots := map[K]int{}
			want := map[K]*int{}
			for j := 0; j <= leafCapacity; j++ {
				i := (j * 73) % (leafCapacity + 1)
				k := key(i)
				v := new(int)
				*v = i
				want[k] = v
				if i != added {
					l.insertDenseEntry(k, v, 42)
					slots[k] = l.match(k, 42)
				}
			}
			k := key(added)
			right := l.split(&record[K, *int]{key: k, value: want[k]}, 42)
			if right == nil || l.n+right.n != leafCapacity+1 {
				t.Fatal("invalid split")
			}
			seen := map[K]bool{}
			for _, part := range []*leaf[K, *int]{l, right} {
				if part.sorted != part.n {
					t.Fatal("dirty split")
				}
				var prev K
				for rank, slot := range part.ss[:part.n] {
					rec := &part.records[slot]
					if rank > 0 && prev >= rec.key {
						t.Fatalf("unordered split at added=%d", added)
					}
					prev = rec.key
					if seen[rec.key] || rec.value != want[rec.key] {
						t.Fatal("lost, duplicate or changed value")
					}
					seen[rec.key] = true
					if part.match(rec.key, 42) != int(slot) {
						t.Fatal("broken lookup index")
					}
					if part == l && rec.key != k && slots[rec.key] != int(slot) {
						t.Fatal("left record moved")
					}
				}
				for slot, tag := range part.tags {
					occupied := part.occupied[slot>>6]&(uint64(1)<<uint(slot&63)) != 0
					if occupied != (tag != 0) {
						t.Fatal("occupancy mismatch")
					}
					if !occupied && (part.records[slot].value != nil || part.records[slot].key != *new(K)) {
						t.Fatal("vacant slot retains moved references")
					}
				}
			}
			if len(seen) != len(want) {
				t.Fatal("missing records")
			}
		}
	}
}
