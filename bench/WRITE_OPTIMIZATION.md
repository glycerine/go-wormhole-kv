# Write optimization against native C

Goal: match or exceed original C write throughput and latency. This remains
open; matching average throughput alone is insufficient.

## Matched measurement

`write_compare.py --out /tmp/write-compare` builds optimized original C library
sources with the same flags as the perf harness. A temporary copy of concbench
adds per-Put timestamps, checks every Put result, and omits probes. Go concbench
uses `-load-only -load-latency` for the corresponding clocked run. Both load the
same glibc-generated million six-word keys, split into four contiguous worker
partitions on physical CPUs 0/1/2/3. Go verifies single-CPU affinity; native uses
its existing fork/join pinning. Native normally keeps its Ref active; `--safe`
selects its automatically parked API as an additional control. C copies kv
records; Go retains immutable key strings. Both prepare hash tags outside the
timed region. GC remains enabled in Go.

Throughput runs have no per-operation clocks. Separate clocked runs sample every
Put and merge samples before reporting p50/p95/p99/p99.9/max; timestamp overhead
is included, never subtracted. Current drivers also report an empty clock-pair
control pinned to CPU 0 after loading. Initial logs predate that control and the
separation of clocked/unclocked Go loops, so final controls must be rerun.
Process startup, corpus creation, sample allocation/sorting and teardown are
outside the load timer. Worker launch/registration/pinning/join are included,
as in original concbench. Sample pages are first touched during clocked loads.
This measures fresh random corpus loads; replacement and ascending traces
require separate controls before making broader write-performance claims.

## Bounded grace-period polling

Profiled Go load waits on metadata publication, with scheduler/futex work and
publication serialization. Previously an old epoch immediately called Gosched,
even though most metadata readers finish in hundreds of nanoseconds. The first
experiment polls an atomic epoch up to 256 times before yielding. It retains
the same protection/publication protocol and still yields for preempted readers,
including with one P. No locks are replaced and registration remains frozen
under the existing registry mutex.

Five alternating pinned pairs (Go 1.26.4-pont-onethread, Threadripper 3960X,
2026-10-10) measured these medians:

| Measure | Immediate yield | Poll 256 |
| --- | ---: | ---: |
| Unclocked load, Mops/s | 3.49 | 4.15 |
| Clocked load, Mops/s | 3.28 | 3.97 |
| Clocked p99, ns | 12143 | 11081 |
| Clocked p99.9, ns | 124717 | 64272 |

This is a ~19% throughput gain and a smaller, but still substantial, long tail.
Raw pairs: `results/epoch-poll-256.txt`. Initial three alternating C/Go controls
before this change measured native 4.27 versus Go 3.56 Mops/s; native clocked p99
15099 ns and p99.9 31089 ns versus Go 12013 and 121270 ns. Those controls used
an earlier instrumented Go loop; see `results/write-native-go-initial.txt`.
Thus throughput is close, Go p99 is lower, and Go p99.9 remains worse. This
checkpoint does not establish the goal.

The retained polling change passes full native and race/native tests, vet,
benchmark-module race tests, and repeated epoch/snapshot race tests with
GOMAXPROCS=1. Existing tests verify blocked old/entering readers, closure during
a wait, and repeated reuse of both mutable metadata buffers.

## Polling bound and broader controls

A three-repeat sweep of 64/256/1024/4096/16384 polls selected 1024. Unclocked
medians were 3.62/4.32/4.99/4.96/4.96 Mops/s respectively. Clocked p99.9 was
105951/68470/19096/18926/19287 ns. Longer bounds brought no meaningful gain;
1024 is the smallest measured bound reaching this plateau. The final loop
retains a bounded fallback to Gosched. Raw sweep: `results/epoch-poll-sweep.txt`.

Five new alternating C/Go pairs at 1024 polls gave these median results:

| Four-worker fresh corpus | C | Go |
| --- | ---: | ---: |
| Unclocked Mops/s | 3.62 | 3.97 |
| Clocked p50, ns | 501 | 511 |
| Clocked p95, ns | 1042 | 1012 |
| Clocked p99, ns | 17233 | 11632 |
| Clocked p99.9, ns | 37551 | 23364 |

Absolute rates were lower for both implementations than the earlier sweep.
Two of the five Go throughput runs were slower than the neighboring C controls;
median improvement is ~10%, not a guarantee for every process. Measured empty
clock-pair batch costs were roughly 45 ns in C and 73 ns in Go; costs are
retained in the timings. Maxima were noisy and Go's median maximum was worse
(~2.17 ms versus ~1.83 ms). Raw: `results/write-native-go-poll1024.txt`.

Additional three-pair controls use the same corpus. Single-worker processes
and runtimes use CPU 0 and one P; four-worker runs use CPUs 0–3 and four Ps.
Updates preload the same keys serially outside the timer, collecting preload
Go garbage before timing. Ascending sorts the corpus outside the timer and
uses one worker, preserving global order. Flags are `--workers 1 --cpus 0` and
`--workload fresh|update|ascending` on write_compare.py.

| Scenario | C unclocked Mops/s | Go unclocked Mops/s | C p99 ns | Go p99 ns | C p99.9 ns | Go p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Fresh, one worker | 1.56 | 1.75 | 12022 | 10400 | 22583 | 19497 |
| Update, one worker | 3.19 | 3.35 | 732 | 762 | 932 | 1012 |
| Update, four workers | 8.91 | 10.71 | 771 | 691 | 1002 | 922 |
| Ascending, one worker | 3.33 | 3.01 | 2695 | 3306 | 5791 | 6903 |

Raw files are `results/write-native-go-{fresh1,update1,update4,ascending1}.txt`.
This exposes remaining ascending throughput/tail and serial update latency
work. The objective is still open.

A new diagnostic load block profile accumulates 8.46 ms waiting on metadata
publication and 11.78 ms on leaf writers, versus the earlier 102/36 ms capture.
This supports the reduced scheduler handoff hypothesis; captures are separate,
instrumented runs and must not be treated as precise causal savings. Reports:
`results/epoch-poll1024-{block,cpu,perf}.txt`. Correctness checks pass again with
1024 polls, including the race/native suite and single-P epoch/snapshot tests.

## Prefix-filtered write finger

The first finger prototype copied full lower/upper bounds on every routed Put
and compared them before each next write. Ascending throughput improved, but
random replacement throughput fell about 11–15%, with worse latency. Rejected;
raw pairs remain in `results/write-hint-rejected.txt`. Its fresh throughput
control was unusually variable and is not used to claim a speedup.

The retained candidate stores only the last routed leaf, its protected version,
and the first four key bytes. Matching prefixes permit an attempted cached-leaf
lock. Under that lock, require an unchanged version and verify the key against
the leaf's actual anchor and next anchor (exclusive). Otherwise fall back to
normal metadata routing. Short keys use normal routing. A cached-leaf hit never
accesses mutable metadata and therefore needs no reader epoch. GC retains the
leaf, including a stale retired leaf; every interval-changing split/merge bumps
its version before releasing its lock. Close clears the cached pointer. This
uses the existing native placement, split/merge and lock-order rules.

Three alternating pinned baseline/candidate pairs gave:

| Scenario | Poll-only Mops/s | Prefix finger Mops/s | Poll-only p99 ns | Finger p99 ns | Poll-only p99.9 ns | Finger p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Fresh, four workers | 4.89 | 5.16 | 10510 | 10520 | 19076 | 19267 |
| Ascending, one worker | 3.24 | 4.02 | 3086 | 3015 | 6022 | 5881 |
| Update, one worker | 3.96 | 4.28 | 621 | 621 | 781 | 782 |
| Update, four workers | 13.35 | 13.91 | 651 | 651 | 882 | 892 |

Ascending clocked p50 improves 280→220 ns; random latency controls are essentially
unchanged. Raw: `results/write-prefixhint-pairs.txt`. The differing absolute
rates versus earlier batches reinforce the need for alternating controls.
The hint-specific tests deliberately retain a stale cached leaf through a
split and a retirement merge, then write a moved key with the same prefix;
they also check the exclusive upper bound and clearing references on Close.
Full native differential/race suites verify the actual routing and publication
protocol with the fast path enabled. Ascending split-tail work remains open;
a new direct C comparison is needed before claiming parity there.

## Retained implementation versus C, after the write finger

Five alternating process pairs per scenario, implementation `c5cf928`, with
identical corpus and CPU pinning as above:

