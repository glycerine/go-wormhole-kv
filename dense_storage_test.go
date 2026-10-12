package wormhole

import (
	"cmp"
	"testing"
	"unsafe"
)

func checkDenseStorageLayout[K cmp.Ordered, V any](t *testing.T) {
	t.Helper()
	var d denseStorage[K, V]
	want := unsafe.Sizeof(d.records)
	if !useLeafSIMD {
		want += 256 + leafCapacity
	}
	if unsafe.Offsetof(d.records) != 0 || unsafe.Sizeof(d) != want {
		t.Fatalf("records offset=%d storage=%d want=%d", unsafe.Offsetof(d.records), unsafe.Sizeof(d), want)
	}
}

func TestDenseStorageLayout(t *testing.T) {
	checkDenseStorageLayout[uint64, uint64](t)
	checkDenseStorageLayout[string, uint64](t)
	checkDenseStorageLayout[string, string](t)
	checkDenseStorageLayout[uint8, struct{}](t)
	checkDenseStorageLayout[float64, [64]byte](t)
}
