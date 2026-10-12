package wormhole

import (
	"fmt"
	"reflect"
	"testing"
)

// Compare bulk rebuild with the existing insertion path at every occupancy,
// starting with a full leaf so inactive pointer clearing is observable.
func TestDenseCompactRebuild(t *testing.T) {
	for n := 0; n <= leafCapacity; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			l := &leaf[string, *int]{dense: true, denseStorage: new(denseStorage[string, *int])}
			want := &leaf[string, *int]{dense: true, denseStorage: new(denseStorage[string, *int])}
			for i := 0; i < leafCapacity; i++ {
				v := i
				l.insertDenseEntry(fmt.Sprintf("stale%03d", i), &v, 99)
			}
			values := make([]record[string, *int], n)
			tags := make([]uint16, n)
			for i := range values {
				v := i
				values[i] = record[string, *int]{key: fmt.Sprintf("key%03d", i), value: &v}
				// Exercise complete collisions and shared bucket bytes.
				tags[i] = uint16(42 + (i%3)*256)
				want.insertDenseEntry(values[i].key, values[i].value, tags[i])
			}
			want.sorted = n
			l.buildDenseCompact(values, tags)
			if !reflect.DeepEqual(l.denseStorage, want.denseStorage) || l.tags != want.tags || l.ss != want.ss || l.occupied != want.occupied || l.n != want.n || l.sorted != want.sorted {
				t.Fatal("bulk rebuild differs from ordinary insertion")
			}
			for i, v := range values {
				slot := l.match(v.key, tags[i])
				if slot == leafCapacity || l.records[slot].value != v.value {
					t.Fatalf("lookup %d lost key or pointer identity", i)
				}
			}
			if l.match("missing", 42) != leafCapacity {
				t.Fatal("false match")
			}
		})
	}
}