| Measure | C fresh, four workers | Go fresh, four workers | C ascending, one worker | Go ascending, one worker |
| --- | ---: | ---: | ---: | ---: |
| Unclocked Mops/s | 4.18 | 4.95 | 3.65 | 3.94 |
| Clocked Mops/s | 3.74 | 4.37 | 3.05 | 2.86 |
| Clocked p50, ns | 471 | 470 | 211 | 231 |
| Clocked p95, ns | 942 | 892 | 441 | 401 |
| Clocked p99, ns | 16391 | 10870 | 2635 | 3276 |
| Clocked p99.9, ns | 34265 | 19337 | 5471 | 6693 |

The main four-writer random corpus load now leads C by ~18% in throughput,
~34% at p99 and ~44% at p99.9. Ascending leads by ~8% in unclocked throughput,
but has ~24% worse p99 and ~22% worse p99.9. Its clocked throughput is also
lower. The full write throughput/latency goal therefore remains open. Raw:
`results/write-current-native-go-{fresh4,ascending1}.txt`. These are empirical
results for this host/toolchain and trace, not guarantees for arbitrary inputs
or every repetition. Sample maxima remain susceptible to scheduling noise.

## Rejected split and prefix experiments

All of the following were restored before the current C comparisons. None is
part of the retained implementation:

- Inverse sorted-index rebuilding preserved native magnet placements in the
  differential suite, but added copying overhead. Three pairs gave ascending
  3.92→3.83 Mops/s and p99 3106→3567 ns; random fresh throughput was flat at
  5.10 Mops/s and p99 worsened 10631→11662 ns. Raw:
  `results/write-bulk-rejected.txt`.
- An optional AVX2 shift path matched native's 32-byte vectors, checked CPU/OS
  support and retained SSE2 fallback. Shift/native-leaf tests passed, but five
  pairs gave ascending 3.94→3.96 Mops/s with essentially unchanged p99, while
  random fresh throughput fell 5.09→5.02 and p99.9 rose 19968→22533 ns. Rejected:
  `results/write-avxshift-rejected.txt`. A combined AVX2/single-entry-copy
  diagnostic likewise failed to improve both workloads and was discarded;
  it is not evidence about isolated single-entry copying.
- Eight-byte XOR prefix comparisons removed scalar byte work but did not improve
  the target. Three pairs regressed ascending 2.85→2.61 Mops/s and p99
  3377→3487 ns; random controls were mixed. Raw:
  `results/write-wordprefix-rejected.txt`.
- A portable eight-byte equality loop retained inlining (compiler cost 53),
  but still did not establish a consistent win: ascending 3.75→3.68 Mops/s,
  random fresh 3.17→3.26, with random p99 17092→17853 ns. The batch ran slower
  overall; alternating controls remain the relevant comparison. Raw:
  `results/write-prefix8-rejected.txt`.

Matched single-worker ascending perf reports are
`results/write-ascending-{native,go}-profile.txt`. Only about 500 loader samples
were collected per implementation; use them to identify targets, not to infer
precise tail causality. Both spend many samples stalled on the first input
record/key read; Go additionally pays assembly call/setup and GC-visible pointer
copy costs. The first-input stalls can move when code is rearranged. Splits
retain their original rebuilding and prefix algorithms after these experiments.

The retained `c5cf928` implementation also passed new ten-second map/range fuzz
campaigns (4037/2716 executions) and ARM64 cross-compilation. No experimental
library edits remain after the rejections above.

## Packed split rebuilding and shallow metadata references

The inverse-index prototype above still copied GC-visible pointers on every
magnet insertion. A revised builder simulates the same sorted-entry insertion
sequence using 32-bit tag/rank words in stack storage. Moving these words needs
no pointer barriers or ss repairs. Once placements are final, it materializes
pointers, tags and sorted indices in the actual leaf. A two-word occupied bitmap
records the newly filled gap per insertion, then visits only live slots during
materialization. This avoids a poorly predicted branch for every empty slot.
Pointers stay ordinary Go pointers and all pointer writes retain GC barriers.
Capacity, anchors, split decisions and exact magnet placements are unchanged.

The initial integer-only builder improved some throughput/p99.9 controls but
regressed ascending p99. Bitmap materialization corrected that regression.
Initial three-way medians were ascending p99 3076 ns original, 3276 ns packed
scan, 2845 ns packed bitmap; p99.9 was 6001/5591/5491 ns. Raw intermediate runs:
`results/write-packedbuild-pairs.txt` and `results/write-packedbitmap-pairs.txt`.

The second change retains direct references to existing empty/one-byte metadata
prefixes in each table. This skips hash-bucket lookup and assembly calls for
these frequently visited prefixes, including root-child routing. Put/erase keep
the references synchronized; expansion/cuckoo relocation preserve node identity.
A hash mismatch or absent cached node uses the original lookup. Protection is
the same table epoch as the underlying metadata. This adds 257 pointers per
table (~4 KiB per map before allocation rounding); no extra metadata nodes or
per-key allocations are introduced.

Five alternating three-way runs give the following medians:

| Scenario/measure | Original finger | Packed bitmap | Plus shallow references |
| --- | ---: | ---: | ---: |
| Ascending one-worker Mops/s | 3.67 | 3.72 | 3.73 |
| Ascending p99 ns | 3326 | 3066 | 3006 |
| Ascending p99.9 ns | 6632 | 6142 | 6052 |
| Fresh four-worker Mops/s | 5.04 | 5.00 | 5.24 |
| Fresh p99 ns | 10801 | 10711 | 10730 |
| Fresh p99.9 ns | 19818 | 18405 | 18636 |

Raw: `results/write-shallow-pairs.txt`. Absolute timings vary between batches;
these changes are assessed against the same-batch alternating controls. A new
matched native comparison is still needed, and ascending parity is not claimed.

Tests compare packed placements against the native-verified ordinary insertion
path at every size 0–128, with random tags and maximal low/high/middle collision
clusters; existing leaf invariants independently check matching and ordering.
Metadata tests cover all 257 shallow prefixes, expansion/relocation, hash
mismatches, deletion/reinsertion and attempts to erase a retired node pointer.
Full native and race/native suites, plain tests, vet, benchmark-module race tests
and ARM64 test-binary compilation pass for the combined implementation.


## Bitmap vacancy searches

Split rebuilding now searches the two occupied words with leading/trailing-zero
operations instead of scanning individual occupied slots. Five alternating pairs
against d762218 gave ascending throughput 4.03→4.02 Mops/s, p99 2825→2665 ns,
and p99.9 5411→5160 ns. Fresh four-worker throughput was 5.12→5.17 Mops/s,
p99 10591→10399 ns and p99.9 18074→17734 ns. Raw:
`results/write-gapscan-pairs.txt`.

The same bitmap is now retained in each leaf for ordinary insertion searches.
Insertion marks the newly filled hole; removal clears the final hole after any
pulling shifts. Packed rebuilding installs its final bitmap. This adds sixteen
bytes of logical storage per leaf and leaves native placement decisions intact.
The invariant checker reconstructs occupancy from tags after operations; benchmark
template copies include the new state without copying mutexes.

Five alternating pairs against the split-only bitmap search gave:

| Scenario/measure | Split-only bitmap search | Persistent leaf bitmap |
| --- | ---: | ---: |
| Ascending one-worker Mops/s | 4.01 | 4.18 |
| Ascending p50 ns | 230 | 220 |
| Ascending p95 ns | 391 | 361 |
| Ascending p99 ns | 2775 | 2675 |
| Ascending p99.9 ns | 5670 | 5410 |
| Fresh four-worker Mops/s | 5.21 | 5.29 |
| Fresh p99 ns | 10319 | 10259 |
| Fresh p99.9 ns | 17574 | 17093 |

Replacement controls were within roughly one percent: single-worker throughput
4.22→4.19 Mops/s, p50 260→261 ns, p99 632→641 ns. Replacements do not use the
bitmap searches. Raw: `results/write-leafbitmap-pairs.txt`.

Full race/native, plain, and native suites pass, as do native vet, benchmark-module
race tests and ARM64 cross-compilation. The allocation check reports one allocation
per fresh insertion and zero per replacement. These paired Go controls establish
the optimization gains; direct C comparisons must be refreshed separately.


## Direct C refresh after leaf bitmap (fce2f83)

Three alternating C/Go process pairs, one million identical keys, CPUs 0–3 for
four workers and CPU 0 for one worker. Original optimized C library, separate
unclocked throughput and clocked per-Put latency runs. Medians:

| Measure | C fresh, four workers | Go fresh, four workers | C ascending, one worker | Go ascending, one worker |
| --- | ---: | ---: | ---: | ---: |
| Unclocked Mops/s | 3.80 | 4.66 | 3.71 | 4.16 |
| Clocked Mops/s | 3.66 | 4.44 | 3.11 | 3.18 |
| p50 ns | 471 | 461 | 210 | 220 |
| p95 ns | 952 | 902 | 431 | 350 |
| p99 ns | 16180 | 10670 | 2595 | 2605 |
| p99.9 ns | 35407 | 19417 | 5551 | 5139 |

