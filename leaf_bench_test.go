package wormhole

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

var benchmarkRecord *record[string, string]

func BenchmarkLeafProbe(b *testing.B) {
	for _, n := range []int{32, 96, 128} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			var l leaf[string, string]
			var keys [1024]string
			var tags [1024]uint16
			for i := range keys {
				keys[i] = fmt.Sprintf("key-%08d", i%n)
				tags[i] = keyTag(keys[i])
			}
			for i := 0; i < n; i++ {
				l.insert(&record[string, string]{keys[i], "value"}, tags[i])
			}
			b.ResetTimer()
			b.ReportMetric(float64(unsafe.Sizeof(leaf[string, string]{})), "bytes/leaf")
			for i := 0; i < b.N; i++ {
				j := i & 1023
				benchmarkRecord = l.hs[l.match(keys[j], tags[j])]
			}
		})
	}
}

func BenchmarkLeafChurn(b *testing.B) {
	var l leaf[string, string]
	var entries [128]*record[string, string]
	var tags [128]uint16
	for i := range entries {
		entries[i] = &record[string, string]{fmt.Sprintf("key-%08d", i), "value"}
		tags[i] = keyTag(entries[i].key)
		if i < 96 {
			l.insert(entries[i], tags[i])
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	var indices [1024]int
	for i := range indices {
		indices[i] = rng.IntN(len(entries))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := indices[i&1023]
		if slot := l.match(entries[j].key, tags[j]); slot < leafCapacity {
			l.remove(slot)
		} else {
			l.insert(entries[j], tags[j])
		}
	}
}

func BenchmarkLeafSort(b *testing.B) {
	var template leaf[string, string]
	rng := rand.New(rand.NewPCG(1, 2))
	for _, i := range rng.Perm(leafCapacity) {
		kv := &record[string, string]{fmt.Sprintf("key-%08d", i), "value"}
		template.insert(kv, keyTag(kv.key))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Copy only leaf contents, never a synchronization primitive.
		l := leaf[string, string]{tags: template.tags, hs: template.hs, ss: template.ss, n: template.n, sorted: template.sorted, occupied: template.occupied}
		l.syncSorted()
		benchmarkRecord = l.at(0)
	}
}

func BenchmarkLeafSortTail(b *testing.B) {
	for _, prefix := range []int{0, 32, 64, 96, 127} {
		b.Run(fmt.Sprint(prefix), func(b *testing.B) {
			var template leaf[string, string]
			rng := rand.New(rand.NewPCG(5, 7))
			for _, i := range rng.Perm(leafCapacity) {
				kv := &record[string, string]{fmt.Sprintf("key-%08d", i), "value"}
				template.insert(kv, keyTag(kv.key))
			}
			slices.SortFunc(template.ss[:prefix], func(a, c uint8) int {
				if template.hs[a].key < template.hs[c].key {
					return -1
				}
				if template.hs[a].key > template.hs[c].key {
					return 1
				}
				return 0
			})
			template.sorted = prefix
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				l := leaf[string, string]{tags: template.tags, hs: template.hs, ss: template.ss, n: template.n, sorted: template.sorted, occupied: template.occupied}
				l.syncSorted()
				benchmarkRecord = l.at(0)
			}
		})
	}
}

func BenchmarkLeafSortBinary(b *testing.B) {
	for _, prefix := range []int{0, 32, 64, 96, 127} {
		b.Run(fmt.Sprint(prefix), func(b *testing.B) {
			var template leaf[string, string]
			rng := rand.New(rand.NewPCG(5, 7))
			for _, i := range rng.Perm(leafCapacity) {
				var key [8]byte
				// Spread sorted ranks across all eight bytes of the key.
				binary.BigEndian.PutUint64(key[:], uint64(i)*0x0202020202020202)
				kv := &record[string, string]{string(key[:]), "value"}
				template.insert(kv, keyTag(kv.key))
			}
			slices.SortFunc(template.ss[:prefix], func(a, c uint8) int {
				return strings.Compare(template.hs[a].key, template.hs[c].key)
			})
			template.sorted = prefix
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				l := leaf[string, string]{tags: template.tags, hs: template.hs, ss: template.ss, n: template.n, sorted: template.sorted, occupied: template.occupied}
				l.syncSorted()
				benchmarkRecord = l.at(0)
			}
		})
	}
}
