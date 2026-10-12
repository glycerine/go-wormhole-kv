package bench

import (
	"encoding/binary"
	"math"
	"slices"
	"testing"
)

// Both representations must describe the same logical keys and scan bounds.
func TestComparisonTraceRepresentations(t *testing.T) {
	for _, maximum := range []int{1, 10000, 100000} {
		native := nativeBenchTrace(257, 8192, maximum)
		encoded := benchTrace(257, 8192, maximum)
		for i, op := range native {
			if binary.BigEndian.Uint64([]byte(encoded[i].key)) != op.key ||
				binary.BigEndian.Uint64([]byte(encoded[i].miss)) != op.key|1 ||
				encoded[i].length != op.length {
				t.Fatalf("trace representations differ at %d", i)
			}
		}
	}
}

func TestNativeComparisonAdapters(t *testing.T) {
	const n = 257
	for _, layout := range nativePointLayouts(n) {
		t.Run(layout.name, func(t *testing.T) {
			idx := loadNativePoints(layout, n)
			for i := 0; i < n; i++ {
				key := nativeBenchKey(i)
				if idx.Get(key) != uint64(i) || idx.Get(key|1) != 0 {
					t.Fatal("loaded key or miss")
				}
				idx.Put(key, uint64(n+i))
				idx.Put(key|1, uint64(2*n+i))
				if idx.Get(key) != uint64(n+i) || idx.Get(key|1) != uint64(2*n+i) {
					t.Fatal("replacement or fresh key")
				}
			}
			if idx.(interface{ Len() int }).Len() != 2*n {
				t.Fatal("fresh Put must grow the container")
			}
			if layout.name == "GoMap" {
				if _, ok := idx.(nativeUint64Map); !ok {
					t.Fatal("default Go map must store native uint64 keys and values")
				}
			}
			key := nativeBenchKey(50)
			if a := testing.AllocsPerRun(100, func() { idx.Get(key); idx.Get(key | 1) }); a != 0 {
				t.Fatal("native Get allocates", a)
			}
			tr, ok := idx.(nativeOrderedScan)
			if !ok {
				return
			}
			// Include unsigned extremes, ordered beyond the original even keys.
			keys := make([]uint64, 0, 2*n+2)
			values := make(map[uint64]uint64)
			for i := 0; i < n; i++ {
				key := nativeBenchKey(i)
				values[key], values[key|1] = uint64(n+i), uint64(2*n+i)
			}
			for _, key := range []uint64{1 << 63, math.MaxUint64} {
				idx.Put(key, ^key)
				values[key] = ^key
			}
			for key := range values {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, start := range []uint64{0, 1, nativeBenchKey(10), 1 << 63, math.MaxUint64} {
				for _, length := range []int{0, 1, 100, 10000} {
					pos, _ := slices.BinarySearch(keys, start)
					want := keys[pos : pos+min(length, len(keys)-pos)]
					var got []uint64
					tr.Scan(start, length, func(k, v uint64) bool {
						if values[k] != v {
							t.Fatal("native scan value")
						}
						got = append(got, k)
						return true
					})
					if !slices.Equal(got, want) {
						t.Fatal("native scan order/bounds")
					}
				}
			}
			count := 0
			tr.Scan(0, len(keys), func(k, v uint64) bool { count++; return false })
			if count != 1 {
				t.Fatal("native scan early stop")
			}
		})
	}
}

func TestComparisonModesCoverTable(t *testing.T) {
	for _, kind := range []string{"uint64", "string"} {
		names := make(map[string]bool)
		for _, group := range []struct {
			prefix string
			cases  []comparisonBenchmark
		}{
			{"Points/", tablePointCases(2, kind)}, {"Iteration/", tableIterationCases(2, kind)},
		} {
			for _, bc := range group.cases {
				names[group.prefix+bc.name] = true
			}
		}
		for _, name := range readmeBenchmarkNames() {
			if !names[name] {
				t.Fatalf("%s mode lacks %s", kind, name)
			}
		}
	}
	if validateComparisonKeyType("bytes") == nil {
		t.Fatal("invalid representation must fail")
	}
}
