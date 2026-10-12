# Sharded size accounting

The previous padding experiment left all writers updating one atomic counter.
This change gives each Ref separate, cache-isolated insertion and deletion
tallies. Only its owning caller updates them; references remain single-owner.
Each size-changing write updates one private atomic. Replacements do not
update a tally. Closing a Ref transfers its signed contribution into a retired
total under the reference-registry mutex, so accounting survives handle churn.

`Len()` remains an exact, linearizable snapshot, rather than an approximate sum.
It freezes registration/retirement with that mutex and reads aggregate insertion
and deletion tallies twice. Every component only increases. Equal aggregates
therefore imply every component remained unchanged between its two reads; those
intervals share a common instant between passes. Comparing the net total alone
would not establish this. As with epoch versions, this assumes fewer than 2^64
tally increments between passes. One open Ref needs only insertion/deletion/
insertion reads; no open Refs return the retired total.

The tradeoff is deliberate: `Len()` changes from a single atomic load to a
mutex-protected O(open Refs) scan that may retry during writes. The snapshot
allocates no memory, regardless of reference count. On amd64 each epoch
registration grows from 64 to 136 bytes before allocator rounding, with the
tallies isolated from both the epoch state and neighboring allocations. Per-key
allocation behavior is unchanged. The native C API has no matching global
length counter.

## Initial measurements

2026-10-10, Threadripper 3960X, Go 1.26.4-pont-onethread. Baseline is `d5f955e`.
Use CPUs 0–3, GOMAXPROCS=4, one verified pinned Ref per writer. Serial writers
use CPU 0. Benchmark inputs and operation counts are unchanged. The following
are medians of five alternating baseline/sharded process pairs, N=1,048,576,
unclocked ns/write:

| Scenario | Baseline | Sharded |
| --- | ---: | ---: |
| Serial fresh | 548.1 | 536.5 |
| Four-writer fresh | 279.0 | 267.1 |
| Serial replacement control | 206.9 | 208.2 |
| Four-writer replacement control | 67.80 | 59.21 |

Fresh writes improved approximately 2% serial and 4% with four writers.
Replacement writes do not change any count, so their variation must not be
attributed directly to removal of a shared count update. Five alternating
million-key concbench load pairs measured median 3.37 versus 3.49 Mops (~3.6%).
The initial broad benchmark batch was noisier; paired runs are the comparison
used here. In a diagnostic loader profile, samples around the count update
fell from about 12% to 3% of loader user cycles. Structural publication/locking
and sorting remain substantial costs; this does not close the native C gap.

Five alternating clocked million-operation runs give these median p99 values
(ns, clock overhead included):

| Scenario | Baseline | Sharded |
| --- | ---: | ---: |
| Serial fresh | 7775 | 7564 |
| Four-writer fresh | 9468 | 9388 |
| Serial replacement control | 561 | 511 |
| Four-writer replacement control | 551 | 531 |

Run-to-run ranges overlap substantially. These do not establish a p99 win.
The final allocation-free aggregate snapshot changes only Len's cold path;
the initial write comparisons above used the same private tally update path
with a per-Ref snapshot list. Final-version checks are recorded separately.

## Final implementation check

The allocation-free snapshot is committed as `236231c`. Three additional
alternating baseline/final pairs gave median fresh-write times of 543.3 versus
529.1 ns serial and 283.1 versus 266.5 ns with four writers (~6% improvement).
The replacement control remained variable: serial medians were 193.6 versus
228.1 ns, with final runs ranging from 193.3 to 263.4 ns; four-writer medians
were 61.41 versus 60.74 ns. Replacements never update the counters. The earlier
five-pair serial control was approximately unchanged; these measurements do
not establish a general replacement-write improvement or regression cause.

Final `Len()` medians with 0, 1, 4 and 64 open references were 15.22, 34.81,
107.1 and 1193 ns respectively, all with zero allocations. The original shared
atomic took approximately 0.53 ns in this microbenchmark. Frequent length
queries therefore pay a substantial cost for removing the shared write counter.
The earlier snapshot-list prototype took about 2.7 us with 64 references.

A separate four-worker probe control measured 11.81–11.82 Mops before versus
11.87–11.95 Mops after, with zero misses. These short runs are a regression
check, not evidence of a read optimization. The final load measured 3.50 Mops.

Raw logs are in [results](results): `counter-load.txt`, `counter-paired.txt`,
`counter-latency-paired.txt`, `counter-final-paired.txt`, `counter-len-before.txt`,
`counter-len-final.txt`, and `counter-probe-*.txt`. Initial broad write/latency
batches are also retained as `counter-writes-*.txt` and `counter-latency-*.txt`;
paired comparisons above address their substantial run-to-run variation.
The sharded loader profile report and annotated insert are
`counter-sharded-load-profile-report.txt` and `counter-sharded-insert.annotate`.
The baseline profile is `profile-go-load-1.txt`.

## Reproduction and correctness

Build the benchmark module separately and run baseline/variant binaries
sequentially, alternating their order:

```sh
go -C go/bench test -c -o /tmp/writes.test
WORMHOLE_BENCH_N=1048576 taskset -c 0-3 /tmp/writes.test -test.run '^$' -test.bench '^BenchmarkWrites(Parallel)?$/^(Fresh|Update)$/^Wormhole$' -test.benchtime=1048576x -test.count=1 -test.cpu=4
WORMHOLE_BENCH_N=1048576 taskset -c 0-3 /tmp/writes.test -test.run '^$' -test.bench '^BenchmarkWriteLatency$/^(Fresh|Update)$/^Wormhole$/^Workers(1|4)$' -test.benchtime=1048576x -test.count=1 -test.cpu=4
go -C go test -c -o /tmp/map.test
taskset -c 0 /tmp/map.test -test.run '^$' -test.bench '^BenchmarkMapLen$' -test.benchtime=1s -test.count=3 -test.cpu=1
```

Count tests exercise positive/negative contributions across handle closure,
range deletion, repeated registration/retirement, and concurrent transfers
whose true aggregate remains between zero and one. Plain/native differential
and race/native suites, vet, and benchmark-module race tests pass. ARM64
compilation checks the portable atomics path. Exact size accounting does not
change native hash placement, key ordering, split/merge rules, or epoch safety.

Final verification also passed ten-second map and range fuzz campaigns (1504
and 958 executions), full plain/native and race/native suites, vet, nested
benchmark-module race tests, and an ARM64 test-binary cross-compilation.
