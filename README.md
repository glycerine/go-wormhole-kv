# Wormhole in Go

Here we implement the [ordered in-memory map Wormhole](https://github.com/wuxb45/wormhole) 
in Go. The theory of operation is described in [the Eurosys 2019 paper](wormhole.pdf). 
The upstream implementation is C. The port keeps
the hash-indexed prefix trie, two-choice cuckoo metadata tables, linked leaves
of 128 records, lazy sorted indices, separator selection,
and native split/merge policies. All key types use stable slots, tag-bucket
chains and leaf-owned record arrays to avoid pointer shifts and per-key
allocations. Record storage is separate from the common leaf, and leaf pointers
precede scalar arrays to reduce GC scanning. String maps use this layout by
default, trading higher retained memory and slower small-set reads for faster
fresh insertion. Numeric routing caches
leaves for complete two-byte prefixes under the same metadata epochs;
prefixes containing a longer separator fall back to the trie. Numeric separator
selection compares order bits directly; string separator selection reads the
existing keys and retains the chosen prefix without copying its bytes.
It has no C dependency in ordinary builds.

```go
import "github.com/glycerine/wormhole/go"

m := wormhole.New[string, string]()
r := m.Ref() // one handle per concurrent caller
defer r.Close()
if err := r.Put("user:42", "Ada"); err != nil {
    panic(err)
}
value, found := r.Get("user:42")
```

Runnable examples also cover [iteration and callbacks](example_test.go).
Construct maps with `New`; do not copy a Map or Ref. Close each Ref when done.
`New` and zero-valued `Options` use the lower-memory metadata configuration:
the two-byte metadata lookup cache is off and initial hash tables are smaller.
To enable that cache and the larger initial reservation, use
`NewWithOptions[K, V](Options{MetaCache: true})`. The separate numeric leaf-route
cache remains enabled in both modes, and hash tables still grow automatically.
Choose the option at construction; it cannot be changed on a live map.
`Map[K cmp.Ordered, V any]` stores native keys and values, including named
ordered key types and values containing slices, maps or pointers. Keys follow
natural numeric order or bytewise string order. NaNs sort after positive
infinity; all NaN payloads identify one key. Positive and negative zero also
identify one key. Range bounds and iterator seeks use the same ordering.

Values are copied by assignment under leaf locks. Callers must synchronize
mutations of objects reachable through stored values. Strings are retained
without copying and must remain immutable. Numeric routing bytes use scratch
storage owned by each Ref; only metadata separators and prepared keys retain
encoded copies. Existing-key writes do not allocate replacement records.

| Operation | Go API |
| --- | --- |
| Point reads/writes | `Ref.Get`, `Probe`, `Put`, `Delete` |
| Atomic callback under write lock | `Ref.Merge`, `Update` |
| Callback under read lock | `Ref.Inspect` |
| Range deletion | `Ref.DeleteRange(start, end)` for [start,end), `DeleteFrom(start)` without an upper bound |
| Ordered iteration | `Ref.Iterator`, then `Seek`, `Valid`, `Peek`, `Next`, `Skip`, `Inspect`, `Park`, `Close` |
| Ordered callback scan | `Ref.Scan(start, limit, visit)` returns the number visited; a false callback stops the scan |
| Repeated prepared-key operations | `PrepareKey`, `Ref.PutKey`, `ProbeKey` |
| Number of keys | `Map.Len` |

Size-changing writes update per-Ref insertion/deletion tallies rather than a
shared global counter. `Len` takes a consistent snapshot across open references;
it costs O(open Refs), locks the reference registry, and can retry during writes.
Closing a Ref preserves its contribution. See [the counter measurements](bench/COUNTER.md)
for the write-throughput benefit and the increased cost of `Len`.

Each Ref is used by one goroutine at a time. An iterator reserves its Ref until
closed, including while parked. It holds a leaf read lock while positioned,
so writers to that leaf wait until it moves, parks, or closes. Iteration uses
left-to-right lock coupling and is not a whole-map snapshot. Range deletion
also releases locks between leaves; it is not an atomic transaction. `Scan`
uses the same lock coupling, visiting records within each leaf in one loop;
locks and the Ref are released even if its callback panics. Point callback
methods invoke the callback exactly once, including on misses. Callbacks must
not acquire leaf locks on the same Map; method documentation describes their
individual return-value and panic behavior.

Structural writers update the inactive prefix table, publish a new immutable
version descriptor, release leaf locks, wait for earlier metadata readers, then
update the retired table. Readers announce their epoch and recheck publication
before accessing a table; after routing they park before acquiring a leaf lock
and validate its version. This preserves native's metadata/leaf coordination
while using Go atomics and locks. Go GC keeps records and routed
leaves alive; it does not replace the table-reuse grace period.

The public API follows native's automatically parked `whsafe` operations.
Native raw-memory views, allocator hooks, manual epoch controls, destruction,
and separate unsynchronized entry points are represented by native Go values,
automatic epoch handling, GC, and the synchronized API rather than C ABI
wrappers. To remove all records use `DeleteFrom("")`; to release a map close its
Refs and discard it. As in native metadata, split anchors have a 65,535-byte
length limit; a split that cannot satisfy it returns `ErrAnchorTooLong` without
inserting the new key. Long keys that do not require such an anchor are allowed.

Run from this directory:

```sh
go test ./...
go test -tags=native ./...       # requires a C compiler and parent native sources
go test -race -tags=native ./...
go vet -tags=native ./...
```

BDD-style tests build from leaf placement and prefix metadata through complete
map operations, iterator/range behavior, and concurrency. Randomized model tests
and fuzz targets check arbitrary keys and operation histories. The `native`
build tag compares slots, tags, sorted indices, split/merge anchors, metadata,
callbacks, and workload generation against the parent C sources. Controlled
concurrency tests cover publication, table reuse, stale routes, lock ordering,
and small histories checked for linearizability. Native oracle fixtures live
in `test_vectors/`; Go's `testdata/` name is reserved for fuzz-generated data.

[Write benchmarks](bench/README.md) compare identical traces with
`github.com/tidwall/btree`, including raw latency quantiles. They run in a
separate module. Linux benchmark workers pin their OS threads to individual
CPUs and verify affinity; select physical cores with `taskset`.

To reproduce the native million-key workload, build native `concbench.out` from
the parent directory and build the Go driver here, then run sequentially:

```sh
go build -o /tmp/wormhole-concbench ./cmd/concbench
taskset -c 0-3 ../concbench.out ../words.txt 1000000 4
taskset -c 0-3 /tmp/wormhole-concbench ../words.txt 1000000 4
```

Both use four load workers, native six-word corpus generation and Lehmer RNG,
10,000-probe batches, and three three-second rounds. The dictionary is an
external input, not required for tests. The Go driver prints a corpus hash,
CPU mapping, counts, and misses; `-cpuprofile` profiles probes. Ordinary GC stays
enabled. Historical progress and implementation evidence are in
[PORTING.md](PORTING.md). The requirement-by-requirement evidence is in
[VALIDATION.md](VALIDATION.md); current write measurements are in `bench/results/`.

# benchmarks

## meta-data cache space/time trade-off button.

By default we have turned off the Meta data cache of the original design.
This saves about 38% of memory use.

The Options.MetaCahce = true can restore it. The difference in Get hit performance varies; 
we observe between 2-30% depending on the data set.

~~~
$ make memory  # Wormhole meta cache on vs off; versus
$ make memory2 # meta cache on.
# combining, to summarize:
~~~
| Container         | Heap bytes (MB)  | B/key | extra GC pointer tracing load (bytes) |
| ----------------- | ---------------: | ----: | ------------------------------------: |
| Wormhole (meta on)|     6.16         | 64.63 |                               3124336 | 
| glycerine/rbtree  |     6.10         | 64.00 |                               4000056 |
| insdict.Dict      |     5.30         | 55.62 |                                   112 |
| Turtle            |     4.78         | 50.15 |                                  1720 |
| Wormhole(meta off)|     4.45         | 46.69 |                               1452656 | 
| bufftree.BPTree   |     2.44         | 25.60 |                                 40256 |
| tidwall/btree.Map |     2.40         | 25.13 |                                141872 |
| builtin Go map    |     2.26         | 23.65 |                                  4248 |

> conclude: notice how Wormhole moved in the ranking.
>
> 6.16/4.45 => 1.38x less heap w/ meta cache off. 
>
> 3.1M/1.4M => 2.2x less GC trace load.
>

With the metadata caches in use:

| Operation (showing ns/key) | Wormhole | Turtle | BP-Tree| tidwall/btree | red-blk tree | insdict | builtin map |
| -------------------------- | -------: | -----: | -----: | ------------: | -----------: | ------: | ----------: |
| (hit)   Get                |     56.6 |  116.2 |   97.6 |         112.7 |        199.1 |    21.5 |        16.9 |
| (fresh) Put                |    142.1 |  127.1 |  113.8 |         158.4 |        358.5 |    53.6 |        77.2 |
| Ordered scan               |     2.49 |   2.79 |   3.06 |          3.89 |        15.23 |    5.43 |         N/A |
| Unordered scan (map does)  |     2.47 |   2.74 |   3.92 |          2.34 |        15.34 |    0.67 |        9.31 |

versus MetaCache off, Wormhole Get (hit) can be 58-73 ns/key (3-30% slower)

| Operation (showing ns/key) | Wormhole | Turtle | BP-Tree| tidwall/btree | red-blk tree | insdict | builtin map |
| -------------------------- | -------: | -----: | -----: | ------------: | -----------: | ------: | ----------: |
| (hit)   Get                |     58.9 |  118.6 |  101.2 |         111.5 |        199.7 |    21.8 |        16.9 |
| (fresh) Put                |    143.6 |  130.0 |  112.9 |         162.8 |        399.0 |    55.8 |        76.7 |
| Ordered scan               |     2.51 |   2.80 |   3.04 |          3.92 |        15.86 |    6.29 |         N/A |
| Unordered scan (map does)  |     2.50 |   2.76 |   3.94 |          2.34 |        15.25 |    0.66 |        9.18 |

## Tail latencies

as of 1d2aeb3f7, Oct 10 21:44:50 2026 -0500

~~~
  Pinned benchmarks now show:

  • 64 keys: Go 39.27M writes/s, C 5.43M.
  • One key: Go 43.82M writes/s, C 5.35M.
  • Go leads throughput and p50/p95/p99/p99.9 in all seven measured C comparisons.

  Tidwall comparisons are refreshed too. Go leads 
  throughput on all comparisions. Tidwall still wins serial fresh/
  ascending p95 - p99, but the max latency p100 is 
  tighter on wormhole while displaying better mean and median ns/write.
  
BenchmarkWriteLatency/Ascending/Wormhole/Workers1-48          	 8788957	       131.6 ns/op	    233_274 p100-ns	       110.0 p50-ns	       200.0 p95-ns	      1610 p99-ns	    976551 samples	      51 B/op	       1 allocs/op

BenchmarkWriteLatency/Ascending/Tidwall/Workers1-48           	 8436313	       135.5 ns/op	    305_292 p100-ns	       130.0 p50-ns	       140.0 p95-ns	       700.0 p99-ns	    937369 samples	      68 B/op	       0 allocs/op

~~~

| Container / traversal                                     | ns/key | L1 hit estimate | L1 misses/key | Instructions/key | Ins/cycle |
| ---------------------------                               | -----: | --------------: | ------------: | ---------------: | --------: |
| tidwall / full ordered scan                               |   2.39 |          97.37% |         0.420 |            23.95 |      2.38 |
| insdict / sorted inserts + Ascend (cost of 1 indirection) |   3.89 |          97.90% |         0.633 |            49.05 |      3.01 |
| insdict / unordered All                                   |   0.74 |          87.50% |         0.501 |            14.00 |      4.49 |

## arg preformance regression vs C

~~~
$ concbench words.txt 1000000 20
worker CPUs=[0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47] (w\
orker i uses CPUs[i % 48], verified after pinning)
corpus keys=1000000 sha256=3f962a45de138d589a31725b5831cfba78d893c564255b92f1ea79320847585b
Go go1.26.4-pont-onethread GOMAXPROCS=20; prepared keys, automatic epoch parking
workload prefetch=true
load worker 0 250000
load worker 250000 500000
load worker 500000 750000
load worker 750000 1000000
load x4 4.56 mops count=1000000 dt_ns=219346580 clocked=false workload=fresh
probe with 20 threads. each round takes 3s
probe x20 44.90 mops count=134880000 misses=0
...

vs C at 57 mops
----------

• Bisect and alternating parent/child runs confirmed three drops:

   Commit     Change                                       Before → after, Mops/s
  ━━━━━━━━━  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  ━━━━━━━━━━━━━━━━━━━━━━━━
   60e3550    Initial generic conversion                            12.21 → 11.70
  ─────────  ───────────────────────────────────────────  ────────────────────────
   6760239    Numeric leaf slots; shared layout changes             11.55 → 11.23
  ─────────  ───────────────────────────────────────────  ────────────────────────
   08b5d30    String hash-slot array moved outside leaf             11.12 → 10.35

  Each comparison used pinned CPUs 0–3, fixed queries, and nine measured rounds per revision.

  The largest confirmed individual drop is 08b5d30, about 7%. The regression accumulated across multiple commits.

  Evidence committed as d415a39. No library changes made.


• The three commits changed these specific things:

  1. 60e3550: converted the string-only implementation to generics.
     Prepared string keys grew from 24 to 48 bytes: they now 
	 hold both the native key and its routing string, plus a 
	 readiness flag. Lookups pass both representations and check
     readiness. Leaf matching also changed from direct string equality:

     stored.key == key

     to generic ordering:

     orderedCompare(stored.key, key) == 0

     That supports ordered types and our NaN-last behavior, but 
	 changes the string lookup code too. Confirmed throughput: 
	 12.21 → 11.70 Mops/s, about 4.2% lower.

  2. 6760239: introduced stable storage for numeric keys.
     Numeric leaves gained a block of 128 records and bucket chains, 
	 avoiding individual record allocations and shifting records during inserts.

     However, strings shared the same leaf struct, so string 
	 leaves also acquired unused numeric bookkeeping: 256 bucket 
	 bytes, 128 chain bytes, a record-block pointer, and a
     dense flag. Every string leaf lookup also acquired a
	 branch checking that flag.

     Confirmed throughput: 11.55 → 11.23 Mops/s, about 2.8% lower.

  3. 08b5d30: separated numeric storage to reduce GC scanning—and 
     moved string storage outside the leaf.
     This is the largest confirmed individual drop. The string 
	 record-pointer array changed from:

     hs [128]*record[K, V]  // embedded inside the leaf

     to:

     hs *[128]*record[K, V] // separately allocated array

     Previously, accessing a record loaded its pointer directly 
	 from the leaf. Afterward, it first had to load the pointer 
	 to the separate array, then load the record pointer.
     That adds a dependent pointer load and changes cache locality. 
	 The prefetch path also needs that array pointer.

     Confirmed throughput: 11.12 → 10.35 Mops/s, about 6.9% lower.

The bisect confirms the cost of each whole commit. It does 
not yet isolate the cost of each field, branch, or comparison
within those commits. These storage and lookup choices
can be revised while keeping the generic API and the
numeric insertion improvements.
~~~