Fresh Go throughput leads ~23%, with p99 ~34% and p99.9 ~45% lower.
Ascending throughput leads ~12%; p99 is effectively tied in these short runs
and p99.9 is ~7% lower, but median latency remains ~5% higher. This is close to
ascending parity, not evidence that every write workload/percentile beats C.
Replacement and read comparisons have not been refreshed for this revision.
Raw: `results/write-status-native-go-{fresh4,ascending1}.txt`.


## Prepared-prefix and prefetch experiments

New matched ascending perf captures (4999 Hz, cycles:u; Go frame pointers,
C DWARF unwinding) after fce2f83 collected 1199 Go and 1350 C loader samples.
About 43% of Go loader cycles sample the initial four-byte prefix read; about
46% of C loader cycles sample the initial kv header read. Go split rebuilding
is now about 9% and ordinary insertEntry about 6%; C magnet insertion about
18%. Sampling skid and the short capture limit precise attribution. Reports:
`results/write-bitmap-ascending-{go,native}-profile.txt`.

Caching four prefix bytes in prepared Key padding avoids that initial key-data
read. Alone it improves ascending p50 220→211 ns but regresses random throughput;
raw three-pair controls are `results/write-preparedprefix-pairs.txt`.

A second experiment proves, under the cached leaf lock, whether an entire
four-byte prefix lies in the leaf interval. It reuses that proof only while
the leaf version is unchanged. Partial prefixes still check actual key bounds.
This shortcut alone did not help. Combined with a key-data prefetch before
locking, it gives insertion/allocation work time to overlap the key-data fetch.
Three-way controls are `results/write-prefixcoverage-pairs.txt`.

Prefetch alone was flat on ascending (4.18→4.17 Mops/s); prepared prefix plus
prefetch without the interval proof reached 4.44 Mops/s. Neither established a
broad win. Raw: `results/write-keyprefetch-pairs.txt`.

Five further three-way pairs showed the full combination improving ascending
4.25→5.49 Mops/s and p50 211→151 ns, with fresh four-worker throughput within
one percent (5.36→5.32). However, replacement throughput regressed 4.23→3.82
Mops/s and p99 632→671 ns. Unconditional prefetch is therefore rejected.
A PutKey-level prefix guard restored replacement throughput to 4.23 but added
random overhead (fresh 5.36→5.21 Mops/s, p99 10289→10550 ns); that variant is
also rejected. Raw: `results/write-prefixgated-pairs.txt`.


Moving prefetch inside the leaf-hint branch restored fresh throughput
(5.27→5.29 Mops/s), but replacement still fell 4.29→4.16. Rejected;
`results/write-localprefetch-pairs.txt`.

The retained candidate uses only one PutKey-level prefix comparison. A mismatch
uses the original ordinary routing path, preserving the key-data read before
entering the metadata epoch. A match prefetches key bytes and passes the prepared
prefix into the validated leaf hint. This removes the redundant nil/length checks
from the previous guard; short/empty keys remain valid, with the original routing
fallback, and the amd64 prefetch hint does not fault for nil. Other architectures
and race builds use the existing no-op prefetch convention.

Under the cached leaf lock, the interval proof requires anchor <= prefix and
an absent upper bound or an upper bound lexically beyond the prefix that does
not begin with it. A whole-prefix hit still requires an unchanged leaf version.
The proof is computed lazily once per cached leaf/prefix/version. Partial prefixes
retain full actual-key bounds checks. Every routed miss resets both proof flags.
Prepared Key and Ref use existing padding for the extra fields on 64-bit targets.
No key/value copying, lock or metadata-publication rules change.

Three alternating pinned pairs against fce2f83:

| Scenario/measure | Before | Single prefix guard |
| --- | ---: | ---: |
| Ascending one-worker Mops/s | 4.27 | 5.52 |
| Ascending p50 ns | 211 | 151 |
| Ascending p95 ns | 341 | 311 |
| Ascending p99 ns | 2525 | 2505 |
| Ascending p99.9 ns | 4799 | 4769 |
| Fresh four-worker Mops/s | 5.22 | 5.25 |
| Fresh p50 ns | 441 | 451 |
| Fresh p99 ns | 10239 | 10339 |
| Fresh p99.9 ns | 17353 | 17854 |
| Replacement one-worker Mops/s | 4.28 | 4.28 |
| Replacement p50 ns | 260 | 251 |
| Replacement p99 ns | 631 | 621 |
| Replacement p99.9 ns | 792 | 782 |

The measured win is ascending throughput/median latency. Fresh controls retain
throughput but show small latency costs (p50 +10 ns, p99.9 +~3%); replacement
throughput is unchanged and latency improves slightly. Raw:
`results/write-simplegate-pairs.txt`. New tests check fully covered hints through
splits, partial-prefix exclusive bounds, shorter/longer anchors and binary
zero/0xff prefixes. Direct C comparisons are refreshed separately below.

Full native, race/native and plain suites, native vet, benchmark-module race
checks and ARM64 test-binary compilation pass for the retained single-guard
implementation. The race build retains the coverage validation and uses the
portable no-op prefetch helper.


## Five-workload direct C refresh (0a19552)

Five alternating pairs per workload, one million identical corpus keys, pinned
CPU 0 for serial workers and CPUs 0–3 for four workers; separate unclocked
throughput and clocked latency runs. Medians (C / Go):

| Scenario | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh, one worker | 1.72 / 2.11 | 371 / 330 | 791 / 741 | 10781 / 9158 | 20158 / 16342 |
| Fresh, four workers | 4.23 / 5.14 | 451 / 471 | 892 / 931 | 15560 / 10931 | 32191 / 20639 |
| Replacement, one worker | 3.45 / 3.98 | 300 / 261 | 521 / 481 | 671 / 651 | 831 / 821 |
| Replacement, four workers | 10.57 / 13.89 | 321 / 280 | 571 / 481 | 721 / 651 | 941 / 861 |
| Ascending, one worker | 3.69 / 5.15 | 201 / 151 | 421 / 321 | 2525 / 2565 | 5140 / 5019 |

Go leads unclocked throughput throughout, by ~15–40%. All measured latency
percentiles lead C on serial fresh and replacement workloads. The remaining
raw-latency gaps are fresh four-worker p50/p95 (~4% higher) and ascending p99
(~1.6% higher). They are small but prevent claiming every measured percentile
matches or beats C. Timer overhead remains included: Go's time.Now/time.Since
control costs ~70–76 ns per full pair versus C ~43–44 ns in the fresh-four batch,
with empty measured intervals mostly 20–30 ns Go and 20 ns C. No subtraction or
inferred correction is used. Clocked aggregate rates are instrumentation-sensitive
(e.g. serial replacement 2.96 C / 2.91 Go Mops/s despite faster unclocked Go).

Raw: `results/write-post-prefetch-native-go-{fresh1,fresh4,update1,update4,ascending1}.txt`.
A new matched random four-writer perf capture has 1342 Go and 1751 C loader
samples. Go metadata lockLeaf/get/peek/longestPrefix/child plus tag matching
remain substantial, and RWMutex unlock alone accounts for ~8.5% of Go sampled
loader user cycles. C samples spend more on allocation, magnet insertion and
QSBR spinning. Reports: `results/write-post-prefetch-fresh4-{go,native}-profile.txt`.
These short sampled captures identify targets rather than exact tail causality.


## Rejected combined metadata tag probes

A combined SSE2 helper shared tag broadcast and call setup between the two
optimistic buckets, retaining first-bucket short circuit and scalar race/portable
fallback. Randomized mask/oracle tests passed, including aliases and zero tags.
However, three alternating pairs regressed fresh four-worker throughput
5.24→4.94 Mops/s and serial replacement 4.33→3.80, with replacement p50
260→290 ns. Rejected: `results/write-dualpeek-rejected.txt`.

A branchless two-load/POR version removed that branch. It was mixed, with fresh
5.24→5.20 Mops/s and modestly better clocked tails, but replacement 3.65→3.56
Mops/s and p99 631→642 ns. Ascending remained approximately flat. Rejected:
`results/write-dualbranchless-rejected.txt`. nm still shows the Go peek method
in every binary, so these results do not establish an inlining regression.
All combined-probe source and test edits were restored to ac39487.


## Additional retained-code checks

After restoring the rejected probes, native five-second fuzz campaigns passed
for map/range/leaf/metadata with 2421/1600/891/2057 executed inputs, respectively,
using four workers. Together with the full native/race checks above, these test
the retained prefix proof, occupancy and shallow metadata state.

