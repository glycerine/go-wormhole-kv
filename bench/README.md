# Container comparison benchmarks

`make -C go bench` from the repository root (or `make bench` from `go/`)
compares Wormhole, Turtle, BP-tree, builtin Go map, `github.com/tidwall/btree`,
`github.com/glycerine/rbtree`, and `github.com/glycerine/insdict`.
**All seven containers store native uint64
keys and values by default**, matching `~/turtle/go && make bench`. Wormhole
now uses `Map[uint64,uint64]` directly. The optional
`BENCH_KEY_TYPE=string` control gives all seven containers pre-encoded eight-byte
strings. Encoding is outside timing in that mode.

The encoded-string results farther down are historical measurements or the
separate binary-string write suite. They do not describe the default generic
comparison. Generics also support string keys, but numeric keys and values are
stored in their native types; they are not converted to strings for storage.

The default population is 65,536 entries (`WORMHOLE_BENCH_N` overrides it).
Both modes retain the reversible even-key permutation and seed-2023 uniform
8,192-operation trace from bufftree/Turtle. Miss and fresh keys are disjoint odd
keys; tests verify native and encoded traces represent the same keys and scan
bounds. Write measurements use fresh insertions, growing from N to 2N records;
fixture reloading is outside timing. Replacements are excluded from comparison
benchmarks. String fixtures and fresh value arrays are prepared ahead of time
and reused across reloads.
Go map and insdict.Dict receive an initial capacity hint of N, matching the
source suite. Dict uses `NewDictSize`, `Get`, `Put`, and lazy `Ascend`; no
sorted index is built before scan timing or memory measurement. The Dict
adapter and full traversal follow the code in `~/bufftree/bench`.

Ordered scans start at the first key at or above the seek key and visit at most
the requested count. Scan ns/key divides by actual visited entries, including
short results near the end. Go map has no ordered scan. Full traversal requires visiting every entry, not
key order: Go map uses its native iteration and insdict uses `All()` (insertion
order). insdict ordered seek/limit scans still use `Ascend`. Wormhole uses `Ref.Scan`, a callback scan with leaf read locks and early stop.
The iterator remains available; its `Next` also advances directly now.
No explicit sorting or flushing is added ahead of timing; API lazy preparation
is included. All containers use their normal serial APIs; Wormhole retains its
normal concurrency safeguards.

`make bench` defaults to GOMAXPROCS=1 and medians of three 100 ms samples.
Each benchmark goroutine pins itself to a verified CPU before fixture loading,
then restores affinity after timing. `BENCH_CPU=-1` selects the first allowed
CPU; an explicit CPU must be inside the process's allowed affinity:

```sh
# Default: native uint64 keys and values in every container.
taskset -c 6 make -C go bench BENCH_CPU=6
# Optional control: string keys and values in every container.
taskset -c 6 make -C go bench BENCH_CPU=6 BENCH_KEY_TYPE=string
# Optional Wormhole metadata cache; competitors are unchanged.
taskset -c 6 make -C go bench BENCH_CPU=6 BENCH_META_CACHE=true
WORMHOLE_BENCH_N=1048576 make -C go bench BENCH_CPU=6 BENCH_TIME=300ms BENCH_COUNT=5
# Individual default-mode cases, including allocation statistics:
GOMAXPROCS=1 go -C go/bench test -run '^$' -bench '^BenchmarkCompare(Points|Iteration)$' -benchmem -args -bench-cpu=6
```

Ordinary tests skip the opt-in table run and check both modes' adapters,
fresh-key growth, replacement values, scan order/bounds/early stop,
allocation-free lookups, trace equivalence, table coverage and formatting.
Competitor dependencies stay in this separate benchmark module: BP-tree v0.0.3,
rbtree v0.2.2, Tidwall v1.8.1, Turtle v0.0.5, and insdict v0.14.6.
Turtle uses released generic `Map[K,V]`, with no local-checkout replacement.
Its defaults can be overridden
with `TURTLE_BENCH_MEMTABLE`, `TURTLE_BENCH_LEAF`, and `TURTLE_BENCH_FANOUT`.
Adapted source retains bufftree's MIT notice in `BUFFTREE_BENCH_LICENSE`.
The existing C and concurrent write throughput/latency suites remain below.

## Ordered scan hardware profiling

`make -C go l1` builds the standalone driver and reproduces the perf table,
with Tidwall full ordered scan first, followed by insdict sorted inserts
and unordered `All()`. Requires Linux, Go,
Python 3.9+, `taskset`, and standard `perf` with hardware-counter access.
Defaults: CPU 6, N=65,536, one pass, 20,000 seek/limit scans per sample,
and 8,000 full traversals per sample. All runs are sequential, pinned, and
validated for matching visit counts and value checksums within each workload.
Counter multiplexing or unavailable counters fail the command.

```sh
make -C go l1
make -C go l1 L1_CPU=6 L1_COUNT=3 L1_RESULTS=bench/results/my-l1-run
# Short smoke run (not useful for performance conclusions):
make -C go l1 L1_COUNT=1 L1_SCANS=20 L1_N=1024
```

`L1_N`, `L1_SCANS`, `L1_COUNT`, and `L1_CPU` override the defaults.
`L1_RESULTS` retains the table, perf counters, control logs, and driver JSON;
otherwise temporary files are removed. Full traversal repetitions are 40% of
`L1_SCANS` (at least one), keeping visited-entry totals close to the seek trace
at the default population.

[The six-row run](results/l1-2026-10-11/table.txt) measured insdict `All()` at
0.74 ns/key versus Tidwall full `Scan` at 2.39 ns/key, while insdict full
`Ascend()` took 4.79 ns/key. The previous comparison's insdict full-traversal
row incorrectly used `Ascend()`; it now uses `All()` in both native and string
modes, and the row is labeled “Full traversal”. Older saved benchmark tables
retain the original measurements. The locality explanation applies to ordered
insdict traversal; it does not imply a penalty for unordered `All()`.
The corrected native Go benchmark measured 0.670 ns/key for insdict versus
2.359 for Tidwall ([output](results/l1-2026-10-11/corrected-native.txt));
the string-mode medians were 1.117 versus 3.979
([output](results/l1-2026-10-11/corrected-string.txt)). The standalone driver
uses range iteration for insdict full traversal, matching the actual benchmark,
and a callback for Tidwall. Ordered seek/limit scans retain their adapter callbacks.
`All()` has a lower estimated hit percentage despite being faster: it performs
far fewer cache accesses per key, streams entries sequentially, and needs fewer
instructions. Hit percentage alone does not predict scan throughput.

