// Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.

package bench

import (
	"cmp"
	"fmt"
	"testing"

	"github.com/glycerine/bufftree"
	wormhole "github.com/glycerine/go-wormhole-kv"
	"github.com/glycerine/rbtree"
	"github.com/tidwall/btree"
)

type nativeUint64Points interface {
	Get(uint64) uint64
	Put(uint64, uint64)
}

type nativeWormhole struct {
	store *wormhole.Map[uint64, uint64]
	ref   *wormhole.Ref[uint64, uint64]
}

func newNativeWormhole() *nativeWormhole {
	m := wormhole.NewWithOptions[uint64, uint64](wormhole.Options{MetaCache: *wormholeMetaCache})
	return &nativeWormhole{store: m, ref: m.Ref()}
}
func (m *nativeWormhole) Get(k uint64) uint64 { v, _ := m.ref.Get(k); return v }
func (m *nativeWormhole) Put(k, v uint64) {
	if err := m.ref.Put(k, v); err != nil {
		panic(err)
	}
}
func (m *nativeWormhole) Len() int { return int(m.store.Len()) }
func (m *nativeWormhole) Close()   { m.ref.Close() }
func (m *nativeWormhole) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	m.ref.Scan(start, length, visit)
}

type nativeUint64Map map[uint64]uint64

func (m nativeUint64Map) Get(k uint64) uint64 { return m[k] }
func (m nativeUint64Map) Len() int            { return len(m) }
func (m nativeUint64Map) Put(k, v uint64) {
	m[k] = v
}

// Use tidwall's generic ordered Map with its default degree (32), without
// copies or path hints. Discard Set's results to match the void Put contract.
type nativeTidwallMap struct{ tree btree.Map[uint64, uint64] }

func (m *nativeTidwallMap) Get(k uint64) uint64 {
	v, _ := m.tree.Get(k)
	return v
}
func (m *nativeTidwallMap) Put(k, v uint64) { m.tree.Set(k, v) }
func (m *nativeTidwallMap) Len() int        { return m.tree.Len() }
func (m *nativeTidwallMap) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	if length <= 0 {
		return
	}
	m.tree.Ascend(start, func(k, v uint64) bool {
		length--
		return visit(k, v) && length > 0
	})
}

type nativeKV struct{ key, value uint64 }

// Reuse a pointer-shaped query for rbtree's interface API, avoiding boxing
// allocations on reads and updates. An inserted item gets its own pointer.
type nativeRBTree struct {
	tree  *rbtree.Tree
	probe *nativeKV
}

func newNativeRBTree() *nativeRBTree {
	return &nativeRBTree{
		tree: rbtree.NewTree(func(a, b rbtree.Item) int {
			return cmp.Compare(a.(*nativeKV).key, b.(*nativeKV).key)
		}),
		probe: &nativeKV{},
	}
}
func (m *nativeRBTree) Get(k uint64) uint64 {
	m.probe.key = k
	if item := m.tree.Get(m.probe); item != nil {
		return item.(*nativeKV).value
	}
	return 0
}
func (m *nativeRBTree) Len() int { return m.tree.Len() }
func (m *nativeRBTree) Put(k, v uint64) {
	*m.probe = nativeKV{key: k, value: v}
	added, it := m.tree.InsertGetIt(m.probe)
	if added {
		m.probe = &nativeKV{}
		return
	}
	item := it.Item().(*nativeKV)
	item.value = v
}
func (m *nativeRBTree) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	if length <= 0 {
		return
	}
	m.probe.key = start
	for it := m.tree.FindGE(m.probe); !it.Limit() && length > 0; it = it.Next() {
		item := it.Item().(*nativeKV)
		length--
		if !visit(item.key, item.value) {
			return
		}
	}
}

// Compare identical uint64 data, uniform lookup/update traces, and unique
// scrambled insertions. Timed point loops use concrete container types.
// Map writes use assignment alone, matching Put's void return contract.

type nativePointLayout struct {
	name string
	make func() nativeUint64Points
}

// Match the native uint64 configurations and capacity hints in Turtle's suite.
func nativePointLayouts(n int) []nativePointLayout {
	return []nativePointLayout{
		{"Wormhole", func() nativeUint64Points { return newNativeWormhole() }},
		{"Turtle", func() nativeUint64Points { return newNativeTurtle() }},
		{"Tree", func() nativeUint64Points { return bufftree.NewBPTree[uint64, uint64](nil) }},
		{"GoMap", func() nativeUint64Points { return make(nativeUint64Map, n) }},
		{"Tidwall", func() nativeUint64Points { return &nativeTidwallMap{} }},
		{"RBTree", func() nativeUint64Points { return newNativeRBTree() }},
		{"Insdict", func() nativeUint64Points { return newComparisonInsdict[uint64, uint64](n) }},
	}
}