Three alternating read-control pairs, two one-second rounds each, four verified
pinned workers on CPUs 0–3, measured median 11.70 Mops/s at fce2f83 versus 11.775
at 0a19552, with zero misses. This is a short regression check, not evidence
that reads now match C. Raw: `results/post-prefetch-read-control.txt`.

The ordinary-Put binary-key Tidwall throughput matrix was refreshed with three
repetitions of 1048576 writes, GOMAXPROCS=4. The serial process is restricted to
CPU 0; the parallel process to CPUs 0–3, with verified writer pinning. Results
are in README.md and `results/post-prefetch-tidwall-{write,parallel}-1m.txt`.
This refresh measures throughput/allocation only; earlier Tidwall latency tables
remain historical. Every replacement benchmark still reports zero allocations.


## Rejected atomic leaf lock and hot-key qualification

The native writer lock uses fewer atomic operations than sync.RWMutex, and the
current Go loader samples spend ~8.5% in RWMutex.Unlock. An experimental private
lock used a writer reservation bit and reader count in one atomic word. Writers
gated new readers before draining existing ones; waiters bounded their polling
and called Gosched. Layout padding preserved the original 24-byte mutex footprint.
Race helpers hid bookkeeping atomics and explicitly modeled writer-to-reader,
reader-to-writer and writer-to-writer synchronization, as sync.RWMutex does.
The full native suite and focused race/epoch/snapshot/coverage tests passed.
Direct lock tests exercised mixed readers/writers and single-P reader draining.

Three pinned ordinary corpus controls at 256 polls looked promising: fresh-four
throughput 5.14→5.23 Mops/s, replacement-one 4.22→4.39, ascending-one 5.31→5.56.
Fresh p50 451→441 ns and ascending p99 2535→2485 ns. Raw:
`results/write-bitlock-pairs.txt`.

A new concbench -hot-keys diagnostic repeats the first H prepared corpus keys
in the N-operation trace, outside timing. It retains the original corpus hash,
logs the deterministic remapping separately, and checks final Len==H. The default
trace is unchanged; ascending+hot is rejected. Both old/new binaries use the
same driver. One-key/64-key traces force leaf contention that the million-key
random workload rarely sees. Four writers use CPUs 0–3; sixteen writers deliberately
oversubscribe those same four CPUs, with GOMAXPROCS=16 as the original driver
sets it. No native-C parity claims are made from this Go-only diagnostic.

At 256 polls, four-writer one-key throughput regressed 23.27→20.03 Mops/s and
p99.9 7424→21331 ns. Sixteen writers regressed p99 1212→25960 ns and p99.9
91574→177407 ns. The 64-key throughput control regressed 18.70→12.99 Mops/s.
Raw: `results/write-bitlock-hot-pairs.txt`.

A same-batch 16/64/1024 polling sweep improved some throughput, but none preserved
all contention tails. For four writers, baseline/16/64/1024 throughput medians
were 26.37/51.46/32.10/12.15 Mops/s; p99.9 21611/25028/18656/17504 ns. For sixteen
writers the corresponding throughput was 24.53/47.94/29.29/9.57, p99
1253/28885/602/1393 ns, and p99.9 85804/154594/161999/130499 ns. Hot-key tails
vary across batches, so same-batch controls matter. Raw:
`results/write-bitlock-poll-sweep.txt`.

The writer-fast-path gains do not justify these contention costs. All atomic
leaf-lock code/tests were restored; sync.RWMutex remains in production. The
rejected prototype is archived in `results/leaf-bitlock-rejected.patch`. The
hot-key diagnostic is retained to qualify future locking changes.


## Rejected serialized point lookups with shared long readers

A second prototype retained standard Go parking primitives. A per-leaf mutex
serialized short Get/Probe operations and writers. Iterators and callbacks
registered shared lifetimes in a separate RWMutex; writers took that RWMutex
only while registration credits remained. The first credit-retirement version
reacquired the point mutex after dropping a read lock, deadlocking iterator
handoff against a left-to-right merge. TestConcurrentIterators caught it before
benchmarking. Atomic retirement, with an explicit per-writer shared-lock flag,
removed that cycle; the full native suite and focused race suites then passed.
A deterministic handoff regression test and mixed point/shared/write tests also
passed. The corrected prototype is archived in `results/leaf-pointmutex-rejected.patch`.

Ordinary corpus write controls were mixed: three pairs showed fresh-four
5.15→5.28 Mops/s, replacement-one 4.19→4.17, ascending-one 5.36→5.54. Ascending
p99 improved 2524→2475 ns, but fresh p50 rose 451→461 ns. A further three-way
layout control measured fresh 5.20→5.31 Mops/s and p50 451→441 ns; the alternative
field layout gave 5.34 Mops/s but worse clocked tails. These varying absolute
latency results do not establish a robust common-case improvement. Raw:
`results/write-pointmutex-pairs.txt`, `results/write-pointlayout-pairs.txt`.

Hot writers improved: four writers/one key 24.77→36.30 Mops/s, p99.9
17533→9338 ns; four writers/64 keys 16.96→20.49, p99.9 28905→27112 ns. Sixteen
writers on four CPUs improved throughput 23.63→30.99 but regressed p99.9
105420→156558 ns. Raw: `results/write-pointmutex-hot-pairs.txt`.

The decisive rejection was read contention: three alternating pairs, two
one-second rounds each, gave ordinary million-key reads 11.99→12.055 Mops/s,
but one-key reads 21.625→13.005 Mops/s (~40% worse). The 64-key control showed a
similar regression. All reads had zero misses. Raw:
`results/read-pointmutex-pairs.txt`. All production locking/routing source and
prototype tests were restored; the existing shared read concurrency is retained.

## Preliminary one-byte search seed

Starting optimistic metadata search from the exact cached one-byte prefix
passed the full native suite. Three alternating pairs gave fresh-four
5.26→5.33 Mops/s, replacement-one 4.24→4.25 and ascending-one 5.45→5.50;
clocked ascending throughput was slightly lower. This is a small, mixed result,
not a retained win. The code was set aside before locking experiments; raw:
`results/write-prefixseed-pairs.txt`.


## Short-prefix bytes in metadata padding

Cache the first up-to-four prefix bytes as a little-endian word in the four
padding bytes after metadata.hash. Metadata remains 64 bytes on amd64, with
unchanged pointer offsets and no added GC pointer. A candidate of matching
length up to four bytes can be verified without loading its separate key text;
longer prefixes retain exact string comparison. Child lookup packs the parent
and child byte similarly. Length checks distinguish binary NUL padding. The
word is initialized by put before publishing a new node; table expansion moves
existing nodes directly without calling put. Trie topology, hashes, bucket
placement, and structural publication remain unchanged.

Tests force tag/hash collisions between binary prefixes of different lengths,
including trailing NULs, and exercise child tokens 0/1/127/255 and the long-key
fallback. The direct-bucket reciprocal-cycle fixture initializes the word
explicitly because it bypasses put.

An initial three-pair fresh throughput batch was negative (5.48→5.21 Mops/s),
while replacement throughput and fresh latency improved. A subsequent five-pair
three-way comparison measured committed control / unconditional cache /
conditional initialization as follows:

| Scenario | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh, four workers | 5.31 / 5.42 / 5.32 | 461 / 441 / 471 | 881 / 852 / 881 | 10420 / 10370 / 10660 | 17834 / 17493 / 19006 |
| Replacement, one worker | 4.20 / 4.40 / 4.34 | 261 / 250 / 251 | 481 / 441 / 451 | 651 / 611 / 621 | 821 / 781 / 791 |
| Ascending, one worker | 5.39 / 5.38 / 5.37 | 151 / 151 / 151 | 320 / 320 / 320 | 2555 / 2535 / 2535 | 4929 / 4850 / 4849 |

The conditional variant adds a zero-word check before initializing the cache;
it did not improve on unconditional initialization. Fresh throughput is mixed
between batches; its latency improvement and replacement improvement are more
consistent. Three alternating read pairs, two one-second rounds per process,
gave control/cache 11.955/12.175 Mops/s on the million-key trace,
22.09/21.56 on one key, and 21.225/21.19 on 64 keys, all with zero misses.
These short controls do not establish a hot-read improvement.

Raw: results/write-metaword-initial-pairs.txt,
results/write-metaword-five-pairs.txt, and results/read-metaword-pairs.txt.

A fresh original-C comparison uses the existing optimized native binaries from
the previous checkpoint (unchanged library and timing drivers), five alternating
pairs per scenario, one million identical corpus keys, CPU 0 serial and CPUs
0–3 parallel. Medians C / Go:

| Scenario | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh, one worker | 1.79 / 2.36 | 341 / 290 | 741 / 622 | 9969 / 8496 | 18745 / 14186 |
| Fresh, four workers | 4.37 / 5.35 | 451 / 441 | 882 / 852 | 15509 / 10249 | 31780 / 17343 |
| Replacement, one worker | 3.53 / 4.25 | 310 / 301 | 551 / 601 | 711 / 782 | 892 / 1052 |
| Replacement, four workers | 11.84 / 14.55 | 330 / 270 | 582 / 470 | 741 / 631 | 971 / 842 |
| Ascending, one worker | 3.72 / 5.41 | 201 / 160 | 421 / 321 | 2515 / 2545 | 5180 / 4909 |

Go leads all unclocked throughput cases. Fresh-four p50/p95 now lead C, but
ascending p99 remains ~1.2% higher. Serial replacement clocked runs varied
substantially (Go p50 260–330 ns), leaving worse p95/p99/p99.9 medians in this
batch despite the control above. This discrepancy requires a three-way
baseline/cache/C confirmation; the full write goal remains open. Clocks include
their overhead without subtraction. Raw:
results/write-metaword-native-go-{fresh1,fresh4,update1,update4,ascending1}.txt.


Six additional clocked serial replacement triplets rotate all three positions
and reverse order on alternate repetitions. Medians C / committed baseline /
cache: p50 291/251/241 ns, p95 491/451/431, p99 646.5/621/601, p99.9
796/781.5/762. Clocked aggregate rates are 3.105/3.04/3.145 Mops/s. The cache
improves each percentile against the baseline in this confirmation, and each
leads C. The earlier bad batch was not reproduced; its cause is unresolved,
so it is retained above rather than discarded. Raw:
results/write-metaword-update-control.txt.


Retained unconditional cache validation: full plain, race, native, and combined
race/native suites pass, as do native vet, the benchmark-module race suite, and
Linux ARM64 compilation. A pinned CPU-0 metadata benchmark smoke reports
64 bytes/meta (results/metaword-footprint.txt); ten iterations verify layout,
not steady-state timing. No new metadata allocation or pointer is introduced
on amd64. Wider fuzz/profile follow-up is recorded separately below.


## Ascending profile and single-slot packed shifts

A matched ascending capture at febb29b pins the sole writer to CPU 0, using
cycles:u at 1999 Hz, frame-pointer Go and DWARF native stacks. Go's 366 loader
samples attribute ~31.5% of sampled cycles to string comparison, ~10.2% to
buildSortedPacked, ~9.6% to insertEntry, and ~3.0% to memmove. C's 534 loader
samples attribute ~46.2% to inlined kv_size at kv.c:37 and ~18.7% to magnet
insertion. The kv_size attribution is a sampled source location, not proof that
its arithmetic alone costs that fraction. These short profiles identify work
sites; they do not isolate p99 causes. Reports and driver logs:
results/write-metaword-ascending-{go,native}-profile{,-run}.txt.

Two isolated packed-builder experiments passed the full native suite:
(1) replace a dynamic copy with a direct uint32 assignment when a shift spans
exactly one slot; (2) build record pointers in a stack array and replace hs in
one bulk assignment, removing the left leaf's preceding pointer-array clear.
The latter's placement test also starts with every old record pointer populated
and checks every final pointer, including vacant slots, against ordinary insert.

Six repetitions rotate three positions and reverse on alternate repetitions,
with separate unclocked and clocked runs. One million identical keys, CPU 0
serial and CPUs 0–3 for four writers. Medians baseline / single-slot / bulk:

| Scenario | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Ascending, one worker | 5.245 / 5.38 / 5.185 | 151 / 151 / 151 | 320 / 320 / 320 | 2559.5 / 2454.5 / 2555 | 4945 / 4814 / 4894.5 |
| Fresh, four workers | 5.205 / 5.30 / 5.26 | 441 / 441 / 440 | 851 / 856.5 / 842 | 10180 / 10329 / 10329 | 17077.5 / 17718.5 / 17448 |
| Replacement, one worker | 4.485 / 4.39 / 4.32 | 241 / 245.5 / 250 | 431 / 440.5 / 450.5 | 601.5 / 606.5 / 621 | 767 / 771.5 / 781.5 |

Every single-slot ascending p99 (2434–2464 ns) beats every baseline p99
(2525–2595 ns). This targets the remaining ascending gap, with ~4.1% lower p99
and ~2.6% higher throughput. It has tradeoffs: fresh tails increase slightly
and replacement throughput falls ~2.1% in this batch despite no changes to the
replacement path. Those effects are recorded rather than assumed to be noise;
a full fresh C matrix follows before accepting the overall tradeoff.

The bulk variant has no meaningful ascending p99 gain and regresses ascending
and replacement throughput. Its code and special stale-pointer test were
restored; archived patch: results/leaf-packed-bulk-rejected.patch. Raw controls:
results/write-packed-shift-rotate.txt.


The fresh five-pair original-C matrix for the single-slot version, with the same
pinning and one-million-key trace, now leads on unclocked throughput and every
reported latency percentile in all five primary cases. Medians C / Go:

| Scenario | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh, one worker | 1.77 / 2.35 | 341 / 281 | 722 / 621 | 9778 / 8266 | 18525 / 13836 |
| Fresh, four workers | 4.29 / 5.23 | 460 / 451 | 902 / 861 | 15830 / 10480 | 32933 / 18054 |
| Replacement, one worker | 3.60 / 4.34 | 291 / 241 | 501 / 431 | 652 / 601 | 812 / 762 |
| Replacement, four workers | 11.93 / 14.30 | 320 / 261 | 531 / 460 | 672 / 622 | 871 / 822 |
| Ascending, one worker | 3.75 / 5.38 | 210 / 160 | 441 / 321 | 2615 / 2514 | 5560 / 4980 |

Unclocked throughput leads by ~20–43%. Timing overhead is included without
subtraction. This closes the primary matrix's remaining ascending p99 gap;
concentrated-write native comparisons remain to qualify contention separately.
The single-slot variant is retained for this measured overall tradeoff. Raw:
results/write-packone-native-go-{fresh1,fresh4,update1,update4,ascending1}.txt.

Plain, native, combined race/native, native vet, benchmark-module race checks,
and Linux ARM64 compilation pass for the single-slot version. Packed placement
tests cover every occupancy 0–128 under random and extreme tag collisions,
comparing all slots against ordinary insertion. The bulk variant remains
rejected and no pointer-array or layout changes are retained.

Native five-second fuzz campaigns with four workers also pass: leaf 831,
metadata 1848, map 1925, and range 1284 executed inputs. These are bounded
regression checks, not an exhaustive concurrency proof.


## Original C concentrated-write qualification

The comparison harness now accepts --hot-keys N for both implementations.
Outside timing, C frees unused corpus records and repeats references to the
first N keys, matching Go's prepared repeated-key trace. Cleanup frees each
original once. After timing, C probes every retained key and iterates to verify
exact cardinality; Go checks Len. Ascending with hot keys and negative/out-of-
range counts are rejected. Fresh-64 and update-1 smoke traces with 1000
operations pass both clock modes; invalid argument checks pass. Native library
sources and optimization flags remain unchanged.

Five alternating pairs, one million operations, four writers pinned to CPUs
0–3, implementation 0542493. Medians C / Go:

| Repeated keys | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 5.34 / 24.55 | 460 / 70 | 2004 / 561 | 5341 / 1072 | 16191 / 8777 |
| 64 | 5.37 / 18.26 | 421 / 90 | 1843 / 602 | 4378 / 1302 | 9267 / 27422 |

Go wins all one-key metrics and wins 64-key throughput and p50/p95/p99, but
64-key p99.9 is ~3x C. The user explicitly requests retaining the 18.26 Mops/s
result despite that tail tradeoff; it remains committed in 0542493 with the
metadata cache in febb29b. Further tail work must preserve this winner.
Both traces fit in a single leaf: after their initial insertions there are no
splits or further size-counter increments, isolating concentrated replacements.
Raw: results/write-packone-native-go-hot{1,64}.txt; smoke logs:
results/write-hot-native-smoke-{fresh,update}.txt.

Eight clocked load repetitions per implementation were profiled sequentially,
with the same pinning, cycles:u at 1999 Hz, Go frame pointers and native DWARF
stacks. Go's 4595 loader samples attribute 18.3% to lockLeaf, 13.0% to processor
spinning, 10.9% to RWMutex.Lock, 9.3% to RWMutex.Unlock and 5.9% to Mutex.lockSlow.
C's 10985 samples attribute 47.3%/23.9% to its write-lock attempts and 13.2% to
write unlock. Unknown Go addresses include separately mapped timer code; they
are left unattributed. Sample percentages identify contention and routing work;
they do not isolate the source of individual p99.9 delays. Reports and driver
logs: results/write-packone-hot64-{go,native}-profile{,-run}.txt.