`cmd/scanperf` is a standalone insdict/Tidwall driver using the native comparison's
same keys, values, seed-2023 trace, scan adapters, and checksum callback. It pins
to CPU 6 by default with GOMAXPROCS=1. Fixture loading and one full warm scan
(including insdict's lazy index preparation) precede measurement. Optional perf
control/ack FIFOs gate hardware counting to the scan loop. This reports steady
state; `make bench` includes and amortizes lazy preparation in its timed scans.

On the Threadripper 3960X, medians of three sequential runs per case were:

| Container / insertion order | ns/key | Estimated L1 hit rate | L1 misses/key | Instructions/key | IPC |
| --- | ---: | ---: | ---: | ---: | ---: |
| tidwall / benchmark order | 3.91 | 98.48% | 0.426 | 45.99 | 2.81 |
| insdict / benchmark order | 5.70 | 95.91% | 1.236 | 49.05 | 2.06 |
| insdict / key order (diagnostic) | 3.94 | 97.90% | 0.633 | 49.05 | 2.98 |

Each run performed exactly 513,940,036 visits with checksum 16,849,806,477,872.
All four counters ran together without multiplexing (100% running time).
[Raw counters](results/scanperf-2026-10-11/stat.txt) and
[insdict](results/scanperf-2026-10-11/insdict-profile.txt) /
[Tidwall](results/scanperf-2026-10-11/tidwall-profile.txt) cycle profiles are saved.
A separate run of the actual 100 ms Go benchmarks gave median 6.438 ns/key for
insdict and 3.990 for Tidwall ([output](results/scanperf-2026-10-11/go-benchmark.txt)).
The steady-state gap here is 1.46x; the short Go benchmark gap is 1.61x. These
runs do not reproduce a full 2x gap.

insdict `Ascend` walks an eight-byte sorted index, then uses each index to load
an insertion-order entry. Its uint64/uint64 entries occupy 32 bytes including
hash, live flag, and padding. The benchmark's scrambled insertion order makes
these entry loads nonsequential. Tidwall mostly streams contiguous 16-byte
key/value pairs inside B-tree nodes. insdict executes only about 7% more
instructions per key but incurs about 2.9x as many L1 misses per key and lower
IPC. Putting the same insdict key/value pairs into key order preserves the scan
trace and checksum, makes entry access sequential, and removes nearly all the
time gap. Together these measurements strongly implicate memory locality and
the dependent entry loads. About 81% of insdict's sampled cycles are in its
`Ascend` traversal closure. The diagnostic changes insertion order only; it is
not a proposed change to the benchmark workload.

The hit estimate is `100 * (1 - L1-dcache-load-misses / L1-dcache-loads)`.
On Zen 2, these generic perf aliases are not a literal fraction of retired loads:
the access counter includes load/store references, and the miss counter includes
prefetch traffic. Local `perf list --details` describes the underlying events as
`ls_dc_accesses` and `l2_cache_accesses_from_dc_misses`. Consequently the table
labels this as an estimate and also reports misses per actual visited key.
`perf mem -t load record` was unavailable on this host (`event '(null)' not supported`).

To reproduce one counter run from `go/bench` (repeat sequentially for medians):

```sh
go build -o /tmp/scanperf ./cmd/scanperf
work=$(mktemp -d)
mkfifo "$work/control" "$work/ack"
GOMAXPROCS=1 perf stat --delay=-1 \
  --control="fifo:$work/control,$work/ack" \
  -e '{cycles:u,instructions:u,L1-dcache-loads:u,L1-dcache-load-misses:u}' \
  -- taskset -c 6 /tmp/scanperf -cpu 6 -container insdict \
  -control "$work/control" -ack "$work/ack"
rm -r "$work"
```

Use `-container tidwall` for the comparison, or add `-ordered-inserts` to insdict
for the locality control. To collect a CPU profile, replace `perf stat` and its
`-e` argument with `perf record -o /tmp/scanperf.data -e cycles:u -F 997 --call-graph fp`,
keeping `--delay`, `--control`, and the driver arguments. Inspect with
`perf report -i /tmp/scanperf.data --stdio --no-children --sort symbol -n`.

## Retained memory and GC load

`make -C go memory` ranks all seven containers from largest retained heap to
smallest, with 100,000 native uint64 keys and values by default:

```sh
make -C go memory BENCH_CPU=6
make -C go memory BENCH_CPU=6 MEMORY_N=1048576
make -C go memory BENCH_CPU=6 BENCH_META_CACHE=true
```

Each row runs in a fresh process with GOMAXPROCS=1 and a verified CPU pin,
using the same scrambled keys, container configurations, and capacity hints
as the native comparison benchmarks. The table reports post-GC retained heap
minus the pre-load baseline, MiB, bytes/key, and the change in
`/gc/scan/heap:bytes`. The latter measures extra scannable heap for GC pointer
tracing, not GC time or total heap consumption. Retained pools and pending
writes count; ordered scans and explicit flushes are not run. These numbers
measure retained Go heap, rather than RSS or cumulative allocation traffic.
[Sample 100,000-entry output](results/memory-100000.txt) and
[sample million-entry output](results/memory-1048576.txt) use Turtle v0.0.5
and insdict v0.14.6.

`BENCH_KEY_TYPE` does not affect this native uint64 diagnostic. `BENCH_CPU=-1`
selects the first allowed CPU, just as for `make bench`.

The diagnostic is opt-in; ordinary tests skip its subprocess measurements.
From `go/bench`, run it directly with
`WORMHOLE_BENCH_N=100000 go test -v -run '^TestMemoryFootprint$' -count=1 -args -memory-table -bench-cpu=6`.

## Optional metadata cache configuration

`MetaCache` defaults to false. `New()` and zero-valued `Options` omit the
numeric two-byte metadata lookup directory and start hash tables at 64 buckets.
To enable that cache and reserve 4,096 initial buckets, use:

```go
m := wormhole.NewWithOptions[uint64, uint64](wormhole.Options{MetaCache: true})
```

Tables grow automatically in both modes. The separate numeric leaf-route
cache remains enabled. Options are copied during construction and cannot be
changed on a live Map.

`BENCH_META_CACHE=true` enables the option for Wormhole in either comparison
key mode and in the memory subprocesses. The output prints the chosen value.
The default is false. Current memory samples use the lower-memory default;
older cached and uncached measurements below retain their original labels.
The size and timing cost of the memory tradeoff depend on population and
workload.

## Current default configuration

The refreshed memory samples above use MetaCache=false. At 100,000 native
uint64 entries, the default retained 4,674,200 B (46.74 B/key), versus
6,463,440 B with [MetaCache=true](results/meta-cache-enabled-memory-100000.txt).
At 1,048,576 entries the default retained 41,584,848 B (39.66 B/key).

The current native table below uses insdict v0.14.6, Turtle v0.0.5, 65,536
initial entries, CPU 6 pinned, GOMAXPROCS=1, and three 100 ms samples. Put
means fresh insertions. [Raw results](results/meta-cache-default-table.txt).

| Operation (showing ns/key) | Wormhole | Turtle | BPTree | tidwall/btree | red-black tree | insdict.Dict | builtin Go map |
| -------------------------- | -------: | -----: | -----: | ------------: | -------------: | -----------: | -------------: |
| Get                        |     59.1 |  127.2 |   98.9 |         134.9 |          199.5 |         20.5 |           16.6 |
| Put                        |    140.5 |  127.1 |  132.0 |         162.6 |          369.4 |         63.2 |           78.4 |
| Ordered scan               |     2.47 |   2.81 |   3.03 |          3.87 |          15.90 |         6.30 |            N/A |

Cached [native](results/meta-cache-enabled-smoke.txt) and
[string](results/meta-cache-string-enabled-smoke.txt) smoke tables also passed
(one 100 ms sample each). Core/native oracle race tests, race tests in both
benchmark modes, and vet passed. A regression test checks that New() and
zero-valued Options both leave metadata caching off, while MetaCache=true
enables it. The [default heap profile census](results/meta-cache-default-profile.txt)
confirms zero two-byte metadata pages and retains the leaf-route pages.

## Earlier validation with the NoMetaCache API

The earlier inverted `NoMetaCache` field has been replaced by `MetaCache`.
In these archived logs, NoMetaCache=false means MetaCache=true, and
NoMetaCache=true means MetaCache=false:

Validated on CPU 6 with native uint64 keys/values and 100,000 entries:
NoMetaCache=false retained 6,484,720 B and NoMetaCache=true retained 4,679,520 B
(27.8% less). Extra GC-scannable heap was 3,135,760 B versus 1,458,152 B.
[Default memory](results/option-memory-default.txt) and
[optional memory](results/option-memory-no-cache.txt).
Full native tables passed with
[caching enabled](results/option-bench-default.txt) and
[caching disabled](results/option-bench-no-cache.txt), both three 100 ms samples.
The [string-mode smoke table](results/option-bench-string-no-cache.txt) used
one 100 ms sample. Core/native oracle race tests, both benchmark option modes'
race tests, and vet passed. Randomized mutation, routing equivalence, and
concurrent publication tests exercise both constructor configurations.

## Earlier restored-cache configuration

The two-byte metadata cache and original 4,096-bucket initial tables are
restored. Leaf pointers remain grouped before scalar arrays, reducing GC
scanning without changing the caches or leaf capacity. The sample memory
outputs in the linked restoration logs describe this configuration.

CPU 6 pinned, GOMAXPROCS=1, 65,536 native uint64 entries: five one-second
Get-hit samples have a median of 59.04 ns/key, versus 58.90 in the original
baseline. Lookups allocate nothing.
[Restored lookup results](results/cache-restored-get.txt).
Retained heap is back to 6,463,440 B at 100,000 keys and 43,102,104 B at
1,048,576 keys. Keeping the leaf pointer grouping reduces GC-scannable heap
from 3,558,504 to 3,126,760 B at 100K and from 10,605,072 to 5,503,192 B at
1M, relative to the original baseline. Race tests, native C oracle checks,
and native-tag vet passed after restoring the caches.

## Earlier seven-container comparison with caching enabled

With restored caches, insdict v0.14.6 and Turtle v0.0.5, 65,536 initial
native uint64 entries, CPU 6 pinned, GOMAXPROCS=1, medians of three 100 ms
samples. Put means fresh insertions. [Raw results](results/cache-restored-table.txt).
Use the longer Get-hit measurement above when assessing lookup performance.

| Operation (showing ns/key) | Wormhole | Turtle | BPTree | tidwall/btree | red-black tree | insdict.Dict | builtin Go map |
| -------------------------- | -------: | -----: | -----: | ------------: | -------------: | -----------: | -------------: |
| Get                        |     59.9 |  124.6 |  100.6 |         114.2 |          204.8 |         22.3 |           17.3 |
| Put                        |    141.2 |  122.7 |  110.1 |         153.4 |          361.2 |         54.4 |           74.4 |
| Ordered scan               |     2.41 |   2.75 |   3.18 |          3.89 |          15.29 |         6.20 |            N/A |

## Earlier cache-removal experiment (reverted)

The cache removal and smaller initial table reservation described below have
been reverted at the user's request. The two-byte metadata cache and initial
4,096-bucket tables are restored. The leaf pointer grouping is retained to
reduce GC scanning. Figures in this section describe the earlier experiment;
the linked cache-restoration logs describe that restored configuration.

The native uint64 memory change removes the duplicate two-byte metadata
pointer directory, retains the fast two-byte leaf-route cache, and starts
metadata hash tables at 64 buckets rather than 4,096. They grow through the
existing cuckoo-table expansion. Leaf pointers now precede the scalar arrays,
so GC stops scanning before those arrays. Leaf capacity, record placement,
locks, epochs, and the counter design are unchanged.

Pinned CPU 6, GOMAXPROCS=1, same scrambled population and post-GC diagnostic:

| Entries | Heap before | Heap after | Heap reduction | GC scan before | GC scan after | GC scan reduction |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100,000 | 6,463,440 B | 4,668,880 B | 27.8% | 3,558,504 B | 1,452,656 B | 59.2% |
| 1,048,576 | 43,102,120 B | 41,584,864 B | 3.5% | 10,605,072 B | 4,053,936 B | 61.8% |

At 100,000 keys Wormhole now uses 46.69 B/key, below both rbtree and Turtle.
The heap saving is smaller at a million keys because the removed directory
and initial hash-table reservation contribute less to the total.
[Before 100K](results/memory-before-100000.txt),
[before 1M](results/memory-before-1048576.txt), [experiment 100K](results/memory-cache-removal-100000.txt), and
[experiment 1M](results/memory-cache-removal-1048576.txt).
GC scan bytes describe pointer-tracing work, not measured GC CPU time.

Five one-second samples at 65,536 initial entries compare the baseline code
at 2fc0093 with the memory changes, using the same benchmark harness and
CPU pinning. Medians:

| Operation | Before (ns/key) | After (ns/key) |
| --- | ---: | ---: |
| Get, hit | 58.90 | 58.31 |
| Fresh Put | 138.2 | 140.0 |
| Ordered scan, maximum 100,000 | 2.479 | 2.470 |
| Full traversal | 2.482 | 2.471 |

The initial short samples suggested a 6% fresh-insert cost; the longer run
measured 1.3%, with no statistically clear fresh-insert difference in these
five samples. Get and traversal allocate nothing; reported fresh-insert
allocation traffic changed from 37 to 38 B/key despite the lower retained heap.
[Before](results/memory-perf-before-long.txt),
[after](results/memory-perf-after-long.txt), and
[benchstat](results/memory-perf-long-benchstat.txt).

Additional controls used three 300 ms samples, also pinned to CPU 6:

| Population / types | Operation | Before (ns/key) | After (ns/key) |
| --- | --- | ---: | ---: |
| 1,048,576 / uint64 | Get, hit | 89.63 | 84.86 |
| 1,048,576 / uint64 | Fresh Put | 325.3 | 326.6 |
| 1,048,576 / uint64 | Ordered scan, maximum 100,000 | 3.626 | 3.636 |
| 1,048,576 / uint64 | Full traversal | 4.143 | 4.262 |
| 65,536 / string | Get, hit | 101.3 | 103.2 |
| 65,536 / string | Fresh Put | 340.4 | 332.6 |
| 65,536 / string | Ordered scan, maximum 100,000 | 6.946 | 6.968 |
| 65,536 / string | Full traversal | 6.854 | 6.902 |

[Million-key baseline](results/memory-perf-baseline-million.txt),
[million-key candidate](results/memory-perf-candidate-million.txt),
[string baseline](results/memory-perf-baseline-string.txt), and
[string candidate](results/memory-perf-candidate-string.txt).
These serial microbenchmarks do not measure concurrent tail latency.
Core and benchmark module race tests and vet passed, including the existing
numeric routing, split/merge, concurrent publication, and scan checks.
The native C oracle suite also passed with `go test -race -tags=native ./...`,
and native-tag vet passed.

An exact heap profile and structural census found 1,025 leaves at 76.22%
occupancy. Each of the two metadata tables had only 1,026 prefixes, yet
reserved 4,096 buckets and two directories of 256 full pointer pages.
The duplicate metadata directory cost 1.125 MiB across both tables. After the
change each table needs 256 hash buckets. The remaining leaf-route directory
still costs 1.125 MiB; leaf allocations account for about 3.13 MiB.
[Before profile](results/memory-profile-before.txt),
[after profile](results/memory-profile-after.txt),
[before census](results/memory-layout-before.txt), and
[after census](results/memory-layout-after.txt).

Reproduce the opt-in exact heap profile separately from performance runs:

```sh
GOMAXPROCS=1 WORMHOLE_MEMORY_PROFILE=/tmp/wormhole-heap.pprof taskset -c 6 go -C go test -run '^TestMemoryLayoutProfile$' -v -count=1
go tool pprof -top -inuse_space /tmp/wormhole-heap.pprof
```

## Earlier seven-container comparison after cache removal

Measured after the memory reduction, with insdict v0.14.6 and Turtle v0.0.5,
65,536 initial entries, native uint64 keys and values, CPU 6 pinned,
GOMAXPROCS=1, and medians of three 100 ms samples. Put means fresh insertions.
Short samples can vary.
[Native results](results/memory-native-table.txt) and
[earlier optional string control](results/insdict-string-table.txt).

| Operation (showing ns/key) | Wormhole | Turtle | BPTree | tidwall/btree | red-black tree | insdict.Dict | builtin Go map |
| -------------------------- | -------: | -----: | -----: | ------------: | -------------: | -----------: | -------------: |
| Get                        |     56.8 |  123.9 |   97.7 |         110.7 |          197.5 |         21.0 |           16.7 |
| Put                        |    145.9 |  135.6 |  112.5 |         158.4 |          354.4 |         58.3 |           76.9 |
| Ordered scan               |     2.48 |   2.77 |   3.07 |          3.86 |          15.87 |         6.56 |            N/A |

The [first short table](results/memory-native-table-first.txt) reported
74.4 ns/key for Wormhole Get; the repeat above reported 56.8. Use the longer
matched before/after checks above to assess the memory change.

## Ordered scan optimization

Final six-container table: 65,536 initial entries, native uint64 keys and values,
CPU 6 pinned, GOMAXPROCS=1, medians of three 300 ms samples. Turtle is v0.0.4.
[Raw table](results/numeric-scan-six-final.txt).

| Operation (showing ns/key) | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Get                        |     59.3 |  121.9 |   96.5 |           16.7 |         111.0 |          210.5 |
| Put                        |    143.4 |  131.6 |  116.0 |           79.8 |         158.1 |          363.3 |
| Ordered scan               |     2.51 |   2.83 |   3.08 |  not supported |          3.88 |          15.90 |

Reproduce with `taskset -c 6 make -C go bench BENCH_CPU=6 BENCH_TIME=300ms`.
The longer point and scan checks below provide the primary Wormhole/Tidwall
comparison; short-run table samples can vary.


Measured on 2026-10-11, AMD Ryzen Threadripper 3960X, Go
1.26.4-pont-onethread, linux/amd64, CPU 6 pinned, GOMAXPROCS=1. These are
medians of three one-second samples on the final paired-load, 1 KiB-prefetch
implementation. Both containers store native uint64 keys and values.

| Initial entries | Operation (ns/key) | Wormhole | Tidwall |
| ---: | --- | ---: | ---: |
| 65,536 | Ordered scan, maximum 10,000 | 2.790 | 3.912 |
| 65,536 | Ordered scan, maximum 100,000 | 2.499 | 4.041 |
| 65,536 | Full traversal | 2.545 | 2.528 |
| 1,048,576 | Ordered scan, maximum 10,000 | 3.584 | 6.611 |
| 1,048,576 | Ordered scan, maximum 100,000 | 3.493 | 6.561 |
| 1,048,576 | Full traversal | 4.129 | 5.789 |

Bounded scans beat Tidwall at both sizes. Small full traversal is approximately
level (within 2% in this longer run), and million-entry full traversal is faster.
All these scans allocate nothing. Actual visited counts normalize short scans
near the end, as before. [Raw small run](results/numeric-scan-final-small.txt),
[raw million-entry run](results/numeric-scan-final-million.txt).

Fresh puts remain ahead at 65,536 initial entries: **141.7 versus 159.0 ns/key**,
with 37 versus 45 B/key of allocation traffic. At one million initial entries,
fresh puts are **309.8 versus 345.7 ns/key**, with 39 versus 46 B/key of
allocation traffic. [Raw million-entry fresh puts](results/numeric-scan-final-million-fresh.txt).
Structural allocations still occur; rounded `0 allocs/op` is not zero total
allocations. The scan changes do not alter the insertion path.

Reproduce the longer checks:

```sh
GOMAXPROCS=1 taskset -c 6 go -C go/bench test -run '^$' -bench '^BenchmarkCompare(Points|Iteration)$/^(Wormhole|Tidwall)$/^(FreshPut|Scan10000|Scan100000|Iterate)$' -benchtime=1s -count=3 -benchmem -args -bench-cpu=6
# Set WORMHOLE_BENCH_N=1048576 for the larger fixture.
```

The CPU-6 profile of the original uint64 scan put 42% of sampled time in
`Iterator.Skip` and its boundary handling, which ran for every key. `Next` now
increments directly and handles leaf transitions only at a boundary. Its full
traversal fell from about 8.7 to 4.9 ns/key in the intermediate measurements.

`Ref.Scan(start, limit, visit)` keeps the visitor loop inside each leaf, hoists
the string/numeric layout choice, and counts records once per leaf. This is a
production API used by the benchmark adapters in both native and string modes.
It preserves lazy sorting, left-to-right leaf lock coupling, early stopping,
and cleanup on callback panic. The callback runs under a read lock; callers
must not reenter the Ref or acquire locks on the same Map. Tests compare its
results to Iterator across mutations, cover noncomparable values and NaNs,
and exercise concurrent structural writes and callback failures.

The benchmark traces, scan limits, callbacks, sums, and actual-visited-key
normalization are unchanged. Fresh-put implementations are unchanged by this
scan optimization. Historical comparisons below used the iterator adapter.
[Original scan measurements](results/numeric-scan-baseline.txt),
[direct-Next measurements](results/numeric-scan-direct-next.txt),
[Wormhole profile](results/numeric-scan-baseline-profile.txt),
[Tidwall profile](results/numeric-scan-tidwall-profile.txt).
Tidwall's profile mostly shows its internal traversal and visitor callbacks;
Wormhole's former per-key iterator machinery was the larger avoidable cost.

For larger maps, the scan also prefetches its successor's lock, sorted indices,
and up to 1 KiB of record storage while visiting the current leaf. Storage
pointers are immutable after publication; mutable contents are read only after
locking the successor. This uses bounded amd64 cache hints, with a no-op
fallback on other architectures and under the race detector. Fresh writes do
not execute these hints. Short scans that end in the current leaf skip them. Small numeric records
(up to 32 bytes) are loaded in pairs to overlap slot-dependent loads; larger
values retain the single-record path to avoid extra temporary copies.
The [callback scan profile](results/numeric-scan-callback-profile.txt) shows the
remaining per-record work rather than the former iterator advancement costs.

## Native uint64 comparison before scan optimization

Measured on 2026-10-11, AMD Ryzen Threadripper 3960X, linux/amd64,
Go 1.26.4-pont-onethread, CPU 6 pinned, GOMAXPROCS=1. All six containers
store native uint64 keys and values. Fresh insertions grow from N to 2N;
fixture loading stays outside timing. Medians of three 100 ms samples,
65,536 initial entries. [Raw six-container table](results/numeric-final-table.txt).

| Operation (ns/key) | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Get, hit | 59.2 | 120.0 | 95.9 | 16.8 | 111.5 | 200.8 |
| Put, fresh key | 149.2 | 112.4 | 114.3 | 77.0 | 160.5 | 387.2 |
| Ordered scan, maximum 100,000 | 8.36 | 2.78 | 3.09 | not supported | 3.87 | 15.23 |

Numeric leaves now keep records in stable slots with tag-bucket chains,
removing per-key record allocations and magnet pointer shifts. Separate numeric
storage avoids scanning an unused string pointer array during GC. An
epoch-protected directory routes complete two-byte prefixes directly to leaves;
ambiguous prefixes use the trie. Numeric separator selection compares order
bits directly for every integer and floating-point type, including named types.
String separators read existing strings and retain the selected prefix without
copying its bytes. Leaf locks, version checks, epochs, and sharded counts remain.
NaNs still sort last and share one key; signed zeros share one key.

Longer CPU-6 point runs confirm the fresh-insert lead at both sizes. The
65,536-entry run uses five one-second samples; the million-entry run uses three.

| Initial entries | Operation (ns/key) | Wormhole | Tidwall |
| ---: | --- | ---: | ---: |
| 65,536 | Get, hit | 59.28 | 112.90 |
| 65,536 | Put, fresh key | 141.50 | 157.70 |
| 1,048,576 | Get, hit | 87.62 | 176.70 |
| 1,048,576 | Put, fresh key | 310.10 | 352.80 |

[Raw five-sample point run](results/numeric-directory-final-long.txt),
[raw million-entry run](results/numeric-directory-million.txt).
Fresh insertion allocation traffic is 37 versus 45 B/key at 65,536 entries,
and 39 versus 46 B/key at one million. Both lookups allocate nothing.
Structural allocations remain; rounded `0 allocs/op` does not mean zero total
allocations. Reproduce with the individual point command below, using
`-benchtime=1s -count=5` and optionally `WORMHOLE_BENCH_N=1048576`.

Wormhole still trails Turtle and BP-tree on fresh insertions in the six-container
suite, and ordered scans remain slower than Tidwall.

## Initial generic native uint64 comparison (before optimization)

Measured on 2026-10-10, AMD Ryzen Threadripper 3960X, linux/amd64,
Go 1.26.4-pont-onethread, pinned CPU 6, GOMAXPROCS=1; 65,536 initial entries,
medians of three 100 ms samples. **Every column stores native uint64 keys and
values; Put measures fresh insertions only.** Dependencies are unchanged.
[Raw generic table](results/generic-uint64-readme-table.txt).

| Operation (showing ns/key) | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Get                        |    115.6 |  135.8 |   96.7 |           15.7 |         109.8 |          200.0 |
| Put                        |    271.5 |  121.2 |  113.7 |           76.1 |         157.1 |          364.4 |
| Ordered scan               |    10.62 |   2.73 |   3.00 |  not supported |          3.87 |          15.51 |

Detailed results:

| Operation (showing ns/key)        | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| --------------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Tree `Get`, hit                   |    115.6 |  135.8 |   96.7 |           15.7 |         109.8 |          200.0 |
| Tree `Get`, miss                  |     78.5 |  115.6 |   94.2 |           15.3 |         111.3 |          215.0 |
| Tree `Put`, fresh key             |    271.5 |  121.2 |  113.7 |           76.1 |         157.1 |          364.4 |
| Ordered scan, maximum 10,000      |    10.68 |   2.77 |   3.03 |  not supported |          3.89 |          15.71 |
| Ordered scan, maximum 100,000     |    10.62 |   2.73 |   3.00 |  not supported |          3.87 |          15.51 |
| Full traversal (Go map unordered) |    11.22 |   2.75 |   3.94 |           9.22 |          2.37 |          15.08 |

The generic conversion does not establish a fresh-insertion speedup over the
previous string API, and Wormhole still trails Tidwall on this workload.
Short-run reads varied between captures; avoid interpreting a single short
sample as an improvement. The
[initial generic capture](results/generic-uint64-initial-table.txt) predates
caching the routing key's type.

Longer verification on CPU 6, three one-second samples per case, gives these
medians and confirms the fresh-write gap:

| Operation (ns/key) | Wormhole | Tidwall |
| --- | ---: | ---: |
| Get, hit | 82.77 | 110.7 |
| Put, fresh key | 269.2 | 157.0 |

Wormhole fresh inserts report 34 B/key and one allocation/key; both containers'
lookups allocate nothing. [Longer raw samples](results/generic-uint64-points-long.txt).
Reproduce with:

```sh
GOMAXPROCS=1 taskset -c 6 go -C go/bench test -run '^$' -bench '^BenchmarkComparePoints$/^(Wormhole|Tidwall)$/^(GetHit|FreshPut)$' -benchtime=1s -count=3 -benchmem -args -bench-cpu=6
```

A two-second fresh-insert profile of the initial conversion measured 295.2
ns/key, 34 B/key and one allocation/key. Its phase labels exclude fixture
reloads from the displayed insert profile. Repeated reflection was visible in
routing, sorting and splitting, so the final code caches the kind once per map
and once per sort/split. Leaf insertion, splitting, metadata routing, pointer
barriers and allocation remain significant costs. Lookups allocate nothing.
[Profile invocation result](results/generic-fresh-profile-run.txt),
[filtered CPU profile](results/generic-fresh-profile-top.txt).

## Historical comparison before the generic Wormhole API

Measured on 2026-10-10, AMD Ryzen Threadripper 3960X, linux/amd64,
Go 1.26.4-pont-onethread, pinned CPU 6, GOMAXPROCS=1; 65,536 entries,
medians of three 100 ms samples. Turtle v0.0.2, BP-tree v0.0.3,
Tidwall v1.8.1 and rbtree v0.2.2. Wormhole's library code is unchanged.
The five competitors store native uint64 keys/values. Wormhole stores their
pre-encoded eight-byte strings, with encoding/decoding outside timing.
[Raw corrected table](results/native-uint64-readme-table.txt).

| Operation (showing ns/key) | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Get                        |     95.1 |  121.8 |   98.4 |           16.7 |         108.8 |          200.6 |
| Put                        |    267.5 |  137.4 |  114.2 |           92.0 |         176.5 |          335.5 |
| Ordered scan               |    13.09 |   2.79 |   3.00 |  not supported |          3.88 |          15.39 |

Detailed results:

| Operation (showing ns/key)        | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| --------------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Tree `Get`, hit                   |     95.1 |  121.8 |   98.4 |           16.7 |         108.8 |          200.6 |
| Tree `Get`, miss                  |     82.9 |  116.1 |   99.2 |           15.1 |         109.4 |          220.5 |
| Tree `Put`, existing key          |    102.4 |   20.4 |  125.5 |           24.3 |         123.0 |          202.4 |
| Tree `Put`, fresh key             |    267.5 |  137.4 |  114.2 |           92.0 |         176.5 |          335.5 |
| Ordered scan, maximum 10,000      |    13.80 |   2.80 |   3.04 |  not supported |          3.92 |          15.19 |
| Ordered scan, maximum 100,000     |    13.09 |   2.79 |   3.00 |  not supported |          3.88 |          15.39 |
| Full traversal (Go map unordered) |    14.41 |   2.75 |   3.98 |           9.21 |          2.36 |          14.99 |


The earlier comparison accidentally changed the storage representation being
measured. Sequential audit runs on the same CPU 6, GOMAXPROCS=1, three
200 ms samples, reproduce the discrepancy and verify the fix (median ns/hit):

| Harness | builtin Go map | Tidwall |
| --- | ---: | ---: |
| Turtle source suite, native uint64 | 15.63 | 111.0 |
| Previous Wormhole suite, all strings | 27.88 | 236.8 |
| Corrected Wormhole suite, native uint64 | 16.76 | 110.8 |

The exact reported 53 ns was not reproduced in this audit. The representation
mismatch is reproducible independently of the earlier noisy CPU 0 run. These
are short sequential captures, not a claim of zero host variation. Disassembly
also confirms the actual builtin-map lookup changed from
`runtime.mapaccess1_faststr` to `runtime.mapaccess1_fast64`.

Audit logs: [Turtle reference](results/bench-audit-turtle-native.txt),
[previous string harness](results/bench-audit-before-string.txt),
[corrected native harness](results/bench-audit-after-native.txt),
[native map assembly](results/bench-audit-native-map-asm.txt), and
[string map assembly](results/bench-audit-string-map-asm.txt).
Benchmark-module race tests and vet pass; both representation modes have
correctness/table-coverage checks. An opt-in all-string table smoke run also
passes; its one-iteration timings are not performance measurements.
[Smoke log](results/string-mode-table-smoke.txt).

## Earlier all-string comparison with Turtle v0.0.2

Measured on 2026-10-10, AMD Ryzen Threadripper 3960X, linux/amd64,
Go 1.26.4-pont-onethread, CPU 6 verified, GOMAXPROCS=1. Population 65,536;
medians of three 100 ms samples. Wormhole library is 1d2aeb3, BP-tree v0.0.3,
Tidwall v1.8.1, rbtree v0.2.2, and the released Turtle Go module v0.0.2
(commit `2fe064dd6a7bf3ac0bc48282c30888085dfe3b5b`). Turtle uses its generic
`Map[string, string]` API and defaults: MemTable 16,384 / leaf 65,536 / fanout 16.
All inputs are pre-encoded before timing; no local Turtle checkout is used.
[Raw table run](results/turtle-v0.0.2-readme-table.txt).

Reproduce with `taskset -c 6 make -C go bench BENCH_CPU=6`.
The simplified rows select Get hit, fresh-key Put, and ordered scans with
maximum length 100,000. Numbers are ns/key; smaller is faster.

| Operation (showing ns/key) | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Get                        |     99.2 |  282.5 |  332.8 |           27.4 |         255.5 |          328.4 |
| Put                        |    290.5 |  766.4 |  588.9 |          213.6 |         510.2 |          618.3 |
| Ordered scan               |    13.86 |   8.29 |   4.66 |  not supported |          6.02 |          18.74 |

Detailed results:

| Operation (showing ns/key)        | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| --------------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Tree `Get`, hit                   |     99.2 |  282.5 |  332.8 |           27.4 |         255.5 |          328.4 |
| Tree `Get`, miss                  |     85.1 |  278.4 |  394.1 |           20.2 |         253.7 |          370.7 |
| Tree `Put`, existing key          |    105.8 |   39.7 |  400.1 |           35.3 |         254.2 |          322.5 |
| Tree `Put`, fresh key             |    290.5 |  766.4 |  588.9 |          213.6 |         510.2 |          618.3 |
| Ordered scan, maximum 10,000      |    14.85 |   8.21 |   4.56 |  not supported |          5.87 |          17.48 |
| Ordered scan, maximum 100,000     |    13.86 |   8.29 |   4.66 |  not supported |          6.02 |          18.74 |
| Full traversal (Go map unordered) |    15.52 |   6.88 |   7.56 |          13.07 |          4.04 |          16.43 |


Benchmark-module race tests, vet, and module checksum verification pass.
A separate pinned 50 ms allocation check reports zero bytes and zero allocations
for hit, miss, and replacement operations in all six containers.
[Raw allocation run](results/turtle-v0.0.2-allocs.txt).

Turtle's 8,192-rank update trace largely fits in its default MemTable; fresh
writes include growth and automatic flushes. These are common-string storage
measurements, not native uint64 results or an isolated before/after estimate
of the release's speedup. The earlier table already used a generic working tree.
A [CPU 0 control](results/turtle-v0.0.2-cpu0-control-table.txt) is retained too:
several late cells took roughly twice earlier same-commit measurements across
unrelated containers. The fresh CPU 6 run above is the published checkpoint.

## Earlier common-string comparison with local Turtle

Measured on 2026-10-10, AMD Ryzen Threadripper 3960X, linux/amd64,
Go 1.26.4-pont-onethread, CPU 0 verified, GOMAXPROCS=1. Population 65,536;
medians of three 100 ms samples. Wormhole library is 1d2aeb3, BP-tree v0.0.3,
Tidwall v1.8.1, rbtree v0.2.2. Turtle is the local generic working tree based on
`dbcb2cdedce1305294f93dd42feece22e4702a72`, including its uncommitted changes,
with default MemTable 16,384 / leaf 65,536 / fanout 16. This records the local
comparison; it is not a measurement of Turtle's unchanged HEAD. All inputs
are pre-encoded before timing. [Raw table run](results/six-container-readme-table.txt).

The simplified rows select Get hit, fresh-key Put, and ordered scans with
maximum length 100,000. Numbers are ns/key; smaller is faster.

| Operation (showing ns/key) | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Get                        |    103.6 |  330.2 |  340.2 |           30.6 |         248.6 |          322.4 |
| Put                        |    277.3 |  776.4 |  505.9 |          226.1 |         596.0 |          638.4 |
| Ordered scan               |    13.30 |   8.49 |   4.46 |  not supported |          5.45 |          17.81 |

Detailed results:

| Operation (showing ns/key)        | Wormhole | Turtle | BPTree | builtin Go map | tidwall/btree | red-black tree |
| --------------------------------- | -------: | -----: | -----: | -------------: | ------------: | -------------: |
| Tree `Get`, hit                   |    103.6 |  330.2 |  340.2 |           30.6 |         248.6 |          322.4 |
| Tree `Get`, miss                  |     85.2 |  392.4 |  387.3 |           22.6 |         333.1 |          558.1 |
| Tree `Put`, existing key          |    106.6 |   43.9 |  324.3 |           35.3 |         261.7 |          324.3 |
| Tree `Put`, fresh key             |    277.3 |  776.4 |  505.9 |          226.1 |         596.0 |          638.4 |
| Ordered scan, maximum 10,000      |    15.29 |   8.69 |   4.74 |  not supported |          5.54 |          17.74 |
| Ordered scan, maximum 100,000     |    13.30 |   8.49 |   4.46 |  not supported |          5.45 |          17.81 |
| Full traversal (Go map unordered) |    14.80 |   6.25 |   5.60 |          12.50 |          3.96 |          16.43 |


This trace repeats 8,192 ranks; Turtle's existing-key writes mostly overwrite
pending MemTable entries. Fresh-key writes include growth and automatic flushes.
The table compares ordinary single-goroutine APIs: Wormhole still provides its
normal concurrency safeguards, while the other containers use their ordinary
serial configurations. Scans and full traversals count actual visited entries.
A separate pinned 50 ms allocation check reports zero bytes and zero allocations
for hit, miss, and replacement operations in all six containers.
[Raw allocation run](results/six-container-allocs.txt).
These common-string results should not be mixed with the source suite's native
uint64 results or the separate million-key write/latency fixtures below.

## Six-word string probe regression audit (2026-10-11)

The generic/numeric optimization sequence regressed the original `concbench`
string probe workload. This is separate from the native uint64 `make bench`
comparisons and from the retained 18.26 Mops/s hot-64 replacement-write win
against C's 5.37 Mops/s. That earlier result was four writers repeatedly writing
64 keys; it was not million-key read throughput.

All audit runs used the same million-key corpus fingerprint, zero Go misses,
and verified worker pinning. Four readers used CPUs 0–3, twenty used 0–19.
Current Go was built with the user's `MetaCache:true` driver setting; historical
versions used their then-default construction. C used the original,
up-to-date `concbench.out`. Three-second confirmation-round medians:

| Implementation | Four-reader probe Mops/s |
| --- | ---: |
| Original C | 13.07 |
| Go before generics (`1d2aeb3`) | 12.17 |
| Current Go | 10.30 |

Current Go is about 15% slower than its pre-generics version, and about 21%
slower than C, in this four-reader check. At twenty readers, a two-second
control measured old Go at 52.61 and current Go at 43.22 Mops/s; C's separate
three-second control measured 57.50. The first old-Go twenty-reader process
varied widely (25.47, 24.39, 52.25); it is preserved, not used to claim a stable
old-version rate. These are sequential process checks with time-seeded worker
zero, not identical query sequences or a fully controlled causal decomposition.

The one-second historical four-reader checks measured 12.00 Mops/s before
generics, 11.74 at the initial generic conversion (`60e3550`), 10.92 after
numeric slot/layout tuning (`a53a328`), and 10.13 currently. They localize most
of the loss after the initial conversion; they do not isolate each commit's
causal cost. The shared string leaf path gained numeric-layout dispatch and
its formerly inline hash-slot pointer array became a separate allocation.
Prepared string keys also grew from 24 to 48 bytes. These changes add footprint,
branches and dependent loads despite being motivated by numeric performance
and GC footprint. `MetaCache:true` does not restore the former string leaf layout.

Matched twenty-reader `perf` profiles put about 27% of current Go cycles in
`lockLeaf`, 22% in leaf `match`, and 11% in `probe`. Annotation of `match` places
most sampled cycles around pointer-array and record-header loads. Sampling skid
means these are locality/indirection clues, not a precise attribution of lost
throughput. C's LTO profile is mostly folded into `kv_probe_worker`; its top
symbol alone cannot be compared directly with a single Go function.

Two temporary experiments did not establish a recovery: replacing membership's
ordering comparator with equality, and extending the future prepared-key
prefetch to cover the larger key header. Neither was kept. No library fix is
claimed by this audit; numeric storage/memory wins need to be preserved while
recovering the string lookup path. [Commands, raw runs, both perf reports, and
annotated Go matching](results/concbench-regression-2026-10-11/audit.txt) are saved.

## String probe bisect and adjacent-revision confirmations

Two `git bisect --no-checkout` passes in a separate shared clone identified
`6760239` and `08b5d30` as successive regression boundaries. Every build used
the same compiler, verified CPUs 0–3, million-key corpus, and a fixed worker-zero
query seed of 2023. Construction was normalized to the original initial-table
configuration (`MetaCache:true` / `NoMetaCache:false` where those APIs exist).
Only the temporary benchmark drivers received these setup changes. All library
sources were taken directly from each tested revision.

Adjacent parent/child checks used three alternating process pairs, with three
two-second probe rounds per process. Pooled medians of the nine round rates:

| Commit | Change | Parent Mops/s | Child Mops/s | Drop |
| --- | --- | ---: | ---: | ---: |
| `60e3550` | Initial generic conversion | 12.21 | 11.70 | 4.2% |
| `6760239` | Stable numeric leaf slots | 11.55 | 11.23 | 2.8% |
| `08b5d30` | Separate string hash-slot arrays | 11.12 | 10.35 | 6.9% |

The regression accumulated across these changes. The initial generic conversion
already introduced a smaller loss; the numeric-storage changes added further
losses while retaining the generic API in both parent and child.

`6760239` adds a numeric-layout branch to leaf matching and numeric fields to
the shared leaf layout. `08b5d30` changes the string hash-slot array from an
inline array to a pointer to a separate allocation, while moving numeric storage
out of the common leaf. These are verified source changes in the measured
culprit commits. The adjacent comparisons establish each commit's throughput
cost; they do not individually isolate every field, branch, or instruction's
contribution. Percentages measured in separate pairs should not be added as
an exact decomposition of the overall loss.

The first bisect used an endpoint-midpoint cutoff of 11.22 Mops/s; `6760239`
was close enough to that cutoff that the binary classification alone was weak.
The independent nine-round parent/child comparison confirms its smaller drop.
The second bisect used 10.70 Mops/s, followed by the same adjacent confirmation.
Initial-conversion checks were then added explicitly, so its earlier cost is
not attributed to the numeric changes. All noisy rounds are retained.

Saved evidence: [first bisect log](results/probe-bisect-2026-10-11/bisect.log),
[second bisect log](results/probe-bisect-2026-10-11/second-bisect.log),
[all raw runs](results/probe-bisect-2026-10-11/runs.txt),
[machine-readable rates](results/probe-bisect-2026-10-11/measurements.jsonl), and
[exact temporary harness](results/probe-bisect-2026-10-11/harness.txt).
The harness records the workspace and temporary-clone paths used; adjust its
`ROOT` and `WORK` constants to replay elsewhere. No library changes resulted
from the bisect, and the user's pending concbench constructor edit is untouched.

# Write benchmarks

For matched native C/Go CPU and contention profiling, see [PROFILE.md](PROFILE.md).
Subsequent per-Ref size-counter sharding and its exact `Len()` tradeoff are
measured separately in [COUNTER.md](COUNTER.md). The earlier Tidwall checkpoint tables below
predate that change and subsequent grace-period polling. A newer throughput
checkpoint is included below. Current native C write
throughput/latency comparisons and remaining gaps are in
[WRITE_OPTIMIZATION.md](WRITE_OPTIMIZATION.md).

This separate binary-string write suite compares Wormhole with
`github.com/tidwall/btree` v1.8.1, the version used by `~/bufftree/bench`.
Unlike the default native-uint64 `make bench` comparison above, both receive
identical 8-byte big-endian string keys and string values. The bufftree examples use uint64 keys/values,
so their results are not directly comparable. Tidwall uses its default degree
32; `TidwallLocked` adds an RWMutex for shared access. Plain Tidwall runs only
with one writer. Wormhole uses one Ref per writer.

Latest checkpoint, identical-value-header optimization after 1c7c6e9,
ordinary Put/Set, 1,048,576 binary-key writes, three repetitions.
Unclocked throughput medians (ns/write):

| Scenario | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| Fresh, one writer | 476.2 | 1193 | 1196 |
| Update, one writer | 184.0 | 837.0 | 892.1 |
| Ascending, one writer | 159.5 | 260.1 | 236.1 |
| Fresh, four writers | 177.0 | — | 1488 |
| Update, four writers | 55.61 | — | 1058 |

Separate clocked runs, all writes sampled, median p99 (ns):

| Scenario | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| Fresh, one writer | 7204 | 3847 | 3867 |
| Update, one writer | 491 | 1482 | 1473 |
| Ascending, one writer | 1743 | 741 | 741 |
| Fresh, four writers | 8676 | — | 12554 |
| Update, four writers | 511 | — | 14487 |

GOMAXPROCS=4, CPU 0 for serial writers and CPUs 0–3 for parallel writers;
all writer affinities are verified. Updates replace the initial value with
written-a/written-b, so they exercise changing value headers. Every update
reports zero bytes and zero allocations. Fresh/ascending Go writes report
51 B and one allocation. Go leads all throughput comparisons and replacement
p99; Tidwall still leads serial fresh/ascending p99. These within-batch results
are not controlled before/after estimates against the older checkpoints.
Raw: `results/valuehint-tidwall-{write,parallel,latency}-1m.txt`.
The six-word original-C comparison uses a different fixture; absolute rates
should not be mixed with this table.

Earlier checkpoint, implementation a83b974, ordinary Put/Set, 1,048,576
binary-key writes and three repetitions. Unclocked throughput medians (ns/write):

| Scenario | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| Fresh, one writer | 501.7 | 1129 | 1169 |
| Update, one writer | 179.6 | 840.4 | 810.1 |
| Ascending, one writer | 153.8 | 246.7 | 238.0 |
| Fresh, four writers | 177.8 | — | 1547 |
| Update, four writers | 60.58 | — | 1199 |

Separate clocked runs, sampling all writes, give median p99 (ns):

| Scenario | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| Fresh, one writer | 7594 | 3868 | 3917 |
| Update, one writer | 491 | 1473 | 1472 |
| Ascending, one writer | 1744 | 722 | 741 |
| Fresh, four writers | 8927 | — | 12564 |
| Update, four writers | 521 | — | 8005 |

GOMAXPROCS=4, CPU 0 for serial throughput and CPUs 0–3 for parallel throughput
and latency; each writer's assigned CPU is pinned and verified (serial latency
uses CPU 0). Tidwall remains v1.8.1, with degree 32. Go leads throughput in all
scenarios and replacement p99, but serial fresh/ascending p99 still trails
Tidwall. Parallel TidwallLocked fresh p99 ranges from 12.06 to 20.63 us;
its median is not a stable worst-case bound. Updates allocate zero bytes;
Go fresh/ascending writes report 51 B and one allocation per write. Raw:
`results/rootproof-tidwall-{write,parallel,latency}-1m.txt`.
These are matched ordinary API comparisons with binary keys; the C comparisons
use prepared six-word keys and a different fixture, so absolute rates should
not be mixed between these tables.

