// Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.

package bench

import (
	"flag"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"testing"
)

var readmeBenchEnabled = flag.Bool("bench-table", false, "rerun the README benchmarks and print both Markdown tables")
var readmeBenchCount = flag.Int("bench-table-count", 3, "number of samples per README benchmark; report the median")

type readmeBenchmarkRow struct {
	label string
	cases [7]string
}

var readmeBenchmarkRows = []readmeBenchmarkRow{
	{"Tree `Get`, hit", [7]string{"Points/Wormhole/GetHit", "Points/Turtle/GetHit", "Points/Tree/GetHit", "Points/Tidwall/GetHit", "Points/RBTree/GetHit", "Points/Insdict/GetHit", "Points/GoMap/GetHit"}},
	{"Tree `Get`, miss", [7]string{"Points/Wormhole/GetMiss", "Points/Turtle/GetMiss", "Points/Tree/GetMiss", "Points/Tidwall/GetMiss", "Points/RBTree/GetMiss", "Points/Insdict/GetMiss", "Points/GoMap/GetMiss"}},
	{"Tree `Put`, fresh key", [7]string{"Points/Wormhole/FreshPut", "Points/Turtle/FreshPut", "Points/Tree/FreshPut", "Points/Tidwall/FreshPut", "Points/RBTree/FreshPut", "Points/Insdict/FreshPut", "Points/GoMap/FreshPut"}},
	{"Ordered scan, maximum 10,000", [7]string{"Iteration/Wormhole/Scan10000", "Iteration/Turtle/Scan10000", "Iteration/Tree/Scan10000", "Iteration/Tidwall/Scan10000", "Iteration/RBTree/Scan10000", "Iteration/Insdict/Scan10000", ""}},
	{"Ordered scan, maximum 100,000", [7]string{"Iteration/Wormhole/Scan100000", "Iteration/Turtle/Scan100000", "Iteration/Tree/Scan100000", "Iteration/Tidwall/Scan100000", "Iteration/RBTree/Scan100000", "Iteration/Insdict/Scan100000", ""}},
	{"Full traversal", [7]string{"Iteration/Wormhole/Iterate", "Iteration/Turtle/Iterate", "Iteration/Tree/Iterate", "Iteration/Tidwall/Iterate", "Iteration/RBTree/Iterate", "Iteration/Insdict/Iterate", "Iteration/GoMap/Iterate"}},
}

