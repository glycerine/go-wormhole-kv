# Concurrency correctness audit

Audited commit: `6c103e489e2178282d6f8737a1a23107b524b3a6`.
Audit date: 2026-10-14.

This was an audit only. No implementation or repository test files were changed.

## Result

I found one reproducible **iterator deadlock hazard that needs an explicit public API restriction**. It is a contract/documentation defect, not a failure of mutual exclusion in the custom mutex. Details and fixes are below.

I did **not** find a concrete data race, stale-route acceptance, or premature metadata-table reuse in the inspected implementation. In particular, parking before acquiring the leaf lock, checking the leaf version under that lock, and releasing leaf locks before waiting for metadata readers are coherent with the surrounding synchronization. The post-unlock metadata updates remain protected by `metalock`.

Both the ordinary and race-enabled main-module test suites passed. These results support the source review; they do not establish correctness for every execution or architecture.

## Finding A1 — P2: cross-Ref operations while holding an iterator can deadlock, including reads

**Locations:** `iterator.go:5–12`, `iterator.go:19–26`, `map.go:121–128`, `leaf_mutex.go:31–41`, and `leaf_mutex.go:78–88`. The public contract in `README.md:104–130` and `README.md:198–206` has the same omission.

**Classification:** confirmed liveness hazard; missing iterator usage restriction. The lock behaves as designed. The API documents that an iterator holds a read lock and reserves its own Ref, but does not explain that synchronous operations through another Ref can deadlock the iterator's owner. The cross-Ref prohibition is documented for callbacks, not for ordinary code between iterator calls.

**Trigger:** one goroutine keeps an iterator positioned and performs an operation through a second Ref on the same map. Each Ref still has only one caller; no Ref or Map is copied. Even a `Get` can hang permanently if a writer arrives first.

### Exact execution

Use a map containing `"a"`; both `"a"` and `"b"` fit in its single leaf, L.

1. Goroutine A calls `it.Seek("a")`. The iterator retains L's read lock; its reader count is 1.
2. Goroutine B calls `writer.Put("b", value)` through its own Ref. `lockSlow` reserves L's writer bit, leaving state `leafWriterBit | 1`, then waits for the existing reader to leave.
3. A calls `otherRef.Get("a")`. `checkAvailable` checks only `otherRef.iterator`, so it allows the call. Routing completes and parks the epoch normally.
4. `Get` tries `L.mu.RLock()`. The reserved writer bit prevents it from becoming another reader.
5. A cannot reach `it.Park()` or `it.Close()` until `Get` returns. B cannot acquire its write lock until A releases the iterator. Neither operation completes.

The dependency cycle is:

```text
A's Get -> B's pending write -> A's retained iterator lock -> A finishing Get
```

An immediate write through the second Ref needs no competing goroutine to cause a self-deadlock. The read-only variant is easier to miss because it succeeds when no writer is pending. Two positioned iterators owned by the same goroutine can encounter the same recursive-read problem.