Earlier throughput checkpoint, implementation 0a19552, ordinary Put/Set,
1048576 binary-key writes, medians of three repetitions (ns/write):

| Scenario | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| Fresh, one writer | 499.7 | 1175 | 1198 |
| Update, one writer | 192.0 | 842.9 | 858.6 |
| Ascending, one writer | 164.5 | 251.3 | 240.3 |
| Fresh, four writers | 182.8 | — | 1406 |
| Update, four writers | 58.71 | — | 1307 |

GOMAXPROCS=4; serial process affinity is CPU 0, parallel affinity CPUs 0–3.
Writer affinity is verified. Updates allocate zero bytes in every store;
Wormhole fresh/ascending writes report 51 B and one allocation per write.
These are within-batch comparisons, not before/after estimates from older tables;
absolute rates and GC placement can differ between batches. One serial Wormhole
fresh repetition was slower (771.6 ns), retained in the raw log. Latency was not
refreshed in this checkpoint. Raw:
`results/post-prefetch-tidwall-write-1m.txt` and
`results/post-prefetch-tidwall-parallel-1m.txt`.

Earlier pinned comparison checkpoint, 2026-10-10, Threadripper 3960X and Go
1.26.4-pont-onethread: million-key binary traces, GOMAXPROCS=4, CPU 0 for
serial writers and CPUs 0/1/2/3 for parallel writers. Medians of three unclocked
throughput runs (ns/write):