func readmeBenchmarkNames() []string {
	var names []string
	seen := make(map[string]bool)
	for _, row := range readmeBenchmarkRows {
		for _, name := range row.cases {
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}

// TestReadmeBenchmarkTable is opt-in so normal correctness tests stay fast.
// make bench enables it and sets Go's standard -benchtime flag. Each case is
// the same timed loop used by BenchmarkComparePoints/BenchmarkCompareIteration.
func TestReadmeBenchmarkTable(t *testing.T) {
	if !*readmeBenchEnabled {
		t.Skip("run make bench to regenerate the README tables")
	}
	if *readmeBenchCount < 1 {
		t.Fatal("bench-table-count must be at least 1")
	}
	if err := validateComparisonKeyType(*comparisonKeyType); err != nil {
		t.Fatal(err)
	}
	n := benchLoadSize()
	if runtime.GOOS == "linux" {
		cpu, err := comparisonCPU()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("benchmark CPU (verified by each case): %d; GOMAXPROCS=%d", cpu, runtime.GOMAXPROCS(0))
	}
	cases := make(map[string]func(*testing.B))
	for _, bc := range tablePointCases(n, *comparisonKeyType) {
		cases["Points/"+bc.name] = bc.run
	}
	for _, bc := range tableIterationCases(n, *comparisonKeyType) {
		cases["Iteration/"+bc.name] = bc.run
	}
	names := readmeBenchmarkNames()
	results := make(map[string]float64, len(names))
	for i, name := range names {
		run, ok := cases[name]
		if !ok {
			t.Fatalf("missing benchmark %s", name)
		}
		t.Logf("[%d/%d] %s", i+1, len(names), name)
		samples := make([]float64, *readmeBenchCount)
		for j := range samples {
			result := testing.Benchmark(run)
			value, err := readmeBenchmarkTime(result, strings.HasPrefix(name, "Iteration/"))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			samples[j] = value
		}
		results[name] = readmeBenchmarkMedian(samples)
	}
	simple, detailed, err := formatReadmeBenchmarkTables(results)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("\n%s %s/%s; %d entries; medians of %d runs with -benchtime=%s\n\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, n, *readmeBenchCount, flag.Lookup("test.benchtime").Value)
	fmt.Printf("Wormhole MetaCache=%t\n\n", *wormholeMetaCache)
	fmt.Printf("Key/value types: %s\n\n", comparisonTypeDescription(*comparisonKeyType))
	fmt.Printf("Simplified table (ns/key):\n\n%s\nDetailed table (ns/key):\n\n%s\n", simple, detailed)
}

func readmeBenchmarkTime(result testing.BenchmarkResult, iteration bool) (float64, error) {
	if result.N <= 0 {
		return 0, fmt.Errorf("benchmark did not run")
	}
	// NsPerOp truncates to integer nanoseconds. Preserve sub-nanosecond
	// precision before formatting; traversal must use actual visited keys.
	value := float64(result.T.Nanoseconds()) / float64(result.N)
	if iteration {
		var ok bool
		value, ok = result.Extra["iter_ns/key"]
		if !ok {
			return 0, fmt.Errorf("benchmark did not report iter_ns/key")
		}
	}
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("invalid timing %v", value)
	}
	return value, nil
}

func readmeBenchmarkMedian(samples []float64) float64 {
	values := slices.Clone(samples)
	slices.Sort(values)
	mid := len(values) / 2
	if len(values)%2 == 0 {
		return (values[mid-1] + values[mid]) / 2
	}
	return values[mid]
}

func formatReadmeBenchmarkTables(results map[string]float64) (simple, detailed string, err error) {
	rows := make([][]string, len(readmeBenchmarkRows))
	for i, row := range readmeBenchmarkRows {
		cells := []string{row.label}
		for _, name := range row.cases {
			if name == "" {
				cells = append(cells, "N/A")
				continue
			}
			value, ok := results[name]
			if !ok {
				return "", "", fmt.Errorf("missing result for %s", name)
			}
			precision := 1
			if strings.HasPrefix(name, "Iteration/") {
				precision = 2
			}
			cells = append(cells, fmt.Sprintf("%.*f", precision, value))
		}
		rows[i] = cells
	}
	detailed = alignedBenchmarkTable([]string{"Operation (showing ns/key)", "Wormhole", "Turtle", "BP-Tree", "tidwall/btree", "red-blk tree", "insdict", "builtin map"}, rows)
	var shortRows [][]string
	for i, selected := range []int{0, 2, 4} {
		row := rows[selected]
		shortRows = append(shortRows, []string{[]string{"Get", "Put", "Ordered scan"}[i], row[1], row[2], row[3], row[4], row[5], row[6], row[7]})
	}
	simple = alignedBenchmarkTable([]string{"Operation (showing ns/key)", "Wormhole", "Turtle", "BP-Tree", "tidwall/btree", "red-blk tree", "insdict", "builtin map"}, shortRows)
	return simple, detailed, nil
}

// All table labels are ASCII, so byte widths match their plain-text columns.
func alignedBenchmarkTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = max(3, len(h))
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len(cell))
		}
	}
	var out strings.Builder
	writeRow := func(row []string, header bool) {
		out.WriteByte('|')
		for i, cell := range row {
			out.WriteByte(' ')
			if i == 0 || header {
				fmt.Fprintf(&out, "%-*s", widths[i], cell)
			} else {
				fmt.Fprintf(&out, "%*s", widths[i], cell)
			}
			out.WriteString(" |")
		}
		out.WriteByte('\n')
	}
	writeRow(headers, true)
	separator := make([]string, len(headers))
	for i, width := range widths {
		separator[i] = strings.Repeat("-", width)
		if i != 0 {
			separator[i] = strings.Repeat("-", width-1) + ":"
		}
	}
	writeRow(separator, true)
	for _, row := range rows {
		writeRow(row, false)
	}
	return out.String()
}
