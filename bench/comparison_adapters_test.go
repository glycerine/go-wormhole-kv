package bench

import (
	"flag"
	"fmt"
	"testing"

	turtle "github.com/glycerine/turtle/go"
	wormhole "github.com/glycerine/go-wormhole-kv"
	"github.com/glycerine/go-wormhole-kv/internal/cpuaffinity"
)

// All seven containers receive the same already-encoded strings. Neither these
// adapters nor their timed callers encode keys/values or decode scan results.
type benchTurtle struct{ store *turtle.Map[string, string] }

func newBenchTurtle() *benchTurtle {
	return &benchTurtle{store: turtle.NewMap[string, string](turtleOptions())}
}
func (m *benchTurtle) Get(k string) string { v, _ := m.store.Get(k); return v }
func (m *benchTurtle) Put(k, v string)     { m.store.Put(k, v) }
func (m *benchTurtle) Len() int            { return m.store.Len() }
func (m *benchTurtle) Scan(start string, length int, visit func(string, string) bool) {
	m.store.Scan(start, length, visit)
}

type benchWormhole struct {
	store *wormhole.Map[string, string]
	ref   *wormhole.Ref[string, string]
}

func newBenchWormhole() *benchWormhole {
	m := wormhole.NewWithOptions[string, string](wormhole.Options{MetaCache: *wormholeMetaCache})
	return &benchWormhole{store: m, ref: m.Ref()}
}
func (m *benchWormhole) Get(k string) string { v, _ := m.ref.Get(k); return v }
func (m *benchWormhole) Put(k, v string) {
	if err := m.ref.Put(k, v); err != nil {
		panic(err)
	}
}
func (m *benchWormhole) Len() int { return int(m.store.Len()) }
func (m *benchWormhole) Close()   { m.ref.Close() }
func (m *benchWormhole) Scan(start string, length int, visit func(string, string) bool) {
	m.ref.Scan(start, length, visit)
}

func closeComparison(idx any) {
	if c, ok := idx.(interface{ Close() }); ok {
		c.Close()
	}
}

var wormholeMetaCache = flag.Bool("wormhole-meta-cache", false, "enable Wormhole two-byte metadata caching and larger initial table reservation")

var comparisonBenchCPU = flag.Int("bench-cpu", -1, "CPU for comparison benchmarks (-1 selects the first allowed CPU)")

func comparisonCPU() (int, error) {
	cpus, err := cpuaffinity.CPUs()
	if err != nil {
		return 0, err
	}
	if *comparisonBenchCPU < 0 {
		return cpus[0], nil
	}
	for _, cpu := range cpus {
		if cpu == *comparisonBenchCPU {
			return cpu, nil
		}
	}
	return 0, fmt.Errorf("benchmark CPU %d is outside allowed CPUs %v", *comparisonBenchCPU, cpus)
}

// testing.Benchmark runs each loop on its own goroutine. Pin there, before
// fixture loading and ResetTimer, and restore on that same goroutine afterward.
func pinComparisonCases(cases []comparisonBenchmark) []comparisonBenchmark {
	for i := range cases {
		run := cases[i].run
		cases[i].run = func(b *testing.B) {
			cpu, err := comparisonCPU()
			if err != nil {
				b.Fatal(err)
			}
			restore, err := cpuaffinity.Pin(cpu)
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				b.StopTimer()
				if err := restore(); err != nil {
					b.Fatal(err)
				}
			}()
			run(b)
		}
	}
	return cases
}