| Scenario | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| Fresh, one writer | 530.3 | 1114 | 1119 |
| Update, one writer | 197.4 | 816.3 | 818.1 |
| Ascending, one writer | 190.4 | 173.4 | 180.9 |
| Fresh, four writers | 278.4 | — | 1499 |
| Update, four writers | 58.80 | — | 1293 |

Separate clocked runs report these p99 ranges over three repetitions (us):

| Scenario | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| Fresh, one writer | 7.254–7.644 | 3.937–3.948 | 3.717–4.378 |
| Update, one writer | 0.511–0.531 | 1.433–1.452 | 1.433–1.453 |
| Ascending, one writer | 1.754–1.944 | 0.741–0.942 | 0.741–0.822 |
| Fresh, four writers | 9.238–9.708 | — | 17.934–30.448 |
| Update, four writers | 0.521–0.551 | — | 9.788–22.673 |

Each run samples all 1,048,576 writes. The pinned clock control measured a
median 68.96 ns; its cost is included in the quantiles, never subtracted.
Updates allocate zero bytes in both stores. Wormhole has better throughput on
the random fresh/update traces, but serial fresh and ascending p99 remain
worse than Tidwall. Ascending latency runs use one writer to retain ascending
insertion order. Raw final logs are `final-write-1m.txt` and
`final-latency-1m.txt`. The final six-word corpus comparison uses the same million-key fixtures and
pinning, with ordinary Put/Set and three repetitions:

| Corpus fresh writes | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| One writer, median ns/write | 698.7 | 1393 | 1411 |
| Four writers, median ns/write | 340.8 | — | 2092 |
| One writer, p99 range (us) | 10.791–11.081 | 4.188–4.259 | 4.458–4.749 |
| Four writers, p99 range (us) | 14.468–14.989 | — | 19.127–70.684 |

The mutex wrapper's four-writer tail varies substantially across repetitions;
these results apply to this trace, toolchain and CPU placement. Raw corpus logs
are `final-corpus-write-1m.txt` and `final-corpus-latency-1m.txt`. The sections
below retain the experiment history and its original measurement conditions.

Fresh inserts preload N permuted even keys and insert adjacent odd keys.
Updates replace those N existing keys. Ascending starts empty. Fixtures, key
creation, handle registration, result checks, and resets are outside the timer.
Every pass resets the fixture; fresh inserts never become replacements. Parallel
runs partition the same trace among workers. Launch, affinity setup, and affinity
restoration are outside the timer; barrier release/wait are timed. Linux writers
lock their OS threads and verify single-CPU affinity: serial uses the first
allowed CPU; parallel worker i uses allowed CPUs[i % count], matching native C.
Use taskset to select physical cores (0–3 on the measurement host). Pinning
failure fails the run. Benchmark logs show the worker CPU assignments.
The reported ns/op is inverse aggregate throughput, not per-write latency.

