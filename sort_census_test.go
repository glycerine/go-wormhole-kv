package wormhole

import (
	"encoding/json"
	"math/bits"
	"os"
	"reflect"
	"testing"

	"github.com/glycerine/go-wormhole-kv/internal/benchworkload"
)

type dirtyRunCensus struct {
	Splits       uint64            `json:"splits"`
	LastLeaf     uint64            `json:"last_leaf"`
	Interior     uint64            `json:"interior"`
	DirtyEntries uint64            `json:"dirty_entries"`
	MinDirty     int               `json:"min_dirty"`
	MaxDirty     int               `json:"max_dirty"`
	Buckets      map[string]uint64 `json:"buckets"`
	RadixRuns    uint64            `json:"radix_runs"`
	UniqueTotal  uint64            `json:"unique_bucket_total"`
	SpanTotal    uint64            `json:"bucket_span_total"`
	Unique       map[string]uint64 `json:"unique_buckets"`
}

func (c *dirtyRunCensus) addRadix(l *leaf[string, struct{}]) {
	slots := l.ss[l.sorted:l.n]
	if len(slots) < 32 {
		return
	}
	first := keyHead(l.records[slots[0]].key)
	var varying uint64
	for _, slot := range slots {
		varying |= keyHead(l.records[slot].key) ^ first
	}
	if varying == 0 {
		return
	}
	shift := uint(max(0, bits.Len64(varying)-8))
	var seen [256]bool
	unique, lo, hi := 0, 256, -1
	for _, slot := range slots {
		bucket := int(byte(keyHead(l.records[slot].key) >> shift))
		if !seen[bucket] {
			seen[bucket] = true
			unique++
			lo, hi = min(lo, bucket), max(hi, bucket)
		}
	}
	c.RadixRuns++
	c.UniqueTotal += uint64(unique)
	c.SpanTotal += uint64(hi - lo + 1)
	label := "65-128"
	switch {
	case unique <= 8:
		label = "1-8"
	case unique <= 16:
		label = "9-16"
	case unique <= 32:
		label = "17-32"
	case unique <= 64:
		label = "33-64"
	}
	c.Unique[label]++
}

func (c *dirtyRunCensus) addNumericRadix(l *leaf[uint64, uint64]) {
	slots := l.ss[l.sorted:l.n]
	if len(slots) < 32 {
		return
	}
	first := numericOrderBits(l.records[slots[0]].key, reflect.Uint64)
	var varying uint64
	for _, slot := range slots {
		varying |= numericOrderBits(l.records[slot].key, reflect.Uint64) ^ first
	}
	if varying == 0 {
		return
	}
	shift := uint(max(0, bits.Len64(varying)-8))
	var seen [256]bool
	unique, lo, hi := 0, 256, -1
	for _, slot := range slots {
		bucket := int(byte(numericOrderBits(l.records[slot].key, reflect.Uint64) >> shift))
		if !seen[bucket] {
			seen[bucket] = true
			unique++
			lo, hi = min(lo, bucket), max(hi, bucket)
		}
	}
	c.RadixRuns++
	c.UniqueTotal += uint64(unique)
	c.SpanTotal += uint64(hi - lo + 1)
	label := "65-128"
	switch {
	case unique <= 8:
		label = "1-8"
	case unique <= 16:
		label = "9-16"
	case unique <= 32:
		label = "17-32"
	case unique <= 64:
		label = "33-64"
	}
	c.Unique[label]++
}

func (c *dirtyRunCensus) add(dirty int, last bool) {
	c.Splits++
	c.DirtyEntries += uint64(dirty)
	if c.MinDirty == 0 || dirty < c.MinDirty {
		c.MinDirty = dirty
	}
	if dirty > c.MaxDirty {
		c.MaxDirty = dirty
	}
	if last {
		c.LastLeaf++
	} else {
		c.Interior++
	}
	label := "96-128"
	switch {
	case dirty == 1:
		label = "1"
	case dirty < 8:
		label = "2-7"
	case dirty < 16:
		label = "8-15"
	case dirty < 32:
		label = "16-31"
	case dirty < 64:
		label = "32-63"
	case dirty < 96:
		label = "64-95"
	}
	c.Buckets[label]++
}

