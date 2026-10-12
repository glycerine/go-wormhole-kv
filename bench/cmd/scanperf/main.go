// Scan profiling uses the same native uint64 fixture, seed-2023 trace, and
// adapter loops as make bench. Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/glycerine/insdict"
	"github.com/glycerine/wormhole/go/internal/cpuaffinity"
	"github.com/tidwall/btree"
)

type scanner interface {
	Put(uint64, uint64)
	Scan(uint64, int, func(uint64, uint64) bool)
	Iterate(bool, func(uint64, uint64) bool)
}

type dictScan struct{ *insdict.Dict[uint64, uint64] }

func (m dictScan) Put(k, v uint64) { m.Dict.Put(k, v) }
func (m dictScan) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	if length <= 0 {
		return
	}
	m.Ascend(start)(func(k, v uint64) bool {
		length--
		return visit(k, v) && length > 0
	})
}

func (m dictScan) Iterate(ordered bool, visit func(uint64, uint64) bool) {
	if ordered {
		m.Ascend()(visit)
	} else {
		m.All()(visit)
	}
}

type treeScan struct{ tree btree.Map[uint64, uint64] }

func (m *treeScan) Put(k, v uint64) { m.tree.Set(k, v) }
func (m *treeScan) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	if length <= 0 {
		return
	}
	m.tree.Ascend(start, func(k, v uint64) bool {
		length--
		return visit(k, v) && length > 0
	})
}

func (m *treeScan) Iterate(_ bool, visit func(uint64, uint64) bool) {
	m.tree.Scan(visit)
}

// Match the full-traversal benchmark's API and loop shape: insdict's range
// iterator can inline its yield, while Tidwall takes a callback.
func fullScans(m scanner, ordered bool, scans int) (sum, visited uint64) {
	switch m := m.(type) {
	case dictScan:
		if ordered {
			return ascendScans(m, scans)
		}
		return allScans(m, scans)
	case *treeScan:
		return treeScans(m, scans)
	default:
		panic("unsupported full traversal container")
	}
}

// Separate functions keep callback captures in the ordered scans from forcing
// the unordered range loop's accumulators onto the heap.
func allScans(m dictScan, scans int) (sum, visited uint64) {
	for i := 0; i < scans; i++ {
		for _, v := range m.All() {
			sum += v
			visited++
		}
	}
	return
}
func ascendScans(m dictScan, scans int) (sum, visited uint64) {
	for i := 0; i < scans; i++ {
		for _, v := range m.Ascend() {
			sum += v
			visited++
		}
	}
	return
}
func treeScans(m *treeScan, scans int) (sum, visited uint64) {
	visit := func(k, v uint64) bool { sum += v; visited++; return true }
	for i := 0; i < scans; i++ {
		m.tree.Scan(visit)
	}
	return
}

func key(rank int) uint64 {
	const mask = uint64(1<<63 - 1)
	x := uint64(rank) & mask
	x ^= x >> 30
	x = x * 0x3f58476d1ce4e5b9 & mask
	x ^= x >> 27
	x = x * 0x14d049bb133111eb & mask
	x ^= x >> 31
	return x << 1
}

type op struct {
	key    uint64
	length int
}

func trace(n, maximum int) []op {
	rng := rand.New(rand.NewSource(2023))
	ops := make([]op, 8192)
	for i := range ops {
		rank := rng.Intn(n)
		rng.Intn(100)
		ops[i] = op{key(rank), rng.Intn(maximum + 1)}
	}
	return ops
}