## Cached whole-keyspace write interval

The hot-64 profile identifies routing work even though every key resides in
one leaf. Cache the proof that a routed leaf has an empty lower anchor and no
successor: it covers the entire key space. Subsequent prepared writes can reuse
it across different prefixes and short/binary keys, validating the existing
leaf version under its write lock before touching records. Splits invalidate
that version and force ordinary routing. The single new boolean occupies Ref
padding; unsafe.Sizeof(Ref{}) remains 56 bytes on amd64. No GC pointer, leaf
layout, lock primitive, or count synchronization changes.

The new test crosses different prefixes, empty and binary keys, then splits
the cached root from another Ref and verifies a moved key, original values and
exact length. Existing prefix, split, merge, boundary and closure tests pass.
An initial variant redundantly tested putWhole at the covered-prefix return;
it improved hot-64 but regressed one-key throughput. The retained variant
initializes existing coverage flags from the whole-keyspace proof and uses the
original covered-prefix return, removing that redundant test.

Three alternating pairs per case, one million operations, four pinned writers
on CPUs 0–3. Medians committed 0542493 / retained candidate:

| Trace | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| 64 repeated keys | 18.06 / 24.84 | 90 / 70 | 591 / 230 | 1233 / 1142 | 26210 / 19216 |
| 1 repeated key | 25.79 / 26.10 | 70 / 70 | 400 / 551 | 992 / 1152 | 9518 / 14016 |

The 64-key trace improves throughput ~37.5% and p99.9 ~26.7%. One-key throughput
is maintained, but its clocked tails are worse in this batch. The user explicitly
requests retaining this new winner after discussing the one-key diagnostic.
This is a Go baseline/candidate comparison, not a fresh paired C measurement;
the prior original-C hot-64 checkpoint remains 5.37 Mops/s and 9267 ns p99.9.

Separate ordinary controls, also three alternating pairs, CPU 0 serial and
CPUs 0–3 parallel, give baseline / retained candidate:

| Scenario | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh, four writers | 5.20 / 5.31 | 441 / 441 | 842 / 861 | 10289 / 10420 | 17173 / 17383 |
| Replacement, one writer | 4.18 / 4.38 | 241 / 241 | 431 / 430 | 601 / 592 | 762 / 761 |
| Ascending, one writer | 5.28 / 5.06 | 151 / 160 | 320 / 330 | 2435 / 2504 | 4819 / 5560 |

The ascending regression (~4.2% throughput and higher tails) is a documented
tradeoff, not assumed away as noise. The accepted concentrated-write gain is
retained; the direct C matrix needs refreshing for this revision. The earlier
18.26 Mops/s implementation remains available in 0542493. Raw controls:
results/write-root{finger,proof}-{hot,ordinary}-pairs.txt.

Full native and combined race/native suites, native vet, benchmark-module race
checks, and Linux ARM64 compilation pass for the retained candidate. Focused
write-hint tests also pass. The original C and existing synchronization
primitives remain unchanged. Further latency work continues from this retained
throughput winner rather than reverting it.


## Original C comparison after whole-keyspace caching (a83b974)

Five alternating original-C/Go pairs per scenario, one million identical
six-word keys, CPU 0 for one writer and CPUs 0–3 for four writers. Throughput
is from separate unclocked runs; raw clocked percentiles include clock overhead.
Native binaries and optimized compiler flags are unchanged from the previous
comparison. Medians are C / Go:

| Scenario | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh, one writer | 1.79 / 2.42 | 341 / 281 | 722 / 621 | 9929 / 8296 | 18665 / 13625 |
| Fresh, four writers | 4.30 / 5.36 | 441 / 431 | 871 / 851 | 15269 / 10169 | 31149 / 17353 |
| Replacement, one writer | 3.57 / 4.42 | 300 / 250 | 511 / 450 | 661 / 612 | 822 / 781 |
| Replacement, four writers | 11.94 / 14.47 | 320 / 260 | 552 / 451 | 711 / 621 | 932 / 821 |
| Ascending, one writer | 3.75 / 5.49 | 211 / 161 | 441 / 340 | 2635 / 2565 | 5641 / 5170 |

The retained revision leads C on all reported ordinary-write medians, with
throughput ahead by 21–46%. Two of the five Go ascending clocked repetitions
were substantially slower (p99 3387/3416 ns, p99.9 7083/7314 ns); they are
included, and this median result does not erase the earlier paired ascending
regression versus 0542493. Raw:
results/write-rootproof-native-go-{fresh1,fresh4,update1,update4,ascending1}.txt.


The same five-pair comparison for concentrated replacements, four pinned
writers on CPUs 0–3, gives C / Go:

| Distinct keys | Unclocked Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| 64 | 5.42 / 27.35 | 441 / 70 | 1874 / 491 | 4268 / 1213 | 8717 / 21240 |
| 1 | 5.16 / 24.82 | 521 / 70 | 1954 / 571 | 5601 / 1153 | 15209 / 20068 |

Go now leads throughput by ~5.05x on 64 keys and ~4.81x on one key. The
reported p50/p95/p99 also lead C, but p99.9 is worse in both concentrated cases
in this batch. This is consistent with keeping the user's accepted throughput
winner while continuing contention-tail work. Raw:
results/write-rootproof-native-go-hot{1,64}.txt.

A separate hot-64 Go diagnostic enables full mutex/block sampling and CPU
profiling only around the load. The block profile records 9.41 ms aggregate
waiting in the RWMutex's writer mutex, all under lockPutLeaf; the dominant
91.92 ms WaitGroup wait is the harness waiting for the load to finish. The
mutex profile attributes 9.18 ms to writer unlock; runtime locks account for
another 1.77 ms. These profiles show remaining parked writer contention after
routing is bypassed. They do not identify the cause of each p99.9 event or
measure unperturbed performance: this sampled run reports p99.9 30.06 us, and
its short CPU profile is only a diagnostic. Compare with the previously
recorded C hot-64 perf profile, dominated by active lock attempts/unlock.
Raw: results/write-rootproof-hot64-locks-{profile,run}.txt.


## Bounded standard-RWMutex try-lock sweep (not retained)

Try acquiring the existing RWMutex 1/4/16/64 times before falling back to Lock,
only on a cached whole-keyspace interval. Three rotated repetitions per case,
CPUs 0–3, one million operations, separate clocked/unclocked traces:

| 64-key variant | Mops/s | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: |
| a83b974 | 27.37 | 1103 | 16452 |
| TryLock once | 30.89 | 1082 | 27442 |
| 4 tries | 31.57 | 972 | 29857 |
| 16 tries | 29.97 | 832 | 19918 |
| 64 tries | 13.28 | 812 | 6242 |

Long spinning can lower the tail while sacrificing the accepted throughput
win. A second sweep of 24/32/48 attempts found 32 promising on 64 keys
(28.83 Mops/s, 6182 ns p99.9), but its one-key p99.9 worsened from baseline
9107 ns to 27642 ns. Restricting tries to cross-prefix whole-leaf cache hits
leaves the one-key path using ordinary Lock. Five rotated repetitions then
gave baseline / cross-32 hot-64 27.69 / 29.48 Mops/s, p99 1122 / 852 ns,
and p99.9 22423 / 23735 ns: the strong tail result did not reproduce.
These variants are not retained. The committed library remains a83b974;
this is evidence against treating one promising tail batch as a win.
Raw: results/write-trylock{,-mid,-cross}-hot-pairs.txt.


## Revisiting single-word locks after whole-keyspace caching (experimental)

The previous bit lock used atomic Swap for unlock and yielded after 256 polls.
The new screening repeats it on the much faster a83b974 write finger, preserves
the 24-byte lock/leaf layout, and sweeps polling bounds. An initial replacement
with Go atomic Store is NOT a cheap plain-store unlock: objdump
shows XCHGL on amd64 for Go's sequentially consistent Store, just as for Swap.
Do not attribute that variant's gain to removing the exchange. Three rotated
pairs, one million writes, CPUs 0–3, baseline / Go-Store-with-16-poll-yield:

| Trace | Unclocked Mops/s | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: |
| 64 keys | 26.40 / 56.43 | 1173 / 300 | 26210 / 30197 |
| 1 key | 25.41 / 50.78 | 952 / 191 | 22482 / 23915 |

Full native/race-native suites, vet, nested-module race checks, and ARM64
compilation pass for the Go-Store variant. Ordinary three-pair controls give
baseline / candidate throughput (Mops/s): fresh1 2.34 / 2.30, fresh4 5.28 / 5.42,
update1 4.40 / 4.51, update4 14.28 / 15.54, ascending1 5.35 / 5.59. Serial fresh
p99.9 worsens 13837 / 15008 ns; other tails are mixed. Three paired read runs,
two one-second rounds each, yield baseline / candidate medians: uniform
11.995 / 11.765 Mops/s, one-key 21.745 / 20.555, 64-key 21.305 / 20.035,
all zero misses. The hot-read loss is ~5–6%, not assumed away.

