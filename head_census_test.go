package wormhole

import (
	"os"
	"runtime"
	"testing"
	"unsafe"

	"github.com/glycerine/go-wormhole-kv/internal/benchworkload"
)

// Diagnostic only: enumerate actual tag collisions, outside timed benchmarks.
func TestHeadCensus(t *testing.T) {
	path := os.Getenv("WORMHOLE_HEAD_CORPUS")
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
	keys, sha, err := benchworkload.Corpus(words, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	m := NewWithOptions[string, string](Options{MetaCache: true})
	r := m.Ref()
	defer r.Close()
	for _, key := range keys {
		if err := r.PutKey(PrepareKey(key), ""); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	t.Logf("retained_heap_delta=%d", after.HeapAlloc-before.HeapAlloc)
	runtime.KeepAlive(keys)
	var leaves, entries, collisions, headReject4, headReject8, adjacent, ties4, ties8 int
	for l := m.first; l != nil; l = l.next {
		leaves++
		entries += l.n
		l.syncSorted()
		for a := 0; a < l.n; a++ {
			i := l.ss[a]
			ki := l.records[i].key
			if a > 0 {
				adjacent++
				kp := l.records[l.ss[a-1]].key
				if keyHead(ki)>>32 == keyHead(kp)>>32 {
					ties4++
				}
				if keyHead(ki) == keyHead(kp) {
					ties8++
				}
			}
			for b := a + 1; b < l.n; b++ {
				j := l.ss[b]
				if l.tags[i] != l.tags[j] {
					continue
				}
				collisions++
				kj := l.records[j].key
				if keyHead(ki)>>32 != keyHead(kj)>>32 {
					headReject4++
				}
				if keyHead(ki) != keyHead(kj) {
					headReject8++
				}
			}
		}
	}
	t.Logf("sha256=%s entries=%d leaves=%d leaf_bytes=%d", sha, entries, leaves, unsafe.Sizeof(leaf[string, string]{}))
	t.Logf("unordered equal-tag distinct pairs=%d; rejectable by head4=%d head8=%d; false candidates per exhaustive hit sweep=%d", collisions, headReject4, headReject8, collisions*2)
	t.Logf("adjacent sorted pairs=%d head4_ties=%d head8_ties=%d; requested extra arrays head4=%d head8=%d", adjacent, ties4, ties8, leaves*512, leaves*1024)
	runtime.KeepAlive(m)
}
