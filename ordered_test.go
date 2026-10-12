package wormhole

import (
	"cmp"
	"math"
	"math/rand/v2"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"testing"
)

type namedInt int64
type namedUint uint64
type namedFloat float64
type namedString string

func exerciseOrdered[K cmp.Ordered](t *testing.T, keys []K) {
	t.Helper()
	m := New[K, []int]()
	r := m.Ref()
	defer r.Close()
	order := rand.New(rand.NewPCG(42, 19)).Perm(len(keys))
	for _, i := range order {
		if err := r.Put(keys[i], []int{i}); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC() // Routing scratch must never back a retained metadata anchor.
	for i, k := range keys {
		v, ok := r.Get(k)
		if !ok || len(v) != 1 || v[0] != i {
			t.Fatalf("Get(%v) = %v, %v", k, v, ok)
		}
		if !r.ProbeKey(PrepareKey(k)) {
			t.Fatalf("prepared key missing: %v", k)
		}
	}
	sorted := slices.Clone(keys)
	slices.SortFunc(sorted, orderedCompare[K])
	it := r.Iterator()
	it.Seek(sorted[0])
	for _, want := range sorted {
		k, _, ok := it.Next()
		if !ok || orderedCompare(k, want) != 0 {
			t.Fatalf("scan %v, %v; want %v", k, ok, want)
		}
	}
	if it.Valid() {
		t.Fatal("scan beyond last key")
	}
	for _, want := range sorted {
		it.Seek(want)
		k, _, ok := it.Peek()
		if !ok || orderedCompare(k, want) != 0 {
			t.Fatal("seek", want, k)
		}
	}
	it.Close()
	lo, hi := len(sorted)/4, len(sorted)*3/4
	if n := r.DeleteRange(sorted[lo], sorted[hi]); n != int64(hi-lo) {
		t.Fatal("range count", n)
	}
	for i, k := range sorted {
		if r.Probe(k) != (i < lo || i >= hi) {
			t.Fatal("range membership", i, k)
		}
	}
	if n := r.DeleteFrom(sorted[hi]); n != int64(len(sorted)-hi) {
		t.Fatal("delete from", n)
	}
	for _, k := range sorted[:lo] {
		if !r.Delete(k) {
			t.Fatal("delete", k)
		}
	}
	if m.Len() != 0 {
		t.Fatal("not empty", m.Len())
	}
}

func TestOrderedTypes(t *testing.T) {
	t.Run("signed", func(t *testing.T) {
		var keys []int64
		for i := -512; i < 512; i++ {
			keys = append(keys, int64(i))
		}
		keys = append(keys, math.MinInt64, math.MaxInt64)
		exerciseOrdered(t, keys)
	})
	t.Run("unsigned", func(t *testing.T) {
		var keys []uint64
		for i := 0; i < 1024; i++ {
			keys = append(keys, uint64(i)*0x9e3779b97f4a7c15)
		}
		keys = append(keys, math.MaxUint64)
		exerciseOrdered(t, keys)
	})
	t.Run("float64", func(t *testing.T) {
		var keys []float64
		for i := -512; i < 512; i++ {
			keys = append(keys, float64(i)/7)
		}
		keys = append(keys, math.Inf(-1), math.Inf(1), math.NaN(), math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64)
		exerciseOrdered(t, keys)
	})
	t.Run("float32", func(t *testing.T) {
		var keys []float32
		for i := -512; i < 512; i++ {
			keys = append(keys, float32(i)/7)
		}
		keys = append(keys, float32(math.Inf(-1)), float32(math.Inf(1)), float32(math.NaN()))
		exerciseOrdered(t, keys)
	})
	t.Run("named", func(t *testing.T) {
		exerciseOrdered(t, []namedInt{-10, -1, 0, 1, 10})
		exerciseOrdered(t, []namedUint{0, 1, 1 << 63, math.MaxUint64})
		exerciseOrdered(t, []namedFloat{namedFloat(math.Inf(-1)), -1, 0, 1, namedFloat(math.Inf(1)), namedFloat(math.NaN())})
		exerciseOrdered(t, []namedString{"", "a", "a\x00", "z"})
	})
	t.Run("widths", func(t *testing.T) {
		exerciseOrdered(t, []int{-10, 0, 10})
		exerciseOrdered(t, []int8{-128, -1, 0, 1, 127})
		exerciseOrdered(t, []int16{-32768, 0, 32767})
		exerciseOrdered(t, []int32{math.MinInt32, 0, math.MaxInt32})
		exerciseOrdered(t, []uint{0, 1, ^uint(0)})
		exerciseOrdered(t, []uint8{0, 128, 255})
		exerciseOrdered(t, []uint16{0, 32768, 65535})
		exerciseOrdered(t, []uint32{0, 1 << 31, math.MaxUint32})
		exerciseOrdered(t, []uintptr{0, 1, ^uintptr(0)})
	})
}

func TestNaNLastAndIdentity(t *testing.T) {
	m := New[float64, map[string]int]()
	r := m.Ref()
	defer r.Close()
	nan1 := math.Float64frombits(0x7ff0000000000001)
	nan2 := math.Float64frombits(0xfff8000000000042)
	for _, k := range []float64{nan1, nan2, math.NaN()} {
		if err := r.Put(k, map[string]int{"value": 42}); err != nil {
			t.Fatal(err)
		}
	}
	r.Put(math.Copysign(0, -1), map[string]int{"value": 1})
	r.Put(0, map[string]int{"value": 2})
	r.Put(math.Inf(1), map[string]int{"value": 3})
	if m.Len() != 3 {
		t.Fatal("NaNs and zeros must coalesce", m.Len())
	}
	if v, ok := r.Get(nan2); !ok || v["value"] != 42 {
		t.Fatal("NaN lookup", v, ok)
	}
	if err := r.Merge(nan1, func(v map[string]int, exists bool) (map[string]int, bool) {
		if !exists {
			t.Fatal("merge NaN missing")
		}
		return map[string]int{"value": 43}, true
	}); err != nil {
		t.Fatal(err)
	}
	r.Update(nan2, func(v map[string]int, exists bool) map[string]int { return map[string]int{"value": v["value"] + 1} })
	r.Inspect(nan1, func(v map[string]int, exists bool) {
		if !exists || v["value"] != 44 {
			t.Fatal("inspect", v, exists)
		}
	})
	it := r.Iterator()
	it.Seek(math.Inf(1))
	k, _, ok := it.Next()
	if !ok || !math.IsInf(k, 1) {
		t.Fatal("+Inf")
	}
	k, _, ok = it.Next()
	if !ok || !math.IsNaN(k) {
		t.Fatal("NaN must be last")
	}
	if it.Valid() {
		t.Fatal("after NaN")
	}
	it.Close()
	if r.DeleteRange(math.Inf(1), nan1) != 1 || !r.Probe(nan2) {
		t.Fatal("NaN exclusive upper bound")
	}
	if r.DeleteFrom(nan2) != 1 || r.Probe(nan1) {
		t.Fatal("delete NaN")
	}
	var zero Key[float64]
	if !r.ProbeKey(zero) {
		t.Fatal("zero prepared numeric key")
	}
	if err := r.PutKey(zero, map[string]int{"zero": 1}); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Get(math.Copysign(0, -1)); !reflect.DeepEqual(v, map[string]int{"zero": 1}) {
		t.Fatal("zero", v)
	}
}

func TestGenericConcurrentWrites(t *testing.T) {
	m := New[uint64, *uint64]()
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Go(func() {
			r := m.Ref()
			defer r.Close()
			for i := 0; i < 1000; i++ {
				key := uint64(i*4 + worker)
				value := key ^ 0xdeadbeef
				if err := r.Put(key, &value); err != nil {
					t.Error(err)
					return
				}
				if v, ok := r.Get(key); !ok || *v != value {
					t.Error("concurrent lookup", key)
					return
				}
			}
		})
	}
	wg.Wait()
	runtime.GC()
	if m.Len() != 4000 {
		t.Fatal("concurrent length", m.Len())
	}
	r := m.Ref()
	defer r.Close()
	for key := uint64(0); key < 4000; key++ {
		if v, ok := r.Get(key); !ok || *v != key^0xdeadbeef {
			t.Fatal("retained pointer", key)
		}
	}
}
