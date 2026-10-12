.PHONY: bench memory l1

BENCH_TIME ?= 100ms
BENCH_COUNT ?= 3
BENCH_CPU ?= -1
BENCH_KEY_TYPE ?= uint64
BENCH_META_CACHE ?= false
MEMORY_N ?= 100000
GOMAXPROCS ?= 1

# Train all containers equally; respect an explicit standard GOFLAGS -pgo choice.
# Tests need an explicit profile because Go auto-selection covers main packages.
bench:
	GOMAXPROCS=$(GOMAXPROCS) go -C bench test $(if $(findstring -pgo,$(shell go env GOFLAGS)),,-pgo=default.pgo) -v -run '^TestReadmeBenchmarkTable$$' -count=1 -benchtime='$(BENCH_TIME)' -timeout=15m -args -bench-table -bench-table-count='$(BENCH_COUNT)' -bench-cpu='$(BENCH_CPU)' -bench-key-type='$(BENCH_KEY_TYPE)' -wormhole-meta-cache='$(BENCH_META_CACHE)'

bench2:
	BENCH_META_CACHE=true make bench

memory:
	GOMAXPROCS=1 WORMHOLE_BENCH_N='$(MEMORY_N)' go -C bench test -v -run '^TestMemoryFootprint$$' -count=1 -timeout=5m -args -memory-table -bench-cpu='$(BENCH_CPU)' -wormhole-meta-cache='$(BENCH_META_CACHE)'

memory2:
	BENCH_META_CACHE=true make memory


# Isolated perf counters, including the unordered insdict All() control.
L1_CPU ?= 6
L1_COUNT ?= 1
L1_SCANS ?= 20000
L1_N ?= 65536
L1_RESULTS ?=

l1:
	python3 bench/l1.py --cpu='$(L1_CPU)' --count='$(L1_COUNT)' --scans='$(L1_SCANS)' --n='$(L1_N)' $(if $(L1_RESULTS),--output='$(L1_RESULTS)')