// perf stat/record --delay=-1 --control=fifo:CONTROL,ACK acknowledges each
// transition, excluding fixture loading, lazy sorting, and output from counts.
func control(path, ackPath string) (func(string) error, func(), error) {
	if path == "" && ackPath == "" {
		return func(string) error { return nil }, func() {}, nil
	}
	if path == "" || ackPath == "" {
		return nil, nil, fmt.Errorf("both control and ack paths are required")
	}
	w, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	r, err := os.Open(ackPath)
	if err != nil {
		w.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(r)
	return func(command string) error {
		if _, err := fmt.Fprintln(w, command); err != nil {
			return err
		}
		ack, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimLeft(ack, "\x00") != "ack\n" {
			return fmt.Errorf("unexpected perf acknowledgment %q", ack)
		}
		return nil
	}, func() { w.Close(); r.Close() }, nil
}

func run() error {
	mode := flag.String("mode", "ordered", "ordered (seek/limit), full (key order), or unordered (insdict All)")
	container := flag.String("container", "insdict", "insdict or tidwall")
	n := flag.Int("n", 65536, "initial population (make bench default: 65536)")
	scans := flag.Int("scans", 20000, "number of measured scans")
	maximum := flag.Int("maximum", 100000, "maximum scan length (make bench summary: 100000)")
	cpu := flag.Int("cpu", 6, "verified CPU pin")
	ctl := flag.String("control", "", "perf control FIFO")
	ack := flag.String("ack", "", "perf acknowledgment FIFO")
	ordered := flag.Bool("ordered-inserts", false, "diagnostic: insert the same key/value pairs in key order")
	flag.Parse()
	if *n < 1 || *scans < 1 || *maximum < 1 {
		return fmt.Errorf("n, scans and maximum must be positive")
	}
	if *mode != "ordered" && *mode != "full" && *mode != "unordered" {
		return fmt.Errorf("unknown mode %q", *mode)
	}
	if *mode == "unordered" && *container != "insdict" {
		return fmt.Errorf("unordered mode requires insdict")
	}
	runtime.GOMAXPROCS(1)
	restore, err := cpuaffinity.Pin(*cpu)
	if err != nil {
		return err
	}
	defer restore()
	var m scanner
	switch *container {
	case "insdict":
		m = dictScan{insdict.NewDictSize[uint64, uint64](*n)}
	case "tidwall":
		m = &treeScan{}
	default:
		return fmt.Errorf("unknown container %q", *container)
	}
	if *ordered {
		ranks := make([]int, *n)
		for i := range ranks {
			ranks[i] = i
		}
		slices.SortFunc(ranks, func(a, b int) int {
			ka, kb := key(a), key(b)
			if ka < kb {
				return -1
			}
			if ka > kb {
				return 1
			}
			return 0
		})
		for _, i := range ranks {
			m.Put(key(i), uint64(i))
		}
	} else {
		for i := 0; i < *n; i++ {
			m.Put(key(i), uint64(i))
		}
	}
	ops := trace(*n, *maximum)
	// Make the sorted index materialization explicit and keep it out of the
	// steady-state comparison. make bench amortizes this first scan over b.N.
	prepareStart := time.Now()
	var prepared int
	warm := func(k, v uint64) bool { prepared++; return true }
	if *mode == "ordered" {
		m.Scan(0, *n, warm)
	} else {
		m.Iterate(*mode == "full", warm)
	}
	prepare := time.Since(prepareStart)
	if prepared != *n {
		return fmt.Errorf("warm scan visited %d, want %d", prepared, *n)
	}
	signal, closeControl, err := control(*ctl, *ack)
	if err != nil {
		return err
	}
	defer closeControl()
	var sum, visited uint64
	visit := func(k, v uint64) bool { sum += v; visited++; return true }
	if err := signal("enable"); err != nil {
		return err
	}
	start := time.Now()
	if *mode == "ordered" {
		for i := 0; i < *scans; i++ {
			o := ops[i%len(ops)]
			m.Scan(o.key, o.length, visit)
		}
	} else {
		sum, visited = fullScans(m, *mode == "full", *scans)
	}
	elapsed := time.Since(start)
	if err := signal("disable"); err != nil {
		return err
	}
	runtime.KeepAlive(m)
	return json.NewEncoder(os.Stdout).Encode(struct {
		Container string  `json:"container"`
		Mode      string  `json:"mode"`
		CPU       int     `json:"cpu"`
		N         int     `json:"n"`
		Scans     int     `json:"scans"`
		Ordered   bool    `json:"ordered_inserts"`
		Visited   uint64  `json:"visited"`
		Sum       uint64  `json:"sum"`
		PrepareNS int64   `json:"prepare_ns"`
		ElapsedNS int64   `json:"elapsed_ns"`
		NSPerKey  float64 `json:"ns_per_key"`
	}{*container, *mode, *cpu, *n, *scans, *ordered, visited, sum, prepare.Nanoseconds(), elapsed.Nanoseconds(), float64(elapsed.Nanoseconds()) / float64(visited)})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
