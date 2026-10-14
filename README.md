# Wormhole in Go

Wormhole is an interesting and very fast design for 
an in-memory ordered-key/value store, [originally in C](https://github.com/wuxb45/wormhole) 

The theory of operation is described in [the Eurosys 2019 paper](wormhole.pdf) by
Xingo Wu, Fan Ni, and Song Jiang.

# concurrency - the name of the game

The concurrency design in the paper/C repo is really aiming for high performance, and
we remain faithful to that emphasis in the Go port.

This is an industrial strength key/value store design. It uses a linux-kernel like
RCU (Read-Copy-Update) approach, although still does leaf locking with a custom
optimistic spin-lock. Multiple simultaneous writers and readers are supported.

Serializable transactions are not an aim; unabashed maximizing of single key
write and read performance is the name of the game here. And oh boy, does Wormhole
deliver.

Our Go port turns this up to 11, and even surpasses the performance
of the original C.

C Wormhole, reading with 20 threads:
~~~
concbench.out words.txt 1000000 20
...
probe with 20 threads. each round takes 3 seconds
probe x20 51.89 mops
probe x20 52.03 mops
probe x20 51.98 mops
~~~

Go Wormhole, reading with 20 goroutines, gets 13% more throughput, 
approaching 60 million reads per second on the [words.txt corpus of 479K English words](https://github.com/dwyl/english-words).
These words have a median of 9 characters.

~~~
concbench words.txt 1000000 20
...
probe with 20 threads. each round takes 3s
probe x20 59.60 mops count=178990000 misses=0
probe x20 59.02 mops count=177190000 misses=0
probe x20 58.94 mops count=177010000 misses=0
~~~

The table below is for Wormhole in Go, on map[uint64]uint64 trees and hash tables. The first two get lines
reflect randomized searching. The last shows unsorted sequential
scan so the hash tables can really strut. All numbers are the average nanoseconds per key.
Apple silicon ARM64 numbers are shown; AMD64 numbers has similar ratios.

| Operation (report ns/key)  | Wormhole[1] | Turtle[2] | BP-Tree[3] | tidwall/btree[4] | red-black tree[5] | insdict[6] | builtin map |
| -------------------------- | ----------: | --------: | ---------: | ---------------: | --------------: | ---------: | ----------: |
| Get hit                    |     18.0 |   57.6 |    51.3 |          56.9 |        102.0 |     5.6 |         4.0 |
| Get miss                   |     18.1 |   61.9 |    61.7 |          56.6 |        129.2 |     5.5 |         4.4 |
| Put fresh                  |     48.4 |   46.1 |    66.4 |          96.8 |        185.8 |    18.8 |        34.4 |
| Ordered scan               |     1.28 |   1.28 |    1.35 |          1.79 |         9.93 |    1.97 |         N/A |
| Unordered scan             |         1.32 |   1.28 |    1.94 |          1.03 |         9.83 |    0.31 |        4.68 |

In these micro benchmarks: 

Compared to the fast in memory B-tree tidwall/btree, Wormhole is 2x as fast at Puts (writing keys; 96.8 / 48.4 == 2.0),
and 3.1x faster at Gets (reading keys; 56.6/18.1 == 3.1).

Although the Turtle design matches Wormhole for writes, its reads are still 3x slower. The BP-Tree does not
come close to Wormhole on either readers or writes, and is restricted to a single goroutine.

Full table scan performance is dominated by the insdict, where pre-fetching sequential array elements makes it
15x faster than the built-in Go map (4.68/0.31 = 15.1). Wormhole and friends are 3x faster than the built in map,
and all support ordered scans and range queries.

[1] Wormhole is this repo; https://github.com/glycerine/go-wormhole-kv

[2] Turtle K/V in Go is: https://github.com/glycerine/go-turtle-kv (See also the C https://github.com/mathworks/turtle_kv)

[3] BP-tree: https://github.com/glycerine/bufftree

[4] tidwall/btree is: https://github.com/tidwall/btree

[5] the red-black tree used was: https://github.com/glycerine/rbtree

[6] insdict.Dict is: https://github.com/glycerine/insdict

# memory use

| Container         | Heap bytes | MiB  | B/key | extra GC pointer tracing load (bytes) |
| ----------------- | ---------: | ---: | ----: | ------------------------------------: |
| glycerine/rbtree  |    6400080 | 6.10 | 64.00 |                               4000056 |
| insdict.Dict      |    5562488 | 5.30 | 55.62 |                                   112 |
| Turtle            |    5015336 | 4.78 | 50.15 |                                  1720 |
| Wormhole          |    3718624 | 3.55 | 37.19 |                                342640 |
| bufftree.BPTree   |    2560376 | 2.44 | 25.60 |                                 40256 |
| tidwall/btree.Map |    2513408 | 2.40 | 25.13 |                                141872 |
| builtin Go map    |    2364608 | 2.26 | 23.65 |                                  4248 |


# concurreny design

Reading and writing goroutines share a wormhole Map, but each uses its own Ref (handle)
to the map. The user must call Close() on it when done with the Ref.

| Object       | Concurrency contract |
|--------------|----------------------|
| Map          | Shared across goroutines. Concurrent Ref() creation and Len() calls are supported. Do not copy the map. |
| Ref          | One goroutine at a time. Do not copy it. Call Close() when finished; closing must not overlap its operations. |
| Iterator     | One goroutine at a time. Reserves its Ref until closed, even while parked. |
| Prepared Key | May be shared across goroutines. |

These contracts are documented in [map.go](map.go:83).

RCU is used to:

1. Protect routing metadata with epochs and grace periods. Structural writers update the inactive prefix table and atomically publish a new version. Before reusing the old table, they wait for readers using that version to finish. Readers automatically announce and leave their metadata epochs; developers don’t manually manage them. Go GC handles object lifetime, but cannot replace this grace period because the metadata tables are reused. See [epoch.go](epoch.go:24).

2. Protect records with per-leaf read/write locks. Multiple readers can access a leaf together. A writer gets exclusive access to that leaf. Writers targeting different leaves can proceed concurrently; structural metadata changes serialize through metalock. After routing, callers leave their metadata epoch before waiting for a leaf lock, then validate the leaf version and retry if necessary. See [map.go](map.go:154).

Consequently, reads are not lock-free, and writes are not globally serialized.

Individual point operations are synchronized, but a sequence such as Get followed by Put is not an atomic read-modify-write. Two callers could overwrite each other’s updates. We already offer callbacks for that:

- Merge: atomically inspect and insert/replace one key, including an absent key.
- Update: atomically inspect and replace an existing key.
- Inspect: inspect under a leaf read lock.

These callbacks execute under the relevant leaf lock. They must not re-enter operations that acquire leaf locks on the same map, even through another Ref. See [callback.go](callback.go:6).

Important limits:

- Scans and iterators are not whole-map snapshots. Later leaves may reflect later concurrent updates. A positioned iterator holds a leaf read lock and can delay writers until it advances, parks, or closes.
- Range deletion is not a multi-key transaction. Concurrent operations can interleave as it progresses.


# broader details

The port keeps the hash-indexed prefix trie, two-choice cuckoo metadata tables, linked leaves
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

go
~~~
import "github.com/glycerine/go-wormhole-kv"

m := wormhole.New[string, string]()
r := m.Ref() // one handle per concurrent caller
defer r.Close()
if err := r.Put("user:42", "Ada"); err != nil {
    panic(err)
}
value, found := r.Get("user:42")
~~~

Runnable examples also cover [iteration and callbacks](example_test.go).
Construct maps with New; do not copy a Map or Ref. Close each Ref when done.
New and zero-valued Options use the lower-memory metadata configuration:
the two-byte metadata lookup cache is off and initial hash tables are smaller.
To enable that cache and the larger initial reservation, use
NewWithOptions[K, V](Options{MetaCache: true}). The separate numeric leaf-route
cache remains enabled in both modes, and hash tables still grow automatically.
Choose the option at construction; it cannot be changed on a live map.
Map[K cmp.Ordered, V any] stores native keys and values, including named
ordered key types and values containing slices, maps or pointers. Keys follow
natural numeric order or bytewise string order. NaNs sort after positive
infinity; all NaN payloads identify one key. Positive and negative zero also
identify one key. Range bounds and iterator seeks use the same ordering.

Values are copied by assignment under leaf locks. Callers must synchronize
mutations of objects reachable through stored values. Strings are retained
without copying and must remain immutable. Numeric routing bytes use scratch
storage owned by each Ref; only metadata separators and prepared keys retain
encoded copies. Existing-key writes do not allocate replacement records.

| Operation                        | Go API                                 |
| -------------------------------- | -------------------------------------- |
| Point reads/writes               | Ref.Get, Probe, Put, Delete    |
| Atomic callback under write lock | Ref.Merge, Update                  |
| Callback under read lock         | Ref.Inspect                          |
| Range deletion                   | Ref.DeleteRange(start, end) for [start,end), DeleteFrom(start) without an upper bound |
| Ordered iteration                | Ref.Iterator, then Seek, Valid, Peek, Next, Skip, Inspect, Park, Close |
| Ordered callback scan            | Ref.Scan(start, limit, visit) returns the number visited; a false callback stops the scan |
| Repeated prepared-key operations | PrepareKey, Ref.PutKey, ProbeKey |
| Number of keys                   | Map.Len                              |

Size-changing writes update per-Ref insertion/deletion tallies rather than a
shared global counter. Len takes a consistent snapshot across open references;
it costs O(open Refs), locks the reference registry, and can retry during writes.
Closing a Ref preserves its contribution. See [the counter measurements](bench/COUNTER.md)
for the write-throughput benefit and the increased cost of Len.

Each Ref is used by one goroutine at a time. An iterator reserves its Ref until
closed, including while parked. It holds a leaf read lock while positioned,
so writers to that leaf wait until it moves, parks, or closes. Iteration uses
left-to-right lock coupling and is not a whole-map snapshot. Range deletion
also releases locks between leaves; it is not an atomic transaction. Scan
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

The public API follows native's automatically parked whsafe operations.
Native raw-memory views, allocator hooks, manual epoch controls, destruction,
and separate unsynchronized entry points are represented by native Go values,
automatic epoch handling, GC, and the synchronized API rather than C ABI
wrappers. To remove all records use DeleteFrom(""); to release a map close its
Refs and discard it. As in native metadata, split anchors have a 65,535-byte
length limit; a split that cannot satisfy it returns ErrAnchorTooLong without
inserting the new key. Long keys that do not require such an anchor are allowed.

Run from this directory:

~~~
go test ./...
go test -tags=native ./...       # requires a C compiler and parent native sources
go test -race -tags=native ./...
go vet -tags=native ./...
~~~

BDD-style tests build from leaf placement and prefix metadata through complete
map operations, iterator/range behavior, and concurrency. Randomized model tests
and fuzz targets check arbitrary keys and operation histories. The native
build tag compares slots, tags, sorted indices, split/merge anchors, metadata,
callbacks, and workload generation against the parent C sources. Controlled
concurrency tests cover publication, table reuse, stale routes, lock ordering,
and small histories checked for linearizability. Native oracle fixtures live
in test_vectors/; Go's testdata/ name is reserved for fuzz-generated data.

[Write benchmarks](bench/README.md) compare identical traces with
github.com/tidwall/btree, including raw latency quantiles. They run in a
separate module. Linux benchmark workers pin their OS threads to individual
CPUs and verify affinity; select physical cores with taskset.

To reproduce the native million-key workload, build native concbench.out from
the parent directory and build the Go driver here, then run sequentially:

~~~
go build -o /tmp/wormhole-concbench ./cmd/concbench
taskset -c 0-3 ../concbench.out ../words.txt 1000000 4
taskset -c 0-3 /tmp/wormhole-concbench ../words.txt 1000000 4
~~~

Both use four load workers, native six-word corpus generation and Lehmer RNG,
10,000-probe batches, and three three-second rounds. The dictionary is an
external input, not required for tests. The Go driver prints a corpus hash,
CPU mapping, counts, and misses; -cpuprofile profiles probes. Ordinary GC stays
enabled. 

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
| Tree Get, hit               |     48.8 |  114.2 |    91.6 |         108.8 |        194.3 |    20.2 |        15.6 |
| Tree Get, miss              |     37.8 |  113.6 |    89.8 |         108.9 |        212.2 |    17.7 |        14.4 |
| Tree Put, fresh key         |    116.7 |  118.9 |   106.8 |         151.9 |        356.2 |    58.4 |        74.9 |
| Ordered scan, maximum 100,000 |     2.42 |   2.72 |    2.66 |          4.52 |        15.26 |    5.33 |         N/A |
| Full traversal (map too)      |     2.40 |   2.68 |    3.82 |          2.33 |        14.96 |    0.61 |        9.17 |

versus MetaCache off, Wormhole Get (hit) can be a litte slower.

| Operation (showing ns/key)    | Wormhole | Turtle | BP-Tree | tidwall/btree | red-blk tree | insdict | builtin map |
| ----------------------------- | -------: | -----: | ------: | ------------: | -----------: | ------: | ----------: |
| Tree Get, hit               |     49.7 |  113.1 |    92.3 |         109.0 |        191.6 |    20.2 |        15.6 |
| Tree Get, miss              |     38.3 |  113.9 |    90.4 |         109.2 |        214.6 |    17.7 |        14.4 |
| Tree Put, fresh key         |    119.2 |  119.4 |   107.0 |         150.9 |        402.6 |    55.2 |        74.5 |
| Ordered scan, maximum 100,000 |     2.45 |   2.69 |    2.66 |          4.52 |        15.28 |    5.33 |         N/A |
| Full traversal (map too)      |     2.43 |   2.71 |    3.82 |          2.34 |        14.88 |    0.61 |        9.19 |

| key          | repo                                                    |
| ------------:| -------------------------------------------------------:|
|Wormhole      | https://github.com/glycerine/go-wormhole-kv (this repo) |
|Turtle        | https://github.com/glycerine/go-turtle-kv |
|BP-Tree       | https://github.com/glycerine/bufftree |
|tidwall/btree | https://github.com/tidwall/btree      |
|red-blk       | https://github.com/glycerine/rbtree   |
|insdict       | https://github.com/glycerine/insdict  |

---------------
Copyright (C) 2026 Jason E. Aten, Ph.D.

License: MIT (see LICENSE file)

Derived under MIT license from the upstream C code https://github.com/wuxb45/wormhole , which is
Copyright (c) 2016-2026 by Xingbo Wu

