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
import "github.com/glycerine/go-wormhole-kv"

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
| Container         | Heap MB| B/key | extra GC pointer tracing load (bytes) |
| ----------------- | ------: | ----: | ------------------------------------: |
| glycerine/rbtree  |    6.10 | 64.00 |                               4000056 |
|>Wormhole (meta on)|    5.54 | 58.07 |                               3124336 |
| insdict.Dict      |    5.30 | 55.62 |                                   112 |
| Turtle            |    4.78 | 50.15 |                                  1720 |
|>Wormhole(meta off)|    3.83 | 40.13 |                               1452656 |
| bufftree.BPTree   |    2.44 | 25.60 |                                 40264 |
| tidwall/btree.Map |    2.40 | 25.13 |                                141872 |
| builtin Go map    |    2.26 | 23.65 |                                  4248 |

> conclude: notice how Wormhole moved in the ranking.
>
> 5.81/4.0 => 1.45x less heap w/ meta cache off. 
>
> 3.1M/1.45M => 2.1x less GC trace load.
>

With the metadata caches in use:

| Operation (showing ns/key)    | Wormhole | Turtle | BP-Tree | tidwall/btree | red-blk tree | insdict | builtin map |
| ----------------------------- | -------: | -----: | ------: | ------------: | -----------: | ------: | ----------: |
| Tree `Get`, hit               |     48.8 |  114.2 |    91.6 |         108.8 |        194.3 |    20.2 |        15.6 |
| Tree `Get`, miss              |     37.8 |  113.6 |    89.8 |         108.9 |        212.2 |    17.7 |        14.4 |
| Tree `Put`, fresh key         |    116.7 |  118.9 |   106.8 |         151.9 |        356.2 |    58.4 |        74.9 |
| Ordered scan, maximum 10,000  |     2.44 |   2.75 |    2.68 |          4.53 |        15.12 |    5.42 |         N/A |
| Ordered scan, maximum 100,000 |     2.42 |   2.72 |    2.66 |          4.52 |        15.26 |    5.33 |         N/A |
| Full traversal                |     2.40 |   2.68 |    3.82 |          2.33 |        14.96 |    0.61 |        9.17 |

versus MetaCache off, Wormhole Get (hit) can be a litte slower.

| Operation (showing ns/key)    | Wormhole | Turtle | BP-Tree | tidwall/btree | red-blk tree | insdict | builtin map |
| ----------------------------- | -------: | -----: | ------: | ------------: | -----------: | ------: | ----------: |
| Tree `Get`, hit               |     49.7 |  113.1 |    92.3 |         109.0 |        191.6 |    20.2 |        15.6 |
| Tree `Get`, miss              |     38.3 |  113.9 |    90.4 |         109.2 |        214.6 |    17.7 |        14.4 |
| Tree `Put`, fresh key         |    119.2 |  119.4 |   107.0 |         150.9 |        402.6 |    55.2 |        74.5 |
| Ordered scan, maximum 10,000  |     2.44 |   2.73 |    2.64 |          4.55 |        15.14 |    5.44 |         N/A |
| Ordered scan, maximum 100,000 |     2.45 |   2.69 |    2.66 |          4.52 |        15.28 |    5.33 |         N/A |
| Full traversal                |     2.43 |   2.71 |    3.82 |          2.34 |        14.88 |    0.61 |        9.19 |

| key          | repo                                                    |
| ------------:| -------------------------------------------------------:|
|Wormhole      | https://github.com/glycerine/go-wormhole-kv (this repo) |
|Turtle        | unpublished port of https://github.com/mathworks/turtle_kv |
|BP-Tree       | https://github.com/glycerine/bufftree |
|tidwall/btree | https://github.com/tidwall/btree      |
|red-blk       | https://github.com/glycerine/rbtree   |
|insdict       | https://github.com/glycerine/insdict  |

---------------
Copyright (C) 2026 Jason E. Aten, Ph.D.

License: MIT (see LICENSE file)

Derived under MIT license from the upstream C code https://github.com/wuxb45/wormhole , which is
Copyright (c) 2016-2026 by Xingbo Wu
