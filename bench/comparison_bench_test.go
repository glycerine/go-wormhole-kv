// Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.

package bench

import (
	"cmp"
	"fmt"
	"testing"

	"github.com/glycerine/bufftree"
	"github.com/glycerine/rbtree"
	"github.com/tidwall/btree"
)

type benchStringPoints interface {
	Get(string) string
	Put(string, string)
}

type benchStringMap map[string]string

func (m benchStringMap) Get(k string) string { return m[k] }
func (m benchStringMap) Len() int            { return len(m) }
func (m benchStringMap) Put(k, v string) {
	m[k] = v
}

// Use tidwall's generic ordered Map with its default degree (32), without
// copies or path hints. Discard Set's results to match the void Put contract.
type benchTidwallMap struct{ tree btree.Map[string, string] }

func (m *benchTidwallMap) Get(k string) string {
	v, _ := m.tree.Get(k)
	return v
}
func (m *benchTidwallMap) Put(k, v string) { m.tree.Set(k, v) }
func (m *benchTidwallMap) Len() int        { return m.tree.Len() }
func (m *benchTidwallMap) Scan(start string, length int, visit func(string, string) bool) {
	if length <= 0 {
		return
	}
	m.tree.Ascend(start, func(k, v string) bool {
		length--
		return visit(k, v) && length > 0
	})
}

type benchKV struct{ key, value string }

// Reuse a pointer-shaped query for rbtree's interface API, avoiding boxing
// allocations on reads and updates. An inserted item gets its own pointer.
type benchRBTree struct {
	tree  *rbtree.Tree
	probe *benchKV
}

func newBenchRBTree() *benchRBTree {
	return &benchRBTree{
		tree: rbtree.NewTree(func(a, b rbtree.Item) int {
			return cmp.Compare(a.(*benchKV).key, b.(*benchKV).key)
		}),
		probe: &benchKV{},
	}
}
func (m *benchRBTree) Get(k string) string {
	m.probe.key = k
	if item := m.tree.Get(m.probe); item != nil {
		return item.(*benchKV).value
	}
	return ""
}
func (m *benchRBTree) Len() int { return m.tree.Len() }
func (m *benchRBTree) Put(k, v string) {
	*m.probe = benchKV{key: k, value: v}
	added, it := m.tree.InsertGetIt(m.probe)
	if added {
		m.probe = &benchKV{}
		return
	}
	item := it.Item().(*benchKV)
	item.value = v
}
func (m *benchRBTree) Scan(start string, length int, visit func(string, string) bool) {
	if length <= 0 {
		return
	}
	m.probe.key = start
	for it := m.tree.FindGE(m.probe); !it.Limit() && length > 0; it = it.Next() {
		item := it.Item().(*benchKV)
		length--
		if !visit(item.key, item.value) {
			return
		}
	}
}

// All containers use native uint64 keys and values by default.
// The explicit string mode uses strings in every container.
// Both modes use uniform traces, fresh keys and ordinary API calls.
func BenchmarkComparePoints(b *testing.B) {
	for _, bc := range tablePointCases(benchLoadSize(), *comparisonKeyType) {
		b.Run(bc.name, bc.run)
	}
}

type comparisonBenchmark struct {
	name string
	run  func(*testing.B)
}

type benchPointLayout struct {
	name string
	make func() benchStringPoints
}

// Use the same configurations and capacity hints for each point workload.
func comparisonPointLayouts(n int) []benchPointLayout {
	return []benchPointLayout{
		{"Wormhole", func() benchStringPoints { return newBenchWormhole() }},
		{"Turtle", func() benchStringPoints { return newBenchTurtle() }},
		{"Tree", func() benchStringPoints { return bufftree.NewBPTree[string, string](nil) }},
		{"GoMap", func() benchStringPoints { return make(benchStringMap, n) }},
		{"Tidwall", func() benchStringPoints { return &benchTidwallMap{} }},
		{"RBTree", func() benchStringPoints { return newBenchRBTree() }},
		{"Insdict", func() benchStringPoints { return newComparisonInsdict[string, string](n) }},
	}
}

func loadComparisonPoints(layout benchPointLayout, entries []benchPair) benchStringPoints {
	idx := layout.make()
	for _, entry := range entries {
		idx.Put(entry.key, entry.value)
	}
	return idx
}

