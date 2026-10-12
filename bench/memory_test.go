//go:build linux

package bench

import (
	"bytes"
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/metrics"
	"slices"
	"strconv"
	"testing"

	"github.com/glycerine/go-wormhole-kv/internal/cpuaffinity"
)

const memoryWorkerEnv = "WORMHOLE_HEAP_WORKER_LAYOUT"
const memoryResultPrefix = "wormhole-heap-result: "

var memoryTable = flag.Bool("memory-table", false, "print ranked retained-heap and GC scan footprint")

type memoryResult struct {
	Layout    string
	Entries   int
	HeapBytes uint64
	ScanBytes int64
}

// TestMemoryFootprint measures retained heap after loading the same native
// uint64 stores, configurations, and capacity hints as the comparison suite.
// Each row uses a fresh process to isolate runtime state and prior containers.
// Run with make memory (100,000 entries), or set WORMHOLE_BENCH_N directly.
func TestMemoryFootprint(t *testing.T) {
	if testing.Short() || !*memoryTable {
		t.Skip("memory diagnostic")
	}
	n := benchLoadSize()
	cpu, err := comparisonCPU()
	if err != nil {
		t.Fatal(err)
	}
	if layout := os.Getenv(memoryWorkerEnv); layout != "" {
		runtime.GOMAXPROCS(1)
		restore, err := cpuaffinity.Pin(cpu)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := restore(); err != nil {
				t.Error(err)
			}
		}()
		result := measureContainerHeap(t, layout, n)
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s%s\n", memoryResultPrefix, data)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var results []memoryResult
	for _, layout := range nativePointLayouts(n) {
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestMemoryFootprint$", "-test.count=1", "-memory-table", "-bench-cpu="+strconv.Itoa(cpu), "-wormhole-meta-cache="+strconv.FormatBool(*wormholeMetaCache))
		cmd.Env = append(cmd.Environ(), memoryWorkerEnv+"="+layout.name)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", layout.name, err, output)
		}
		var result memoryResult
		found := false
		for line := range bytes.SplitSeq(output, []byte("\n")) {
			if data, ok := bytes.CutPrefix(line, []byte(memoryResultPrefix)); ok {
				if err := json.Unmarshal(data, &result); err != nil {
					t.Fatalf("%s: %v", layout.name, err)
				}
				found = true
				break
			}
		}
		if !found || result.Layout != layout.name || result.Entries != n || result.HeapBytes == 0 {
			t.Fatalf("%s: invalid heap measurement %+v\n%s", layout.name, result, output)
		}
		results = append(results, result)
	}
	slices.SortStableFunc(results, func(a, b memoryResult) int { return cmp.Compare(b.HeapBytes, a.HeapBytes) })
	labels := map[string]string{
		"Wormhole": "Wormhole",
		"Insdict":  "insdict.Dict",
		"Turtle":   "Turtle",
		"Tree":     "bufftree.BPTree",
		"GoMap":    "builtin Go map",
		"Tidwall":  "tidwall/btree.Map",
		"RBTree":   "glycerine/rbtree",
	}
	var rows [][]string
	for _, r := range results {
		rows = append(rows, []string{labels[r.Layout], fmt.Sprint(r.HeapBytes),
			fmt.Sprintf("%.2f", float64(r.HeapBytes)/(1<<20)),
			fmt.Sprintf("%.2f", float64(r.HeapBytes)/float64(n)), fmt.Sprint(r.ScanBytes)})
	}
	fmt.Printf("\n%s %s/%s; %d entries; uint64 keys and values; CPU %d pinned\n", runtime.Version(), runtime.GOOS, runtime.GOARCH, n, cpu)
	fmt.Printf("Wormhole MetaCache=%t\n", *wormholeMetaCache)
	fmt.Println("Retained heap: post-GC HeapAlloc minus pre-load baseline; each row uses a fresh process with GOMAXPROCS=1.")
	fmt.Println("Sorted largest to smallest; configurations and capacity hints match the native uint64 benchmark. Retained pools and pending writes count; no ordered scans or explicit flushes.")
	fmt.Println("extra GC pointer tracing load (bytes): change in /gc/scan/heap:bytes, not total heap consumption.")
	fmt.Printf("\n%s\n\n", alignedBenchmarkTable([]string{"Container", "Heap bytes", "MiB", "B/key", "extra GC pointer tracing load (bytes)"}, rows))
}

func measureContainerHeap(t *testing.T, name string, n int) memoryResult {
	t.Helper()
	var makeContainer func() nativeUint64Points
	for _, layout := range nativePointLayouts(n) {
		if layout.name == name {
			makeContainer = layout.make
			break
		}
	}
	if makeContainer == nil {
		t.Fatalf("unknown memory layout %q", name)
	}
	scanSample := []metrics.Sample{{Name: "/gc/scan/heap:bytes"}}
	scanBytes := func() uint64 { metrics.Read(scanSample); return scanSample[0].Value.Uint64() }
	// Warm both GC and metrics before the baseline. Never build lazy scan indexes
	// merely to count entries: that would measure a different container state.
	scanBytes()
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	scanBefore := scanBytes()
	container := makeContainer()
	defer closeComparison(container)
	for i := 0; i < n; i++ {
		container.Put(nativeBenchKey(i), uint64(i))
	}
	for i := 0; i < n; i++ {
		if value := container.Get(nativeBenchKey(i)); value != uint64(i) {
			t.Fatalf("%s: lookup %d returned %d", name, i, value)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	scanAfter := scanBytes()
	runtime.KeepAlive(container)
	if after.HeapAlloc <= before.HeapAlloc {
		t.Fatalf("%s: heap did not grow: before=%d after=%d", name, before.HeapAlloc, after.HeapAlloc)
	}
	return memoryResult{Layout: name, Entries: n, HeapBytes: after.HeapAlloc - before.HeapAlloc, ScanBytes: int64(scanAfter) - int64(scanBefore)}
}
