# Comparing native C and Go

Use the same Linux `perf` sampler for both implementations, with optimized
builds and debug information. Keep C's LTO/inlining enabled: disabling inlining
would change the implementation being compared. The analyzer resolves C inline
frames with `addr2line` and Go source positions with `go tool addr2line`.

From the repository root, with a writable Go cache and the external dictionary:

```sh
export GOCACHE=/mnt/oldrog/tmp/wormhole-go-cache
# This custom Go build omits addr2line; use the installed standard Go reader.
export GO_ADDR2LINE=/mnt/oldrog/usr/local/go1.23.5/pkg/tool/linux_amd64/addr2line
python3 go/bench/profile_compare.py --out /mnt/oldrog/tmp/compare-probe
python3 go/bench/profile_compare.py --out /mnt/oldrog/tmp/compare-safe --safe
python3 go/bench/profile_compare.py --out /mnt/oldrog/tmp/compare-load --phase load --repeat 3 --frequency 1999
```

Defaults are one million identical six-word keys, one loading writer and four
probe readers on CPUs 0–3; change `--cpus` for the machine being measured. Native workers inherit
the process mask and pin themselves; Go verifies each worker's affinity.
Each capture runs sequentially. Do not run tests or other profiles concurrently
with capture. Higher-frequency write profiles perturb these short load phases:
use separate unprofiled runs for throughput claims.

The script builds a temporary copy of the native benchmark. It adds exact probe
counts after each timed round; `--safe` substitutes `whsafe_probe` for
`wormhole_probe`; `--phase load` omits probe rounds. Library sources and
optimization flags remain unchanged. Go's `-load-only` exits after the same
single-writer load. All binary profiles/build artifacts stay outside git.
Historical four-writer results below are archival and are excluded from current
optimization decisions. Write comparisons now use one writer.
Go's frame pointers provide call stacks; optimized C uses DWARF unwind data.
Both sample the same user-cycle event. Profiled throughput is diagnostic only.

Reports separate samples under loader/prober call stacks from corpus creation,
cleanup, and background runtime work. Each sampled cycle is counted once;
function percentages are exclusive, not overlapping cumulative costs. Counts
are sampling estimates, not exact hardware totals. Sampling skid means a stall
can be attributed to the instruction following the load/atomic that caused it.
Background GC is reported under `other`, so loader-only CPU totals do not
represent all CPU consumed during loading. The hardware event excludes kernel
cycles; Go CPU/block/mutex profiles below additionally expose syscall and wait
costs. Both drivers use time-based probe seeds and concurrent loading, so the
corpus/distribution match but query sequences and split interleavings differ.
Inspect disassembly as well as
function percentages:

```sh
perf annotate -i /mnt/oldrog/tmp/compare-load/go-0.perf --stdio --symbol 'github.com/glycerine/wormhole/go.(*Ref).insertLocked'
perf report -i /mnt/oldrog/tmp/compare-probe/native-0.perf --stdio --no-children --sort symbol
```

Initial matched profiling (2026-10-10, implementation `cb05fee`) reproduced
native probes at 13.02/13.34/13.31 Mops and Go at 11.95/11.87/11.89 Mops.
Native's automatically parked API measured 12.56/12.89/13.08 Mops in another
capture: reference lifetime alone does not explain the gap.

Go's probe samples concentrate in leaf matching (~38%), routing and locking
(~18% exclusive in `lockLeaf`), and metadata lookup/SIMD/string comparison.
Native's LTO folds most probe code into `kv_probe_worker`; annotate that symbol
rather than interpreting its ~71% exclusive total as workload generation.
Both implementations stall on leaf locks, hash slots, and record headers.

An initial experiment moved leaf-prefetch address calculation into assembly to
remove Go's earlier nil-check load. Separate unprofiled runs did not improve:
baseline 11.99/11.58/11.75 versus experiment 11.36/11.82/11.49 Mops. Reverted.
Removing one sampled stall can merely move it to the next dependent access.

The write comparison exposed two layout differences worth measuring next:
Go's leaf capacity check loads the count from the end of the leaf; C keeps it
in the header. Go also updates a global exact length counter on every insert;
this counter shares a cache line with metadata publication. In one write
capture, the count atomic accounts for ~54% of `insertLocked` samples and the
capacity check ~39%; together that function accounts for ~18% of loader cycles.
C has no matching global length-counter update. These observations identify
experiments, not a complete causal decomposition of the throughput difference.

## Results and rejected experiments

