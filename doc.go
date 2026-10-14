// Package wormhole implements a concurrent, ordered in-memory map with ordered
// generic keys and arbitrary values, using the Wormhole paper's hash-indexed prefix trie, linked leaves,
// stable record slots with tag-bucket chains, lazy leaf sorting, and versioned two-table metadata updates.
// It is ported from the C implementation in the parent directory.
//
// Construct a Map with New. Each concurrent caller needs its own Ref, which
// must be closed after use. Point operations automatically leave their metadata
// epoch before returning or waiting for a leaf lock. Iterators reserve their
// Ref and hold a leaf read lock until they advance, Park, or Close; iteration
// and range deletion are not whole-map transactions. Keys use natural order, with NaNs after positive infinity; NaN payloads
// identify one key, as do positive and negative zero. String keys use Go's
// bytewise order and may contain arbitrary bytes, including the empty string.
//
// Values are copied under leaf locks; callers synchronize mutations of objects
// reachable through stored values. String bytes must remain immutable.
// Returned strings remain valid after updates, deletion, and iterator movement.
// Go garbage collection replaces
// native allocation/reclamation hooks; explicit metadata grace periods still
// protect the two reused prefix tables. Callbacks execute under leaf locks and
// must not re-enter operations that acquire leaf locks on the same Map.
package wormhole