func comparisonPointCases(n int) []comparisonBenchmark {
	var cases []comparisonBenchmark
	entries := comparisonData(n)
	ops := benchTrace(n, 8192, 1)
	freshKeys := make([]string, n)
	freshValues := make([]string, n)
	for i := range freshKeys {
		freshKeys[i] = binaryKey(permutedKey(i) | 1)
		freshValues[i] = benchValue(n + i)
	}
	for _, layout := range comparisonPointLayouts(n) {
		for _, op := range []string{"GetHit", "GetMiss"} {
			cases = append(cases, comparisonBenchmark{layout.name + "/" + op, func(b *testing.B) {
				idx := loadComparisonPoints(layout, entries)
				defer closeComparison(idx)
				var sum uint64
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					o := &ops[i%len(ops)]
					key := o.key
					switch op {
					case "GetHit":
						sum += comparisonValueSink(idx.Get(key))
					case "GetMiss":
						sum += comparisonValueSink(idx.Get(o.miss))
					case "Update":
						value := o.value
						if (i/len(ops))&1 != 0 {
							value = o.alternate
						}
						idx.Put(key, value)
					}
				}
				b.StopTimer()
				benchSink = sum
			}})
		}
		cases = append(cases, comparisonBenchmark{layout.name + "/FreshPut", func(b *testing.B) {
			benchmarkFreshPut(b, layout, entries, freshKeys, freshValues)
		}})
	}
	return pinComparisonCases(cases)
}

type benchOrderedScan interface {
	Put(string, string)
	Scan(string, int, func(string, string) bool)
}

type benchScanLayout struct {
	name string
	make func() benchOrderedScan
}

// Scans compare ordered traversal. Full traversals compare the cost of visiting
// every value: Go map uses unspecified order, and
// tidwall/btree and rbtree use key order. All include the same sum and key counter.
func BenchmarkCompareIteration(b *testing.B) {
	for _, bc := range tableIterationCases(benchLoadSize(), *comparisonKeyType) {
		b.Run(bc.name, bc.run)
	}
}

func comparisonIterationCases(n int) []comparisonBenchmark {
	var cases []comparisonBenchmark
	entries := comparisonData(n)
	layouts := []benchScanLayout{
		{"Wormhole", func() benchOrderedScan { return newBenchWormhole() }},
		{"Turtle", func() benchOrderedScan { return newBenchTurtle() }},
		{"Tree", func() benchOrderedScan { return bufftree.NewBPTree[string, string](nil) }},
		{"Tidwall", func() benchOrderedScan { return &benchTidwallMap{} }},
		{"RBTree", func() benchOrderedScan { return newBenchRBTree() }},
		{"Insdict", func() benchOrderedScan { return newComparisonInsdict[string, string](n) }},
	}
	for _, layout := range layouts {
		for _, maximum := range []int{10000, 100000} {
			cases = append(cases, comparisonBenchmark{fmt.Sprintf("%s/Scan%d", layout.name, maximum), func(b *testing.B) {
				tr := layout.make()
				defer closeComparison(tr)
				for _, entry := range entries {
					tr.Put(entry.key, entry.value)
				}
				ops := benchTrace(n, 8192, maximum)
				var sum, visited uint64
				visit := func(k, v string) bool { sum += comparisonValueSink(v); visited++; return true }
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					o := &ops[i%len(ops)]
					tr.Scan(o.key, o.length, visit)
				}
				b.StopTimer()
				benchSink = sum
				reportIteration(b, visited)
			}})
		}
	}
	cases = append(cases, comparisonBenchmark{"Tree/Iterate", func(b *testing.B) {
		m := bufftree.NewBPTree[string, string](nil)
		for _, entry := range entries {
			m.Put(entry.key, entry.value)
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, v := range m.All() {
				sum += comparisonValueSink(v)
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Wormhole/Iterate", func(b *testing.B) {
		m := newBenchWormhole()
		defer m.Close()
		for _, entry := range entries {
			m.Put(entry.key, entry.value)
		}
		var sum, visited uint64
		visit := func(k, v string) bool { sum += comparisonValueSink(v); visited++; return true }
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Scan("", n, visit)
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Turtle/Iterate", func(b *testing.B) {
		m := newBenchTurtle()
		for _, entry := range entries {
			m.Put(entry.key, entry.value)
		}
		var sum, visited uint64
		visit := func(k, v string) bool { sum += comparisonValueSink(v); visited++; return true }
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			m.Scan("", n, visit)
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"GoMap/Iterate", func(b *testing.B) {
		m := make(benchStringMap, n)
		for _, entry := range entries {
			m[entry.key] = entry.value
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, v := range m {
				sum += comparisonValueSink(v)
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Tidwall/Iterate", func(b *testing.B) {
		var m btree.Map[string, string]
		for _, entry := range entries {
			m.Set(entry.key, entry.value)
		}
		var sum, visited uint64
		visit := func(k, v string) bool { sum += comparisonValueSink(v); visited++; return true }
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
		m := newBenchRBTree()
		for _, entry := range entries {
			m.Put(entry.key, entry.value)
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for it := m.tree.Min(); !it.Limit(); it = it.Next() {
				sum += comparisonValueSink(it.Item().(*benchKV).value)
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	cases = append(cases, comparisonBenchmark{"Insdict/Iterate", func(b *testing.B) {
		m := newComparisonInsdict[string, string](n)
		for _, entry := range entries {
			m.Put(entry.key, entry.value)
		}
		var sum, visited uint64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, v := range m.All() {
				sum += comparisonValueSink(v)
				visited++
			}
		}
		b.StopTimer()
		benchSink = sum
		reportIteration(b, visited)
	}})
	return pinComparisonCases(cases)
}