Latency benchmarks clock each write, or uniformly sample at most 1,048,576
writes, and report raw wall-clock quantiles including clock overhead. Clock
cost has its own benchmark; it is not subtracted. Scheduling and GC remain
enabled. Profiles label only timed writes `phase=write`; fixture work still
appears in unfiltered CPU profiles.

Run from this directory (set a writable GOCACHE if needed):

```sh
go test -race ./...
go vet ./...
go test -c -o /tmp/wormhole-write.test
WORMHOLE_BENCH_N=1048576 taskset -c 0 /tmp/wormhole-write.test -test.run '^$' -test.bench '^BenchmarkWrites$' -test.benchtime=1048576x -test.count=3 -test.cpu=1
WORMHOLE_BENCH_N=1048576 taskset -c 0-3 /tmp/wormhole-write.test -test.run '^$' -test.bench '^BenchmarkWritesParallel$' -test.benchtime=1048576x -test.count=3 -test.cpu=4
WORMHOLE_BENCH_N=65536 taskset -c 0-3 /tmp/wormhole-write.test -test.run '^$' -test.bench 'BenchmarkWriteLatency|BenchmarkLatencyClock' -test.benchtime=65536x -test.cpu=4
```

Historical results below predate verified per-worker pinning: the process was
restricted to CPUs 0–3, but Go workers could migrate within that set. Treat
these as historical measurements; repeat with the current harness before
comparing against newly pinned results.