Processor PAUSE backoff with short polls retains some throughput, but long
pause/poll combinations sacrifice much of it. A true release-store experiment
uses MOVL on amd64, STLRW on ARM64, and portable atomic Store elsewhere, with
explicit reader/writer race synchronization. Another variant separates writer
reservation and active-reader count: readers increment/decrement a counter,
then check the writer reservation, backing out when a writer is pending.
Both preserve concurrent readers and drain active readers before writing.
ARM64 release assembly compiles; performance measurements are amd64 only.

A second three-pair sweep gives baseline / single-word release with 64 polls:
hot-64 throughput 26.94 / 55.20 Mops/s, p99 1383 / 190 ns, p99.9
25459 / 18626 ns. Other polling bounds and the separate-reader-counter variant
have mixed tails; no isolated claim that release Store alone produces this
whole gain is established. The release variants still require full correctness
and regression qualification before any is retained. The committed production
implementation remains a83b974 at this experimental checkpoint.

Raw: results/write-storelock-hot-pairs.txt,
results/write-pauselock-hot-pairs.txt,
results/write-release-drain{,-mid}-hot-pairs.txt,
results/write-storefinal-ordinary-pairs.txt, and results/write-store-read-pairs.txt.


## Retained release-store leaf lock with bounded OS yields

The retained leaf lock uses the native-style writer reservation bit and reader
count in one word. A writer reserves the bit, drains active readers, and
publishes record writes with a release store. New readers wait behind a
reserved writer; readers remain concurrent. amd64 uses MOVL and ARM64 STLRW;
other architectures use the stronger portable Go atomic Store. The lock is
still 24 bytes with no GC pointers, preserving the leaf layout. The original
C unlock is atomic_fetch_sub with MO_RELEASE (lib.c rwlock_unlock_write),
compiled as a locked add/subtract on amd64, not a plain release store. C reader
admission can temporarily increment the count while a writer holds the bit,
then back out, so its unlock must preserve those increments. Go's CAS reader
admission prevents such increments while the writer bit is set; this different
admission algorithm permits the plain store safely. The measured hot-read
cost below is a relevant tradeoff of that write optimization. Explicit race
annotations hide bookkeeping atomics and express writer/reader synchronization;
a race-only subprocess test proves independent readers still expose races.

On Linux, waiters call raw sched_yield every 16 failed polls while retaining
their Go execution context. They yield to the Go scheduler after 1024 failed
polls, or after 16 on one P, so oversubscribed/one-P callers can progress.
Other platforms use Go scheduler yields. This is a measured latency/throughput
balance; longer polling and processor PAUSE variants had mixed results.

Five alternating original-C/Go pairs, one million canonical six-word keys,
CPU 0 serial and CPUs 0–3 parallel. Separate unclocked throughput and raw
clocked percentile medians, C / retained Go:

| Scenario | Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh, one writer | 1.60 / 2.24 | 350 / 290 | 731 / 621 | 10059 / 8556 | 18835 / 14377 |
| Fresh, four writers | 4.28 / 5.57 | 461 / 441 | 972 / 872 | 15950 / 10550 | 33484 / 17493 |
| Replacement, one writer | 3.56 / 4.35 | 291 / 251 | 501 / 451 | 652 / 621 | 812 / 782 |
| Replacement, four writers | 11.88 / 14.61 | 321 / 270 | 531 / 471 | 681 / 641 | 892 / 852 |
| Ascending, one writer | 3.80 / 5.57 | 201 / 141 | 421 / 301 | 2505 / 2485 | 5110 / 4769 |
| 64 repeated keys, four writers | 5.14 / 30.60 | 431 / 60 | 1854 / 2014 | 4719 / 4318 | 11462 / 7815 |
| 1 repeated key, four writers | 5.29 / 40.63 | 311 / 50 | 1823 / 1914 | 4509 / 3467 | 14829 / 6302 |

Go leads throughput, p50, p99 and p99.9 in all seven scenarios, including the
previous concentrated p99.9 gap. Concentrated p95 is still ~5–9% worse than C,
so the full throughput-and-latency goal remains open. Ordinary p95 leads C.
Relative to a83b974, concentrated p95/p99 are worse while p99.9 improves; this
is a documented redistribution of contention waits, not an all-percentile
baseline win. The accepted 18.26 Mops/s and whole-keyspace-cache changes remain
in the implementation and history. This new balance preserves their large
throughput gain while improving the longest reported percentile.

Three paired Go controls, baseline / retained: fresh4 throughput 5.37 / 5.52
Mops/s and p99.9 17804 / 16131 ns; serial replacement 4.46 / 4.29 Mops/s,
p99.9 772 / 811 ns; ascending 5.49 / 5.53 Mops/s, p99.9 4799 / 4799 ns.
The serial replacement regression is retained in these data. Three paired read
runs, two one-second rounds each, give uniform 12.10 / 12.225 Mops/s,
one-key 21.81 / 20.05, and 64-key 21.485 / 20.03, all zero misses. Hot read
throughput declines ~7–8%; shared reads remain concurrent.

The higher-throughput Go-scheduler-only candidate (~50–56 Mops/s) is preserved
as results/leaf-store16-throughput-alternative.patch against a83b974, along
with the earlier measurements and green native/race checks. It is not the
selected default because its concentrated p99.9 remains worse. This retains
the reproducible alternative rather than discarding its throughput result.

Eight sequential clocked hot-64 Go repetitions were profiled on CPUs 0–3,
cycles:u at 1999 Hz with frame pointers. 6811 loader samples estimate 8.116B
user cycles: lockSlow 12.67%, lockPutLeaf 12.22%, runtime.lock2 2.87%, match
2.47%, leafLockWait 2.10%. Unknown timer/kernel addresses remain unattributed;
user-cycle sampling does not measure kernel yield costs. Compare with the
unchanged original-C profile above, dominated by write-lock attempts/unlock.
A separate full mutex/block diagnostic no longer shows parked leaf RWMutex
writers; waiting now occurs through polling/yields, not zero waiting. It does
show GOMAXPROCS taking the runtime scheduler lock inside the wait loop; local
runtime/debug.go confirms even GOMAXPROCS(0) locks sched.lock. Removing repeated
queries is the next measured experiment, not yet part of this checkpoint.

Full native and combined race/native suites, native vet, nested benchmark
race checks, Linux ARM64/386 and Darwin amd64 compilation pass. The lock tests
exercise reader/writer exclusion, writer gating, failed TryLock cleanup, and
progress with one/four Ps and 20 goroutines. Five-second native map/range fuzz
campaigns with four workers pass (2254/1533 executions).

Raw: results/write-osyield16-native-go-{fresh1,fresh4,update1,update4,ascending1,hot1,hot64}.txt;
results/write-osyield{,-short}-hot-pairs.txt;
results/write-osfinal-{ordinary,read}-pairs.txt;
results/write-osyield16-hot64-{profile,profile-run,locks-profile,locks-run}.txt.


## Scheduler-query and intermediate-interval controls (not retained)

The new profile identifies GOMAXPROCS(0) taking sched.lock in the wait loop.
Two attempts remove repeated queries: omit them entirely while retaining the
1024-poll Go-scheduler bound, or query lazily once per contended acquisition.
Five rotated repetitions, CPUs 0–3, one million writes, separate clocks:

| Trace / variant | Mops/s | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: |
| 64 keys / 1c7c6e9 | 31.09 | 1973 | 4428 | 7926 |
| 64 keys / no query | 29.19 | 1954 | 4379 | 7965 |
| 64 keys / cached query | 27.72 | 1934 | 4188 | 7524 |
| 1 key / 1c7c6e9 | 40.43 | 1903 | 3487 | 6141 |
| 1 key / no query | 39.07 | 1864 | 3467 | 5851 |
| 1 key / cached query | 38.64 | 1844 | 3317 | 5851 |

The profile's cost is real, but removing it also changes backoff timing and
does not produce an overall winner. No causal claim that its CPU percentage
translates directly into removable wall time is supported. Both are rejected.

Five rotated original-C/Go comparisons also test OS-yield intervals of 16,
32 and 48 polls (Go scheduler after 64 OS yields, or immediately on one P).
For hot-64, C / retained-16 / interval-32 / interval-48 throughput is
5.22 / 31.02 / 27.01 / 20.10 Mops/s; p95 is 1894 / 2093 / 1753 / 1723 ns;
p99.9 is 9919 / 8055 / 10179 / 12173 ns. Interval 32 improves p95 but loses
~13% of retained throughput and remains slightly worse than C at p99.9.
For one key it leads all C percentiles, but does not solve the complete matrix.
These intervals are rejected; 1c7c6e9 remains the production implementation.
Raw: results/write-procsquery-hot-pairs.txt and
results/write-osinterval-native-hot-pairs.txt.