func loadNativePoints(layout nativePointLayout, n int) nativeUint64Points {
	idx := layout.make()
	for i := 0; i < n; i++ {
		idx.Put(nativeBenchKey(i), uint64(i))
	}
	return idx
}

func nativePointCases(n int) []comparisonBenchmark {
	var cases []comparisonBenchmark
	ops := nativeBenchTrace(n, 8192, 1)
	freshKeys := make([]uint64, n)
	for i := range freshKeys {
		freshKeys[i] = nativeBenchKey(i) | 1
	}

	cases = append(cases, comparisonBenchmark{"Wormhole/GetHit", func(b *testing.B) {
		idx := newNativeWormhole()
		defer idx.Close()
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})

	cases = append(cases, comparisonBenchmark{"Wormhole/GetMiss", func(b *testing.B) {
		idx := newNativeWormhole()
		defer idx.Close()
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key | 1
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})
	cases = append(cases, comparisonBenchmark{"Wormhole/FreshPut", func(b *testing.B) {
		benchmarkNativeWormholeFreshPut(b, n, freshKeys)
	}})

	cases = append(cases, comparisonBenchmark{"Turtle/GetHit", func(b *testing.B) {
		idx := newNativeTurtle()
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})

	cases = append(cases, comparisonBenchmark{"Turtle/GetMiss", func(b *testing.B) {
		idx := newNativeTurtle()
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key | 1
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})
	cases = append(cases, comparisonBenchmark{"Turtle/FreshPut", func(b *testing.B) {
		benchmarkNativeTurtleFreshPut(b, n, freshKeys)
	}})

	cases = append(cases, comparisonBenchmark{"Tree/GetHit", func(b *testing.B) {
		idx := bufftree.NewBPTree[uint64, uint64](nil)
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})

	cases = append(cases, comparisonBenchmark{"Tree/GetMiss", func(b *testing.B) {
		idx := bufftree.NewBPTree[uint64, uint64](nil)
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key | 1
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})
	cases = append(cases, comparisonBenchmark{"Tree/FreshPut", func(b *testing.B) {
		benchmarkNativeTreeFreshPut(b, n, freshKeys)
	}})

	cases = append(cases, comparisonBenchmark{"GoMap/GetHit", func(b *testing.B) {
		idx := make(nativeUint64Map, n)
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})

	cases = append(cases, comparisonBenchmark{"GoMap/GetMiss", func(b *testing.B) {
		idx := make(nativeUint64Map, n)
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key | 1
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})
	cases = append(cases, comparisonBenchmark{"GoMap/FreshPut", func(b *testing.B) {
		benchmarkNativeGoMapFreshPut(b, n, freshKeys)
	}})

	cases = append(cases, comparisonBenchmark{"Tidwall/GetHit", func(b *testing.B) {
		idx := &nativeTidwallMap{}
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})

	cases = append(cases, comparisonBenchmark{"Tidwall/GetMiss", func(b *testing.B) {
		idx := &nativeTidwallMap{}
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key | 1
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})
	cases = append(cases, comparisonBenchmark{"Tidwall/FreshPut", func(b *testing.B) {
		benchmarkNativeTidwallFreshPut(b, n, freshKeys)
	}})

	cases = append(cases, comparisonBenchmark{"RBTree/GetHit", func(b *testing.B) {
		idx := newNativeRBTree()
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})

	cases = append(cases, comparisonBenchmark{"RBTree/GetMiss", func(b *testing.B) {
		idx := newNativeRBTree()
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key | 1
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})
	cases = append(cases, comparisonBenchmark{"RBTree/FreshPut", func(b *testing.B) {
		benchmarkNativeRBTreeFreshPut(b, n, freshKeys)
	}})

	cases = append(cases, comparisonBenchmark{"Insdict/GetHit", func(b *testing.B) {
		idx := newComparisonInsdict[uint64, uint64](n)
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})

	cases = append(cases, comparisonBenchmark{"Insdict/GetMiss", func(b *testing.B) {
		idx := newComparisonInsdict[uint64, uint64](n)
		for i := 0; i < n; i++ {
			idx.Put(nativeBenchKey(i), uint64(i))
		}
		var sum uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := ops[i%len(ops)].key | 1
			sum += idx.Get(key)
		}
		b.StopTimer()
		benchSink = sum
	}})
	cases = append(cases, comparisonBenchmark{"Insdict/FreshPut", func(b *testing.B) {
		benchmarkNativeInsdictFreshPut(b, n, freshKeys)
	}})

	return pinComparisonCases(cases)
}

type nativeOrderedScan interface {
	Put(uint64, uint64)
	Scan(uint64, int, func(uint64, uint64) bool)
}

type nativeScanLayout struct {
	name string
	make func() nativeOrderedScan
}

// Scans compare ordered traversal. Full traversals compare the cost of visiting
// every value: Go map uses unspecified order, and
// tidwall/btree and rbtree use key order. All include the same sum and key counter.

func nativeIterationCases(n int) []comparisonBenchmark {
	var cases []comparisonBenchmark
	layouts := []nativeScanLayout{
		{"Wormhole", func() nativeOrderedScan { return newNativeWormhole() }},
		{"Turtle", func() nativeOrderedScan { return newNativeTurtle() }},
		{"Tree", func() nativeOrderedScan { return bufftree.NewBPTree[uint64, uint64](nil) }},
		{"Tidwall", func() nativeOrderedScan { return &nativeTidwallMap{} }},
		{"RBTree", func() nativeOrderedScan { return newNativeRBTree() }},
		{"Insdict", func() nativeOrderedScan { return newComparisonInsdict[uint64, uint64](n) }},
	}
	for _, layout := range layouts {
		for _, maximum := range []int{10000, 100000} {
			cases = append(cases, comparisonBenchmark{fmt.Sprintf("%s/Scan%d", layout.name, maximum), func(b *testing.B) {
				tr := layout.make()
				defer closeComparison(tr)
				for i := 0; i < n; i++ {
					tr.Put(nativeBenchKey(i), uint64(i))
				}
				ops := nativeBenchTrace(n, 8192, maximum)
				var sum, visited uint64
				visit := func(k, v uint64) bool { sum += v; visited++; return true }
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					o := ops[i%len(ops)]
					tr.Scan(o.key, o.length, visit)
				}
				b.StopTimer()
				benchSink = sum
				reportIteration(b, visited)
			}})
		}
	}
	cases = append(cases, comparisonBenchmark{"Wormhole/Iterate", func(b *testing.B) {
		m := newNativeWormhole()
		defer m.Close()
		for i := 0; i < n; i++ {
			m.Put(nativeBenchKey(i), uint64(i))
		}
		var sum, visited uint64
		visit := func(k, v uint64) bool { sum += v; visited++; return true }
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Scan(0, n, visit)
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Tree/Iterate", func(b *testing.B) {
		m := bufftree.NewBPTree[uint64, uint64](nil)
		for i := 0; i < n; i++ {
			m.Put(nativeBenchKey(i), uint64(i))
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, v := range m.All() {
				sum += v
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Turtle/Iterate", func(b *testing.B) {
		m := newNativeTurtle()
		for i := 0; i < n; i++ {
			m.Put(nativeBenchKey(i), uint64(i))
		}
		var sum, visited uint64
		visit := func(k, v uint64) bool { sum += v; visited++; return true }
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Scan(0, n, visit)
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"GoMap/Iterate", func(b *testing.B) {
		m := make(nativeUint64Map, n)
		for i := 0; i < n; i++ {
			m[nativeBenchKey(i)] = uint64(i)
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, v := range m {
				sum += v
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Tidwall/Iterate", func(b *testing.B) {
		var m btree.Map[uint64, uint64]
		for i := 0; i < n; i++ {
			m.Set(nativeBenchKey(i), uint64(i))
		}
		var sum, visited uint64
		visit := func(k, v uint64) bool { sum += v; visited++; return true }
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Scan(visit)
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"RBTree/Iterate", func(b *testing.B) {
		m := newNativeRBTree()
		for i := 0; i < n; i++ {
			m.Put(nativeBenchKey(i), uint64(i))
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for it := m.tree.Min(); !it.Limit(); it = it.Next() {
				sum += it.Item().(*nativeKV).value
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Insdict/Iterate", func(b *testing.B) {
		m := newComparisonInsdict[uint64, uint64](n)
		for i := 0; i < n; i++ {
			m.Put(nativeBenchKey(i), uint64(i))
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, v := range m.All() {
				sum += v
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	return pinComparisonCases(cases)
}