Baseline, 2026-10-10, Threadripper 3960X, Go 1.26.4-pont-onethread,
million-key fixture, medians of three repetitions (ns/write):

| Scenario | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| Fresh, one writer | 880.3 | 1267 | 1224 |
| Update, one writer | 257.0 | 798.0 | 806.6 |
| Ascending, one writer | 292.1 | 193.4 | 203.9 |
| Fresh, four writers | 298.5 | — | 1140 |
| Update, four writers | 99.02 | — | 873.5 |

These numbers cover this binary-string trace only. The 65,536-key fresh-insert
latency run showed Wormhole p99 of 9.87 us with one writer and 11.09 us with
four, versus Tidwall 3.05 us serial and TidwallLocked 3.45 us with four writers.
This reveals a tail-latency weakness despite competitive aggregate throughput.
The write-filtered profile attributes about 21% of write CPU time cumulatively
to leaf slot shifting and index repair, and about 26% to lazy sorting. These
are the next optimization targets. Raw logs are in `results/`.

After porting native vectorized slot-index repair (baseline SSE2 on amd64,
scalar elsewhere and in race builds), the same million-key runs measured
714.1 ns for serial fresh inserts, 221.7 ns ascending, and 254.5 ns with four
fresh-insert writers: respectively 19%, 24%, and 15% less time than baseline.
Tidwall fresh inserts stayed near 1,272 ns serial and 1,181 ns with the mutex
and four writers. Updates do not shift slots and showed no clear improvement.
Fresh p99 at 65,536 keys was 8.35–10.08 us serial and 8.69–9.63 us with four
writers over three repetitions; Tidwall was still better at 2.77–3.02 us serial.
The split/sort tail needs further work. Raw post-change logs have `shift-`
prefixes. All measurements retain ordinary automatic GC.

Caching key headers during dirty-tail sorting and porting native's smaller-run
merge reduced fresh inserts further: 669.3 ns serial and 245.7 ns with four
writers (medians of three million-key repetitions). Full-leaf sorting fell
from about 7.55 us to 6.47 us, and merging a one-key dirty tail from about
733 ns to 245 ns, with zero allocations in these leaf microbenchmarks.
Fresh-insert p99 at 65,536 keys was 7.88–8.00 us serial and 8.16–8.73 us with
four writers; a repeated baseline run measured 9.58–9.61 us and 10.00–10.51 us
respectively. Tidwall serial p99 remained about 2.7 us. Logs use `sort-` and
`baseline-latency-repeat-` prefixes. The improvement is useful, but does not
close the tail-latency gap.

The next chunk caches zero-padded big-endian eight-byte key heads during
sorting. Differing heads establish bytewise order; ties compare full keys,
including short keys containing zeros. If all heads match, sorting skips the
head comparisons. This retains the same lazy sorting and split algorithm and
allocates no scratch objects. Common-prefix leaf benchmarks showed no
regression. Million-key fresh inserts measured 646.5 ns serial and 231.1 ns
with four writers, versus Tidwall 1,183 ns serial and TidwallLocked 1,185 ns
with four writers (medians of three). Compared with the initial Wormhole
baseline, those are 27% and 23% less time per write.

Fresh p99 at 65,536 keys fell to 6.39–6.53 us serial and 6.85–7.00 us with
four writers. At one million keys it was still 13.48–13.68 us serial and
15.06–15.27 us with four writers; Tidwall serial was 3.78–3.81 us and
TidwallLocked with four writers was 7.56–8.54 us. The larger fixture exposes
additional cache and structural-work costs. Raw logs use `head-` prefixes.

The actual native concbench corpus, one million six-word keys and four load
workers, measured 4.34 M writes/sec in Go versus 4.32 M in a C rerun under the
same process affinity. These single load runs indicate parity within noise.
Go probe rounds were 11.86, 11.83, 11.99 Mops/sec with zero misses; native
rounds were 12.80, 12.59, 12.78 Mops/sec. This prepared-key corpus load is a
different workload from the public Put binary-string benchmarks above.

Reusing cached key headers/heads for the sorted-run merge reduces the remaining
pointer chasing while retaining native's smaller-run merge. One-key tails and
tails with a shared eight-byte head keep the direct merge path. All live run
lengths and both merge directions are checked independently against known key
order. Native/race suites, vet, ARM64 compilation and benchmark-module race
tests passed. No scratch objects are allocated.

