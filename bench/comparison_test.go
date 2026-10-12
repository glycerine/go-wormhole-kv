package bench

import (
	"math"
	"slices"
	"testing"
)

func TestComparisonPoints(t *testing.T) {
	const n = 257
	entries := comparisonData(n)
	for _, layout := range comparisonPointLayouts(n) {
		t.Run(layout.name, func(t *testing.T) {
			idx := loadComparisonPoints(layout, entries)
			defer closeComparison(idx)
			for _, entry := range entries {
				if got := idx.Get(entry.key); got != entry.value {
					t.Fatalf("loaded key: got %q want %q", got, entry.value)
				}
			}
			fresh := make([]benchPair, n)
			for i := range fresh {
				fresh[i] = benchPair{binaryKey(permutedKey(i) | 1), benchValue(n + i)}
				if idx.Get(fresh[i].key) != "" {
					t.Fatal("fresh key already present")
				}
				idx.Put(fresh[i].key, fresh[i].value)
			}
			for i, entry := range entries {
				idx.Put(entry.key, fresh[i].value)
				if idx.Get(entry.key) != fresh[i].value || idx.Get(fresh[i].key) != fresh[i].value {
					t.Fatal("replacement lost or fresh key overwritten")
				}
			}
			if got := idx.(interface{ Len() int }).Len(); got != 2*n {
				t.Fatalf("Len = %d want %d", got, 2*n)
			}
			hit, miss := entries[50].key, binaryKey(math.MaxUint64)
			if allocations := testing.AllocsPerRun(100, func() { idx.Get(hit); idx.Get(miss) }); allocations != 0 {
				t.Fatalf("pre-encoded lookup allocates: %v", allocations)
			}
		})
	}
}

func TestComparisonOrderedScans(t *testing.T) {
	for _, layout := range comparisonPointLayouts(1000) {
		t.Run(layout.name, func(t *testing.T) {
			idx := layout.make()
			defer closeComparison(idx)
			tr, ok := idx.(benchOrderedScan)
			if !ok {
				t.Skip("unordered container")
			}
			entries := comparisonData(1000)
			// Include the extremes and the sign-bit boundary to verify that
			// big-endian byte ordering agrees with unsigned integer ordering.
			for _, k := range []uint64{0, 1, 255, 256, 1 << 32, 1 << 63, math.MaxUint64} {
				entries = append(entries, benchPair{binaryKey(k), binaryKey(^k)})
			}
			values := make(map[string]string)
			for _, entry := range entries {
				tr.Put(entry.key, entry.value)
				values[entry.key] = entry.value
			}
			keys := make([]string, 0, len(values))
			for k := range values {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			starts := []string{"", binaryKey(0), binaryKey(1), binaryKey(1 << 63), binaryKey(math.MaxUint64), binaryKey(math.MaxUint64) + "\x00"}
			for _, o := range benchTrace(1000, 100, 100) {
				starts = append(starts, o.key, o.miss)
			}
			for _, start := range starts {
				for _, length := range []int{0, 1, 100, 10000} {
					pos, _ := slices.BinarySearch(keys, start)
					want := keys[pos : pos+min(length, len(keys)-pos)]
					var got []string
					tr.Scan(start, length, func(k, v string) bool {
						if values[k] != v {
							t.Fatal("wrong scan value")
						}
						got = append(got, k)
						return true
					})
					if !slices.Equal(got, want) {
						t.Fatalf("wrong scan start=%x length=%d: got %d entries want %d", start, length, len(got), len(want))
					}
				}
			}
			count := 0
			tr.Scan("", len(keys), func(k, v string) bool { count++; return false })
			if count != 1 {
				t.Fatal("scan must honor early stop")
			}
			// Wormhole's scan must release its iterator before another Ref operation.
			idx.Put(binaryKey(42), binaryKey(43))
			if idx.Get(binaryKey(42)) != binaryKey(43) {
				t.Fatal("scan left container unusable")
			}
		})
	}
}

func TestComparisonTableCoverage(t *testing.T) {
	names := make(map[string]bool)
	for _, group := range []struct {
		prefix string
		cases  []comparisonBenchmark
	}{
		{"Points/", comparisonPointCases(2)}, {"Iteration/", comparisonIterationCases(2)},
	} {
		for _, bc := range group.cases {
			names[group.prefix+bc.name] = true
		}
	}
	for _, name := range readmeBenchmarkNames() {
		if !names[name] {
			t.Fatalf("table references missing benchmark %s", name)
		}
	}
}