// This opt-in diagnostic observes the exact state that syncSorted will consume
// before a fresh insertion splits a full leaf. It is untimed and adds no hook
// to production code.
func TestStringSplitDirtyRunCensus(t *testing.T) {
	path := os.Getenv("WORMHOLE_SORT_CENSUS_CORPUS")
	if path == "" {
		t.Skip("diagnostic corpus required")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	words, err := benchworkload.ReadWords(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	keys, fingerprint, err := benchworkload.Corpus(words, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	m := NewWithOptions[string, struct{}](Options{MetaCache: true})
	r := m.Ref()
	defer r.Close()
	stats := dirtyRunCensus{Buckets: make(map[string]uint64), Unique: make(map[string]uint64)}
	for _, key := range keys {
		tag := keyTag(key)
		l := m.current.Load().table.jump(key)
		if l.n == leafCapacity && l.matchString(key, tag) == leafCapacity {
			stats.add(l.n-l.sorted, l.next == nil)
			stats.addRadix(l)
		}
		if err := r.PutKey(PrepareKey(key), struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.Len(); got != int64(len(keys)) {
		t.Fatalf("length=%d want=%d", got, len(keys))
	}
	data, err := json.Marshal(map[string]any{
		"corpus_keys": len(keys), "sha256": fingerprint,
		"average_dirty":                float64(stats.DirtyEntries) / float64(stats.Splits),
		"average_unique_radix_buckets": float64(stats.UniqueTotal) / float64(stats.RadixRuns),
		"average_radix_bucket_span":    float64(stats.SpanTotal) / float64(stats.RadixRuns),
		"stats":                        stats,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("string-split-dirty-run-census: %s", data)
}

func numericCensusKey(rank int) uint64 {
	const mask = uint64(1<<63 - 1)
	x := uint64(rank) & mask
	x ^= x >> 30
	x = x * 0x3f58476d1ce4e5b9 & mask
	x ^= x >> 27
	x = x * 0x14d049bb133111eb & mask
	x ^= x >> 31
	return x << 1
}

// This opt-in diagnostic reproduces make bench's initial and fresh uint64
// insertion streams and observes the radix buckets consumed at every split.
func TestNumericSplitDirtyRunCensus(t *testing.T) {
	if os.Getenv("WORMHOLE_NUMERIC_SORT_CENSUS") == "" {
		t.Skip("numeric sort census not requested")
	}
	const n = 65536
	m := New[uint64, uint64]()
	r := m.Ref()
	defer r.Close()
	stats := [2]dirtyRunCensus{
		{Buckets: make(map[string]uint64), Unique: make(map[string]uint64)},
		{Buckets: make(map[string]uint64), Unique: make(map[string]uint64)},
	}
	for phase := range 2 {
		for i := range n {
			key := numericCensusKey(i) | uint64(phase)
			route := numericRoutingKey(key, &r.routing, m.keyKind)
			l := m.current.Load().table.jumpFixed(route)
			tag := numericKeyTag(&r.routing)
			if l.n == leafCapacity && l.matchNumeric(key, tag) == leafCapacity {
				stats[phase].add(l.n-l.sorted, l.next == nil)
				stats[phase].addNumericRadix(l)
			}
			if err := r.Put(key, uint64(i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for phase, label := range []string{"initial", "fresh"} {
		s := &stats[phase]
		data, err := json.Marshal(map[string]any{
			"phase": label, "keys": n,
			"average_dirty":                float64(s.DirtyEntries) / float64(s.Splits),
			"average_unique_radix_buckets": float64(s.UniqueTotal) / float64(s.RadixRuns),
			"average_radix_bucket_span":    float64(s.SpanTotal) / float64(s.RadixRuns),
			"stats":                        s,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("numeric-split-dirty-run-census: %s", data)
	}
}