This is the usual restriction of a writer-preferring read/write lock: pending writers exclude additional readers, including recursive read acquisition. The standard library documents this restriction explicitly for [sync.RWMutex](https://pkg.go.dev/sync#RWMutex). Changing the map's mutex to `sync.RWMutex` would retain this behavior.

### Verification

A temporary Go test overlay reproduced this exact schedule without modifying repository source. The child test waited until `atomic.LoadUint32(&m.first.mu.state)&leafWriterBit != 0` before making the cross-Ref `Get`, so the result did not depend on a sleep guessing whether the writer had started. The child then timed out with the reader in `rlockSlow` and writer in `lockSlow`. A companion test parked the iterator before `Get`; both reader and writer completed.

The essential reproducer, suitable for a same-package **subprocess test with a timeout**, is:

```go
m := New[string, string]()
owner, writer, reader := m.Ref(), m.Ref(), m.Ref()
if err := owner.Put("a", "value"); err != nil {
    t.Fatal(err)
}
it := owner.Iterator()
it.Seek("a")
go func() { _ = writer.Put("b", "other") }()

// Deterministically wait until the writer excludes new readers.
for atomic.LoadUint32(&m.first.mu.state)&leafWriterBit == 0 {
    runtime.Gosched()
}
reader.Get("a") // Blocks permanently in this execution.
it.Close()      // Unreachable.
```

The reproducer uses `runtime`, `sync/atomic`, and `testing`. Run it in a child test process, rather than leaving permanently blocked goroutines in the main test suite.

### How to fix

The smallest fix that preserves the current iterator semantics is to make the restriction explicit in the exported `Iterator` documentation, `Ref` documentation, and README concurrency contract:

> While holding a positioned iterator, do not synchronously perform other operations that acquire leaf locks on the same Map, even through another Ref or iterator. This includes reads, which can block behind a pending writer. Park or close the iterator first. Do not wait for work that may need those locks before releasing the iterator.

Explain that `Park` invalidates the position and keeps the Ref reserved. Use another Ref for the intervening operation and `Seek` to position the existing iterator again, or close the iterator before reusing its own Ref. `Peek`, `Valid`, and the iterator's own movement methods remain supported as documented; the warning concerns additional lock acquisitions outside that iterator's traversal.

Add a regression/documentation test for the pending-writer schedule and a positive test showing that parking first permits both operations to finish. Retain the current same-Ref checks, but do not describe them as protection against cross-Ref deadlocks.

If cross-Ref reads during iteration must instead be supported, that requires an iterator design change. One option is to copy a leaf's sorted records while locked, release the lock before returning control to the caller, and resume through a fresh route strictly after the last consumed key when the copied batch is exhausted. Such a design must specify concurrent-update visibility, duplicate avoidance, value ownership, and how `Iterator.Inspect`'s lock guarantee changes. Removing writer preference alone is not a sufficient fix: it does not solve recursive writes or arbitrary multi-leaf lock cycles.

## Audit of the optimistic routing and epochs

### Announce, reload, then access the table

Relevant code: `map.go:137–183`, `specialized_generated.go:9–30` and `172–193`, `epoch.go:112–132`.

`protectSnapshot` first announces the version of an immutable descriptor, reloads `m.current`, and only then returns a descriptor whose mutable table the caller can inspect. A changed descriptor causes the announcement to advance to the newly loaded version. This ordering matters because the first descriptor was loaded while the caller was parked.

The proof relies on the sequentially consistent behavior of the `sync/atomic` publication and epoch operations, including ordering between different atomic locations. This is the guarantee documented by the [Go memory model](https://go.dev/ref/mem#atomic).

For a writer retiring a table, consider its scan of this reader's epoch:

- If the writer accepts a parked state before the reader announces, the reader's subsequent publication reload observes that writer's publication or a later one. It does not dereference the table solely on the strength of its earlier, unprotected load.
- If the reader announces before the writer scans, an older announcement holds the writer in `wait`. An announcement at or above the retirement target means the reader will access a publication that no longer needs the table being retired. Forwarding the announcement is safe because no table access precedes that forwarding.
- A later writer has to perform another grace-period scan before reusing the reader's selected table. The reader's announcement protects that table until routing finishes and the reader parks.

The two physical table pointers alternate, but snapshot descriptors are freshly allocated and immutable. Comparing descriptors, rather than comparing physical table pointers, avoids confusing a reused table with the original publication. A live Go pointer also prevents its descriptor from being reclaimed and allocated again at the same address.

`epochRegistry.register` and `wait` serialize registry membership with `q.mu`. A newly registered reader excluded from an ongoing scan must still perform the publication handshake before accessing metadata. `unregister` parks before acquiring `q.mu`, so closing a handle does not wait behind a grace period while keeping its own old announcement active.

**Conclusion:** no failing interleaving identified in this handshake. Do not remove the reload, begin table access before it, or replace these atomics with weaker operations without a new proof.

### Parking before locking the leaf is necessary and safe here

The reader uses its epoch only to obtain a leaf pointer. It then parks, takes that leaf's read or write lock, and accepts the route only when `l.version <= s.version`. `l.version` is an ordinary field, but both the check and every structural update to it occur under the leaf lock. There is no unlocked optimistic read of leaf records followed by an attempted version-based repair.

If a split or merge occurs after routing and before the lock is acquired, the affected leaf's version advances beyond the routed snapshot's version. The caller drops the acquired lock and routes again. A merged-away right leaf is versioned too, so a retained pointer cannot expose its old records. GC preserves the physical leaf's lifetime during this interval; the version check preserves its logical validity.

The `<=` test is intentional: a leaf can remain unchanged through unrelated metadata publications. Equality would reject valid routes to those unchanged leaves. Cached Put fingers instead use equality with the remembered leaf version, followed by bounds checking or a previously established prefix-coverage proof (`map.go:236–272` and the generated equivalents).

Keeping an old epoch active while waiting for a leaf lock would permit a structural writer holding that lock to wait for that very reader's epoch. The current code avoids that dependency. The retry paths return to the handshake only after unlocking.

### Releasing leaf locks before waiting and replaying metadata

Relevant code: `map.go:291–319`, `map.go:379–395`, `z_dense_split.go:15–36`, and `meta.go:532–631`.

A structural writer holds `metalock` across all of these steps:

1. Update links and affected leaf versions while holding the relevant leaf write locks.
2. Apply the change to the inactive table and publish the new descriptor.
3. Release the relevant leaf locks, except the left lock deliberately retained by range deletion.
4. Wait for old metadata readers.
5. Apply the same change to the retired table, then release `metalock`.

The calls to `old.table.insertAnchor(right)` and `old.table.removeAnchor(right)` occur after leaf unlock, but their leaf accesses concern anchors and neighbor links. Published anchors do not change, and all linked-list changes also require `metalock`, which the original writer still holds. These calls do not read the leaf's mutable records, tags, counts, or sorted arrays. Another writer may acquire a released leaf and prepare a split or merge, but it cannot publish neighbor-link changes until the first writer releases `metalock`.

This also explains the writes to `right.next.prev` without the successor's leaf lock: production accesses to `prev` use the structural lock. Forward traversal uses `next`, whose mutation requires both the predecessor's leaf write lock and `metalock`. A future reverse iterator cannot safely read `prev` under only that leaf's read lock; it would need a synchronization design of its own.

`nextSnapshot` reuses a physical table only after the preceding structural writer has completed its grace period and replay under the same `metalock`. It does not assume that GC makes table mutation safe.

**Conclusion:** the post-unlock replay is protected by an existing lock. Reacquiring leaf locks while holding `metalock` would introduce a reverse lock-order dependency and is not an appropriate precautionary fix.

## Other locking paths checked

| Shared state or path | Protection and assessment |
| --- | --- |
| Leaf values, tags, occupancy, count, and version | Leaf read/write lock. Get copies the value before unlocking; Put replacement stays under the write lock. Split/merge data movement holds the affected leaf locks. |
| Leaf mutex state | Successful CAS owns either a reader count or the writer bit. A reserved writer blocks new readers and waits for existing readers to drain. `RUnlock` atomically decrements the count; `Unlock` releases the exclusive state. No mutual-exclusion failure identified for valid use. |
| Architecture-specific write unlock | `leaf_release_amd64.s:3–6` uses `MOVL`; `leaf_release_arm64.s:3–6` uses `STLRW`; other targets use `atomic.StoreUint32`. The custom assembly needs its own ordering argument and is not validated merely by adding race-detector annotations. Only amd64 was exercised here. |
| Race detector integration | `leaf_mutex_race.go` separates writer publication from aggregated reader completion and hides bookkeeping atomics. Readers acquire only writer synchronization, avoiding an artificial synchronization edge between independent readers. The intentional-reader-race subprocess test passed. |
| Lazy sorting | `syncSortedRead` always takes `sortMu` under the leaf read lock. After that sort finishes, the index remains sorted until a writer can acquire the leaf, so other readers can inspect it after releasing `sortMu`. Point readers do not inspect the sorted index. |
| Iterator/Scan traversal | The successor is locked before releasing the current leaf. Holding the predecessor prevents the successor from being unlinked. Traversal and merge acquisition both proceed left to right. Finding A1 concerns caller-held locks outside this internal traversal order. |
| Range deletion | `unlinkRight(..., true)` intentionally retains the left lock through the grace period. Epoch readers do not wait for leaf locks while active, so that retention does not itself create an epoch/leaf cycle. It can extend the time other operations wait for that leaf. |
| Callbacks | User callbacks run after the metadata epoch is parked. Deferred cleanup releases point/Scan locks and restores callback state on panic. Iterator.Inspect intentionally retains the positioned iterator's lock after a panic, as documented. Reentry through the same Ref is rejected; the cross-Ref callback prohibition remains a caller obligation. |
| Numeric route caches and metadata shortcuts | Mutable cache entries belong to their respective metadata tables and use the same publication and grace-period protection. The two tables allocate separate mutable metadata/cache objects. |
| Ref-local routing scratch and Put hints | Safe only under the documented single-caller, non-copying Ref contract. No shared mutable routing scratch was found. |
| Len and Ref retirement | Registry membership and retired contributions use `q.mu`; live contributions use atomic monotonic addition/removal tallies. Equal aggregate tallies in consecutive passes imply each component was unchanged between its corresponding reads, assuming no full-width wrap. Those intervals overlap, providing a snapshot. The single-Ref shortcut brackets the removal read with equal addition reads. |

The internal blocking order is leaf locks from left to right, followed by `metalock`, followed by the registry mutex during retirement. Sorting takes `sortMu` while already holding a leaf lock and does not acquire another leaf or structural lock. Epoch-protected routing does not block on those locks. No internal cycle was identified within these paths.

## Progress and validation limits

- An iterator retained indefinitely or a callback that does not return can indefinitely delay writers. Pending writer reservation then delays subsequent readers too. Finding A1 describes the missing caller-facing consequence.
- The mutex has bounded polling before yielding, not a bounded acquisition time or FIFO fairness guarantee. Continuous contention can delay particular callers.
- A descheduled metadata reader can delay table retirement. While waiting, a structural writer holds `metalock` and the registry mutex. Point operations on available leaves can still proceed, but further structural changes, Ref registration/retirement, and Len can wait.
- Len retries while holding the registry mutex. Continuous tally changes can prevent a successful snapshot and consequently delay structural retirement and Ref lifecycle operations. The retrying behavior is already documented; no wait-free progress is claimed.
- The race build uses portable leaf/tag paths in places where the ordinary amd64 build uses SIMD. Passing the race suite does not instrument those excluded assembly paths or prove hardware release ordering. The ordinary suite exercised the host's selected non-race paths.
- Runtime validation was on `linux/amd64`, using `go version go1.26.4-pont-onethread linux/amd64`. ARM64 and other fallback targets received source inspection, not execution. Native C differential tests and the separate `bench` module were not run for this locking audit.

## Commands and results

The build cache was redirected to `/mnt/oldrog/tmp/wormhole-concur-gocache` to keep tool output outside the repository.

```text
GOCACHE=/mnt/oldrog/tmp/wormhole-concur-gocache go test ./...
PASS: root package, cmd/concbench, internal/benchworkload, internal/cpuaffinity

GOCACHE=/mnt/oldrog/tmp/wormhole-concur-gocache go test -race ./...
PASS: the same four packages

GOCACHE=/mnt/oldrog/tmp/wormhole-concur-gocache go test \
  -overlay=/mnt/oldrog/tmp/wormhole-concur-audit/overlay.json \
  -run '^TestAuditIterator' -count=1 -v .
PASS: TestAuditIteratorCrossRefReadDeadlock
      (parent verified the expected child timeout and blocked reader/writer stacks)
PASS: TestAuditIteratorCrossRefReadAfterPark
```

The existing suites include epoch grace-period tests, the production snapshot-handshake double-buffer test, stale split/merge routes, access during an outstanding grace period, concurrent point histories with structural churn, concurrent iterators/range deletion/scans, callback panic handling, and Ref-count snapshot/retirement tests. The temporary overlay supplied only the targeted iterator reproducer and its Park control; it did not replace production code.

Recommended follow-up: address A1's API contract and regression coverage. Preserve the publication/epoch/leaf ordering described above; this audit supplies no evidence that replacing that protocol is necessary.
