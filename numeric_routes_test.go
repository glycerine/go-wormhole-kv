package wormhole

import (
	"testing"
	"unsafe"
)

func TestNumericRouteFillPartialPageRanges(t *testing.T) {
	for start := 0; start < 256; start++ {
		for end := start; end <= 256; end++ {
			old, replacement := new(leaf[uint64, uint64]), new(leaf[uint64, uint64])
			d := newNumericRoutes(unsafe.Pointer(old))
			d.fill(start, end, unsafe.Pointer(replacement))
			for i := 0; i < 256; i++ {
				want := old
				if i >= start && i < end {
					want = replacement
				}
				key := string([]byte{0, byte(i)})
				if d.lookup(key) != unsafe.Pointer(want) {
					t.Fatalf("range [%d,%d), cell %d", start, end, i)
				}
			}
		}
	}
}

func TestNumericRouteFillAcrossPages(t *testing.T) {
	boundaries := []int{0, 1, 7, 8, 255, 256, 257, 511, 512, 65535, 65536}
	for _, start := range boundaries {
		for _, end := range boundaries {
			if end < start {
				continue
			}
			old, replacement := new(leaf[uint64, uint64]), new(leaf[uint64, uint64])
			d := newNumericRoutes(unsafe.Pointer(old))
			// Reify pages before overwriting whole-page uniform intervals.
			d.page(0)
			d.page(255)
			d.fill(start, end, unsafe.Pointer(replacement))
			for i := 0; i < 65536; i++ {
				want := old
				if i >= start && i < end {
					want = replacement
				}
				key := string([]byte{byte(i >> 8), byte(i)})
				if d.lookup(key) != unsafe.Pointer(want) {
					t.Fatalf("range [%d,%d), cell %d", start, end, i)
				}
			}
		}
	}
}

func TestNumericRouteLocalIDReuse(t *testing.T) {
	first, removed, replacement := new(leaf[uint64, uint64]), new(leaf[uint64, uint64]), new(leaf[uint64, uint64])
	d := newNumericRoutes(unsafe.Pointer(first))
	d.fill(17, 41, unsafe.Pointer(removed))
	page := d.pages[0]
	removedID := page.cells[17]
	d.fill(17, 41, unsafe.Pointer(first))
	if page.leaves[removedID-1] != nil || page.refs[removedID-1] != 0 {
		t.Fatal("unused local ID remains reachable")
	}
	d.fill(17, 41, unsafe.Pointer(replacement))
	if got := page.cells[17]; got != removedID {
		t.Fatalf("local ID reuse=%d want %d", got, removedID)
	}
	for i := 17; i < 41; i++ {
		if got := d.lookup(string([]byte{0, byte(i)})); got != unsafe.Pointer(replacement) {
			t.Fatalf("reused handle cell %d points to %p", i, got)
		}
	}
}

func TestNumericRoutePagePromotesBeyondLocalCapacity(t *testing.T) {
	d := newNumericRoutes(unsafe.Pointer(new(leaf[uint64, uint64])))
	for i := 0; i < numericRouteLocalLeaves+1; i++ {
		d.fill(i, i+1, unsafe.Pointer(new(leaf[uint64, uint64])))
	}
	if d.pages[0].direct == nil {
		t.Fatal("page did not promote after exhausting local IDs")
	}
	for i := 0; i < numericRouteLocalLeaves+1; i++ {
		if got := d.lookup(string([]byte{0, byte(i)})); got == nil {
			t.Fatalf("promoted cell %d is uncached", i)
		}
	}
}

func TestNumericRoutesPreserveLeafLayout(t *testing.T) {
	var l leaf[uint64, uint64]
	if got := unsafe.Sizeof(l); got != 512 {
		t.Fatalf("leaf size=%d want 512", got)
	}
	if got := unsafe.Offsetof(l.sortMu); got != 24 {
		t.Fatalf("sort mutex offset=%d want 24", got)
	}
}