The same million-key fresh-write runs now measure 567.3 ns serial and 222.5 ns
with four writers, versus 646.5/231.1 ns before this chunk. Million-key p99
fell to 8.92–9.04 us serial and 9.61–10.11 us with four writers, from
13.48–13.68 us and 15.06–15.27 us. Tidwall serial p99 is still about 3.9 us;
the four-writer mutex wrapper is around 8–9 us. Logs use `mergecache-` prefixes.

`BenchmarkCorpusWrites` and `BenchmarkCorpusWriteLatency` broaden the comparison
to six-word keys generated by the native concbench generator. The first N keys
preload the map and the next N are fresh writes. Both containers use ordinary
Put/Set with the same string values; Wormhole computes tags during writes.
This differs from concbench's prepared keys and from the even/odd binary trace.
Corpus runs use GOMAXPROCS=4 even for one writer. The dictionary defaults to
`../../words.txt`; override it with `WORMHOLE_BENCH_WORDS`. Tests use a private
temporary dictionary and do not require that external file.

```sh
WORMHOLE_BENCH_N=1048576 taskset -c 0-3 /tmp/wormhole-write.test -test.run '^$' -test.bench '^BenchmarkCorpusWrites$' -test.benchtime=1048576x -test.count=3 -test.cpu=4
WORMHOLE_BENCH_N=1048576 taskset -c 0-3 /tmp/wormhole-write.test -test.run '^$' -test.bench '^BenchmarkCorpusWriteLatency$' -test.benchtime=1048576x -test.count=3 -test.cpu=4
```

On the same machine/toolchain, using the actual 466,550-word dictionary:

| Corpus fresh writes | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| One writer, median ns/write | 760.9 | 1366 | 1402 |
| Four writers, median ns/write | 270.4 | — | 1451 |
| One writer, p99 range (us) | 10.64–11.61 | 4.15–4.17 | 4.48–4.75 |
| Four writers, p99 range (us) | 11.98–12.76 | — | 8.98–9.91 |

Wormhole's throughput advantage extends to this corpus, but its structural
write tail still needs work. Raw logs have `corpus-` prefixes. Adapter and
trace tests, race-instrumented benchmark smoke runs, and vet passed.

Verified per-worker CPU pinning, 2026-10-10: fresh million-key binary writes,
GOMAXPROCS=4, serial writer on CPU 0, four writers on CPUs 0/1/2/3. No other
project test jobs ran during these measurements. Throughput entries are medians
of three repetitions; latency entries are one run with 1,048,576 samples.

| Fresh writes | Wormhole | Tidwall | TidwallLocked |
| --- | ---: | ---: | ---: |
| One writer, ns/write | 599.2 | 1120 | 1138 |
| Four writers, ns/write | 279.1 | — | 1449 |
| One writer, p99 (us) | 8.887 | 3.727 | 3.747 |
| Four writers, p99 (us) | 11.041 | — | 15.159 |

The serial p99 gap remains despite better aggregate throughput. This is the
new pinned checkpoint; do not attribute differences from historical figures
solely to library changes. Raw logs use `pinned-` prefixes and record CPU
assignments. Ordinary GC and scheduling remain enabled.

The shared-prefix sort optimization caches the next eight bytes when all live
keys share their first eight bytes. Native differential traces and edge cases
verify unchanged ordering. CPU-0 full-leaf common-prefix sorting improved from
6.401 us to 4.291 us (medians of three), retaining stack scratch storage.
Million-key binary fresh-write controls were noisy: before 581.1–668.2 ns,
after 562.1–673.4 ns. These do not establish an integrated throughput gain.
The controls used the same pre-pinning harness binaries with their entire
process restricted to CPU 0, GOMAXPROCS=1. Raw logs use `sharedhead-` prefixes.

A bounded radix pass now partitions dirty runs of at least 32 entries by the
first differing eight cached-head bits. Full-key comparisons sort each bucket;
the existing smaller-run merge and split/anchor policy are unchanged. Runs below
32 entries and equal heads retain comparison sorting. Scratch stays on the
stack. Randomized skewed-bucket/padding/prefix tests, native/race suites, vet,
benchmark-module race tests, and ARM64 compilation passed.

Pinned million-key fresh binary-write controls (three repetitions each):

| Metric | Before radix pass | After radix pass |
| --- | ---: | ---: |
| Serial median ns/write | 619.6 | 567.0 |
| Four-writer median ns/write | 275.0 | 273.1 |
| Serial p99 range (us) | 8.897–9.308 | 7.374–7.745 |
| Four-writer p99 range (us) | 11.071–11.502 | 9.969–10.189 |

CPU-0 microbenchmarks measured full binary-leaf sorting at 3.160 us before and
0.881 us after (medians of three); the common-prefix leaf case improved from
4.227 us to 3.862 us. These remain local measurements, not proof of equal gains
on every key distribution. Raw logs use `radix-` prefixes. Serial p99 is still
above Tidwall's pinned checkpoint, so further structural-work reduction remains
useful.

The same pinned before/after check on million-key six-word corpus fresh writes
did not show a clear benefit: serial p99 was 10.660–12.474 us before versus
11.031–11.542 us after; four-writer p99 was 14.798–14.958 us versus
14.708–15.339 us. Four-writer inverse throughput with latency sampling was
349.8–353.4 ns before and 360.5–364.4 ns after. These samples show no corpus
latency win and a small throughput cost in this run; further profiling should
address corpus structural work rather than assuming the binary-key gain
transfers. Raw files are `radix-corpus-latency-{before,after}.txt`.

A newly pinned concbench comparison (one native and one Go invocation, same
million-key corpus, CPU set 0–3, default three-second rounds) measured:

| Driver | Four-worker load (Mops/s) | Probe rounds (Mops/s) |
| --- | ---: | --- |
| C | 4.31 | 12.78, 12.62, 11.67 |
| Go | 3.41 | 11.76, 11.55, 10.56 |

The Go corpus fingerprint matched the native workload's previously verified
fingerprint and all Go probe rounds had zero misses. These are sequential runs,
not evidence that Go beats native. The raw `radix-concbench-` files preserve the
output. `make concbench.out` confirmed the native binary was up to date.

Existing-key Put/PutKey, Merge and Update now replace only the stored value's
string header while holding the leaf write lock. Every value reader copies its
header under a leaf read lock, and the underlying string bytes remain immutable.
Keys, hash placement, metadata publication, counts and split/merge rules are
unchanged. This removes a 32-byte record allocation from each replacement;
retained Get/Peek/callback values remain valid. Native/race tests, targeted
readers/replacements/split/merge stress, vet and ARM64 compilation passed.

Pinned million-key binary updates, same CPU assignments as above, three runs:

| Metric | Before | After | Tidwall after | TidwallLocked after |
| --- | ---: | ---: | ---: | ---: |
| Serial median ns/write | 265.5 | 218.5 | 829.0 | 839.2 |
| Four-writer median ns/write | 98.26 | 61.24 | — | 1283 |
| Serial p99 range (ns) | 631–691 | 531–561 | — | — |
| Four-writer p99 range (ns) | 721–731 | 531–551 | — | — |
| Bytes/allocations per replacement | 32 / 1 | 0 / 0 | 0 / 0 | 0 / 0 |

The fresh-write control measured 578.9 ns serial and 272.6 ns with four writers
(medians of three), near the prior radix checkpoint's 567.0/273.1 ns. These
controls do not establish a change in fresh-write throughput. Latency samples
include clock overhead; throughput rows use the unclocked loop. Raw logs use
`replacement-` prefixes.

Porting native metadata/leaf cache hints added amd64 prefetches for both candidate
pointer buckets and the leaf's likely tag/pointer slot. Epochs, locks, version
checks and placements still govern all accesses. Race and non-amd64 builds omit
the hints. Native/race suites, vet, feature-disabled checks and ARM64 compilation
passed. Parallel root probe benchmarks also pin and verify each worker now;
worker setup/restore remain included in that RunParallel benchmark's timing.

The ten-million-write corpus profile before these hints attributed about 39% of
write-tagged samples to routing and 23% to leaf split work, with overlapping
call stacks; the filtered text report is `prefetch-corpus-write-profile.txt`.
CPU profiles and test binaries are retained outside git under /mnt/oldrog/tmp.
Pinned binary fresh writes improved from 573.7 to 531.1 ns serial in the paired
controls, while four-writer throughput changed little (279.6 to 277.2 ns).
Corpus p99 showed no clear improvement: serial 10.781–11.182 us before versus
10.770–11.622 us after, four writers 14.377–14.989 us versus 14.508–15.048 us.
Raw paired logs use `prefetch-` prefixes.

Pinned concbench probes before the map hints measured 11.78, 11.77, 11.84 Mops/s;
afterward 11.89, 11.96, 11.91, all with zero misses. Loads were 3.39 and
3.37 Mops/s. Native in the same sequential comparison loaded at 4.06 Mops/s and
probed at 12.39, 13.02, 13.28 Mops/s. This supports a small local probe improvement,
while native remains ahead. Corpus identity, parameters and CPU assignments are
preserved in the raw `prefetch-concbench-` logs.