The completed harness run (`profile-{native,go}-probe-run.txt`) counted
114,730,000 native probes and 103,840,000 Go probes. User-cycle sampling under
probe worker stacks estimated 150.49 billion C cycles and 149.11 billion Go
cycles: approximately **1312 versus 1436 user cycles/probe**, a 9.5% Go overhead
in this capture. These are sampled estimates; CPU frequency and workload seed
variation apply. Profiled rates are not used as unprofiled throughput claims.

The selected routing functions (metadata get/peek/child, tag-mask matching,
prefix search/hashing and descent) account for about 29% of Go's probe cycles,
versus about 12% in C: roughly 420 versus 155 user cycles/probe. External
`memcmp`/`memequal` bodies are excluded from this grouping, so these are not
complete routing costs. C inlines SIMD/short CRC operations into its routing;
Go repeatedly crosses ABI wrappers for its assembly helpers. Profiles also
show dependent loads in metadata key comparison. Reducing those calls and
dependencies is a better probe target than changing the leaf prefetch alone.
This remains a target, not proof that inlining alone recovers the whole gap.

Three higher-resolution load captures per implementation are retained as
`profile-{native,go}-load-{0,1,2}.txt`. C spends roughly 9–11% of loader user
cycles spinning in `qsbr_wait`, plus time in its write spin locks. Go's loader
user-cycle total can be smaller while wall throughput is worse: its scheduler,
kernel calls and blocking waits need separate examination.

To collect load-only CPU and contention evidence:

```sh
go -C go build -o /tmp/wormhole-concbench ./cmd/concbench
taskset -c 0-3 /tmp/wormhole-concbench -load-only -loadprofile /tmp/load words.txt 1000000 4
# Use a standard Go installation's pprof if the custom toolchain omits it.
/mnt/oldrog/usr/local/go1.23.5/bin/go tool pprof -top /tmp/wormhole-concbench /tmp/load.block
/mnt/oldrog/usr/local/go1.23.5/bin/go tool pprof -top /tmp/wormhole-concbench /tmp/load.mutex
```

One diagnostic load took 289 ms and accumulated **102 ms of worker blocking on
metadata publication** and **36 ms on leaf writer locks**. The main goroutine's
WaitGroup wait is normal coordination and excluded from those numbers. These
are summed worker waits, not additional wall time; do not add mutex-profile
delay to block-profile delay. Full contention sampling itself perturbs execution.
The accompanying coarse CPU profile recorded 90 of 750 sampled CPU-ms in
`runtime.futex`. These observations prioritize the serialized publication path,
grace-period wait policy, and scheduler wakeups for write throughput/tails.
They do not establish that replacing Go mutexes wholesale with spin locks
would be safe or beneficial. Raw reports: `profile-go-load-{cpu,block,mutex}.txt`.

The layout experiments did **not** improve performance and were reverted.
Five interleaved pinned runs, median million-key load Mops:

| Layout | Mops |
| --- | ---: |
| Baseline | 3.38 |
| Isolated global length counter | 3.33 |
| Isolated counter and leaf count in header | 3.34 |
| Native C control | 4.22 |

Counter isolation also gave no repeatable probe improvement; moving the leaf
count worsened the probe run. A hot atomic instruction is not, by itself,
evidence that false sharing explains the throughput gap. This experiment
covered four workers; it does not rule out sharing costs at higher concurrency.
Isolation used 64 padding bytes before the 8-byte atomic and 56 after it, giving
the counter its own 64-byte cache line regardless of the Map's alignment.
It did not shard the counter: all writers still contended on the same atomic.
See `profile-layout-ablation.txt` for all load runs.

Go now explicitly collects discarded dictionary/text fixtures before timing
the load, matching C's pre-load dictionary free. In five interleaved runs,
baseline Go measured median 3.34 Mops, pre-load GC 3.43, diagnostic GOGC=off
3.25, and C 4.26. The gap remains and disabling GC did not improve it.
This is benchmark setup hygiene, **not a library optimization or demonstrated
speedup**. Automatic GC remains enabled in the regular benchmark. Raw runs:
`profile-fixture-gc-ablation.txt`.

A subsequent experiment **does** shard size accounting by Ref (`236231c`).
Pinned fresh-write throughput improves modestly, while exact `Len()` becomes
a mutex-protected scan. See [COUNTER.md](COUNTER.md) for the design, final checks,
profiles and paired measurements; the rejected padding experiment above is
retained as historical evidence.

Further write work retains bounded epoch polling and a version-validated
prefix finger. The publication wait dropped substantially in a subsequent Go
block-profile capture. Fresh four-writer throughput and p99/p99.9 now lead C
in new paired controls, while ascending tails remain behind. The matched
per-Put harness, exact checkpoints, profiles and reverted variations are in
[WRITE_OPTIMIZATION.md](WRITE_OPTIMIZATION.md).