## Batched processor-pause controls (experimental)

Instead of calling sched_yield after every 16 polls, issue an unrolled batch
of PAUSE instructions, retaining bounded Go scheduler yields and the one-P
check. Short 1/4/8/16-instruction batches mostly sacrifice throughput. Longer
batches plus an earlier Go-scheduler bound can improve throughput and p95,
but their p99.9 tradeoff still needs qualification. Five rotated original-C/Go
pairs, CPUs 0–3, one million operations, separate clocks, hot-64:

| Variant | Mops/s | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: |
| Original C | 5.07 | 1834 | 4198 | 8666 |
| Retained OS yields | 30.74 | 2074 | 4479 | 8096 |
| 64 PAUSE / 256-poll Go yield | 33.71 | 2214 | 4739 | 8677 |
| 48 PAUSE / 256-poll Go yield | 30.55 | 1984 | 4318 | 7764 |
| 64 PAUSE / 128-poll Go yield | 42.70 | 1503 | 4589 | 8947 |

The last variant gains ~39% throughput and improves p95, but still trails C on
p99 and slightly on p99.9. A further five-pair batch with 48 PAUSE / 128 polls
reports 34.50 Mops/s, p95 1282 ns, p99 3687 ns and p99.9 11361 ns, versus
C 5.38 Mops/s, 1824 / 4328 / 8877 ns. Shortening the Go-yield bound further
to 96 polls raises throughput to 40.32 Mops/s and lowers p95/p99, but worsens
p99.9 to 12885 ns. No new default is selected at this checkpoint.

A separate CPU-0 calibration executes 200000 calls to no-argument assembly
functions per batch, three repetitions, GOMAXPROCS=1 and a locked OS thread.
Median ns/call: empty RET 2.45, 1 PAUSE 16.54, 4 PAUSE 62.30, 16 PAUSE 244.21,
64 PAUSE 981.55, 128 PAUSE 1977.85. These are this host's measured delays,
not fixed architectural times. The write percentiles above are never adjusted
by subtracting these calibration costs. CPU topology confirms cores 0–2 share
L3 ID 0 and core 3 uses L3 ID 1; C and Go use the same four physical cores.

Raw: results/write-burstpause{,-long,-native,-refined-native}-hot-pairs.txt,
results/write-pause-cost.txt. Calibration sources are preserved as
results/pause-cost-main.go.txt and results/pause-cost-pause_amd64.s.txt.
The native unlock explanation earlier in this document is corrected against
lib.c and objdump: C uses a release-ordered atomic subtraction, whereas Go's
different CAS-based reader admission permits a plain release store.


## Identical value-header replacement (retained)

Replacing a record's value now compares its string data pointer and length
under the existing exclusive leaf lock. An identical header needs no store.
Equal text with a different header still replaces the retained allocation;
empty substring pointers participate too. This reduces redundant stores and
associated record cache traffic for idempotent writes. It preserves lock
acquisition, reader exclusion, version validation, and exact Len semantics.
The pointer comparison adds no allocations. Changing-value controls below
exercise the actual replacement store, rather than this idempotent shortcut.

Five rotated original-C / 1c7c6e9 / new-Go repetitions, one million writes,
CPUs 0–3, separate unclocked throughput and clocked latency runs:

| Trace / implementation | Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| 64 keys / original C | 5.43 | 440 | 1833 | 4358 | 8877 |
| 64 keys / 1c7c6e9 | 29.66 | 60 | 2094 | 4508 | 8125 |
| 64 keys / new Go | 39.27 | 60 | 1032 | 2234 | 5190 |
| 1 key / original C | 5.35 | 311 | 1813 | 4548 | 13435 |
| 1 key / 1c7c6e9 | 40.83 | 50 | 1824 | 3507 | 6362 |
| 1 key / new Go | 43.82 | 130 | 501 | 2194 | 5119 |

The 64-key case gains 32% throughput over the retained lock baseline and
closes its remaining C latency gaps. One-key p50 increases versus that Go
baseline but remains below C; throughput and all higher percentiles improve.
These concentrated traces repeatedly write identical empty value headers;
they do not establish the same improvement for changing values.

A fresh five-pair ordinary C matrix uses the same one-million six-word keys,
CPU 0 for serial and CPUs 0–3 for four writers, with alternating run order.
Each cell is original C / new Go; throughput is unclocked, latency separate:

| Scenario | Mops/s | p50 ns | p95 ns | p99 ns | p99.9 ns |
| --- | ---: | ---: | ---: | ---: | ---: |
| Fresh, one writer | 1.77 / 2.33 | 341 / 280 | 721 / 621 | 9849 / 8366 | 18586 / 13706 |
| Fresh, four writers | 4.29 / 5.47 | 451 / 441 | 902 / 852 | 15720 / 10370 | 32342 / 16612 |
| Update, one writer | 3.54 / 4.32 | 310 / 270 | 541 / 481 | 692 / 651 | 862 / 832 |
| Update, four writers | 11.93 / 14.70 | 330 / 261 | 571 / 461 | 721 / 631 | 931 / 841 |
| Ascending, one writer | 3.74 / 5.54 | 210 / 150 | 431 / 310 | 2645 / 2515 | 5440 / 4819 |

All seven measured scenarios now lead C on median throughput and each reported
latency percentile, including p99.9. This is a measured fixture/host result,
not a bound on every run or every workload. In particular, fresh four-writer
p50 and serial ascending p99 have small margins. Clock costs are included
without subtraction; raw logs retain each run and its verification.

Correctness: full native (14.735s) and race/native (41.336s) suites, native vet,
benchmark-module race tests, Linux ARM64/386 and Darwin amd64 compilation pass.
New regression cases check replacement header retention for equal text from
separate allocations, empty substrings, shared pointers with different lengths,
and identical headers through both Put and PutKey.

Raw: results/write-valuehint-native-hot-pairs.txt and
results/write-valuehint-native-go-{fresh1,fresh4,update1,update4,ascending1}.txt.

The final PAUSE tuning controls were rejected before this change. Five-pair
hot-64 runs for 32 PAUSE/320 polls, 48/192 and 64/160 improve selected metrics
but fail to lead C across all tails. A further 56-PAUSE sweep at 160/176/192
polls also fails that criterion. The retained OS-yield policy is unchanged.
Raw: results/write-burstpause-{middle,center}-native-hot-pairs.txt.


The refreshed ordinary Put/Set comparison against Tidwall v1.8.1 exercises
changing values on 1,048,576 binary-key writes, three repetitions and verified
CPU pinning. New Go median ns/write: serial fresh 476.2, serial update 184.0,
serial ascending 159.5, four-writer fresh 177.0, four-writer update 55.61.
All update runs remain allocation-free. Go leads throughput in all comparisons
and update p99; serial fresh/ascending p99 still trails Tidwall. These are
within-batch container comparisons, not an isolated before/after measurement
of the new header check. Full tables and raw logs are in README.md.


The new Go perf capture repeats eight clocked hot-64 loads on CPUs 0–3,
cycles:u at 1999 Hz, with frame-pointer call stacks. Loader-only attribution
excludes fixture construction and verification: 3938 samples, approximately
6.073 billion sampled user cycles. lockPutLeaf accounts for 24.77%, lockSlow
8.47%, and leaf.match 1.86%. The prior retained-lock capture totals 8.116 billion
loader user cycles; these are diagnostic captures, not paired timing estimates.
The unchanged original-C eight-load capture totals 20.996 billion user cycles,
with 84.31% in try-write, no-reader try-write, and write unlock combined.
C unlock is atomic subtraction; Go's CAS reader admission permits release
stores. The profiles support prioritizing routing, lock acquisition, scheduler
waits and redundant record traffic rather than treating C's advantage as a
language-wide constant. They do not isolate cache invalidations as the sole
cause of this header optimization's gain. Unknown timer/kernel addresses remain
unattributed, and cycles:u does not measure the full kernel cost of OS yields.

Separate Go CPU/mutex/block profiles contain no parked leaf-writer mutex wait;
block delay is chiefly main's WaitGroup coordination. Polling and OS-yield waits
still exist. The CPU profile is coarse (260 sampled CPU-ms) and includes clock
sampling overhead. Raw: results/write-valuehint-hot64-{profile,profile-run,
locks-run,locks-cpu,locks-mutex,locks-block}.txt; unchanged C reference:
results/write-packone-hot64-native-{profile,profile-run}.txt.
