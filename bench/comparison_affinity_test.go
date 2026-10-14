package bench

import (
	"runtime"
	"testing"

	"github.com/glycerine/go-wormhole-kv/internal/cpuaffinity"
)

func TestComparisonWithoutAffinity(t *testing.T) {
	if _, err := cpuaffinity.CPUs(); err == nil || runtime.GOOS == "linux" {
		t.Skip("requires a platform without CPU affinity")
	}
	saved := *comparisonBenchCPU
	*comparisonBenchCPU = -1
	defer func() { *comparisonBenchCPU = saved }()
	ran := false
	cases := pinComparisonCases([]comparisonBenchmark{{name: "no-affinity", run: func(b *testing.B) {
		ran = true
		for b.Loop() {
		}
	}}})
	result := testing.Benchmark(cases[0].run)
	if !ran || result.N <= 0 {
		t.Fatalf("default benchmark did not run without affinity: %+v", result)
	}
}
