package wormhole

import (
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"testing"
	"unsafe"
)

// Opt-in structural census and exact heap profile for the native benchmark's
// scrambled uint64 population. Profiling stays outside timed benchmarks.
func TestMemoryLayoutProfile(t *testing.T) {
	path := os.Getenv("WORMHOLE_MEMORY_PROFILE")
	if path == "" {
		t.Skip("set WORMHOLE_MEMORY_PROFILE to write a retained-heap profile")
	}
	n := 100000
	if s := os.Getenv("WORMHOLE_BENCH_N"); s != "" {
		var err error
		n, err = strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("invalid population %q", s)
		}
	}
	oldRate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = oldRate }()
	m := New[uint64, uint64]()
	r := m.Ref()
	defer r.Close()
	for i := 0; i < n; i++ {
		const mask = uint64(1<<63 - 1)
		x := uint64(i) & mask
		x ^= x >> 30
		x = x * 0x3f58476d1ce4e5b9 & mask
		x ^= x >> 27
		x = x * 0x14d049bb133111eb & mask
		x ^= x >> 31
		if err := r.Put(x<<1, uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	leaves, entries := 0, 0
	spans := make([]uint64, 0)
	bounded := [4]int{}
	bounds := [...]uint64{128, 256, 512, 1024}
	for l := m.first; l != nil; l = l.next {
		leaves++
		entries += l.n
		if l.n == 0 {
			continue
		}
		low, high := l.records[l.ss[0]].key, l.records[l.ss[0]].key
		for _, slot := range l.ss[1:l.n] {
			key := l.records[slot].key
			low, high = min(low, key), max(high, key)
		}
		span := high - low + 1
		spans = append(spans, span)
		for i, bound := range bounds {
			if span <= bound {
				bounded[i]++
			}
		}
	}
	slices.Sort(spans)
	t.Logf("entries=%d leaves=%d occupancy=%.2f%%; requested sizes: leaf=%d dense=%d metadata=%d",
		entries, leaves, 100*float64(entries)/float64(leaves*leafCapacity),
		unsafe.Sizeof(leaf[uint64, uint64]{}), unsafe.Sizeof(denseStorage[uint64, uint64]{}), unsafe.Sizeof(metadata[uint64, uint64]{}))
	t.Logf("numeric leaf key spans: median=%d; <=128:%d <=256:%d <=512:%d <=1024:%d (of %d nonempty leaves)",
		spans[len(spans)/2], bounded[0], bounded[1], bounded[2], bounded[3], len(spans))
	for i, table := range m.tables {
		secondPages, routePages := 0, 0
		if table.second != nil {
			for _, p := range table.second {
				if p != nil {
					secondPages++
				}
			}
		}
		for _, p := range table.routes.pages {
			if p != nil {
				routePages++
			}
		}
		t.Logf("table %d: prefixes=%d buckets=%d second pages=%d route pages=%d", i, table.n, len(table.buckets), secondPages, routePages)
	}
	runtime.GC()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.WriteHeapProfile(f); err != nil {
		t.Error(err)
	}
	if err := f.Close(); err != nil {
		t.Error(err)
	}
	runtime.KeepAlive(m)
}
