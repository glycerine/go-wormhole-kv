package wormhole

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// Equal text can come from a different allocation. Put must retain the new
// header so a smaller replacement can release a larger backing allocation,
// including for zero-length substrings. A no-op may skip only an exact header.
func TestReplacementEqualTextRetainsNewHeader(t *testing.T) {
	backing := strings.Repeat("payload-", 8192)
	small := strings.Clone(backing[:7])
	tiny := strings.Clone("tiny")
	cases := []struct{ name, old, next string }{
		{"equal bytes different allocation", backing[:7], small},
		{"empty substring to empty literal", backing[:0], ""},
		{"empty substrings different allocations", backing[:0], tiny[:0]},
		{"same allocation different length", backing[:4], backing[:8]},
		{"identical header", small, small},
	}
	data := func(s string) uintptr { return (*reflect.StringHeader)(unsafe.Pointer(&s)).Data }
	for _, prepared := range []bool{false, true} {
		api := "Put"
		if prepared {
			api = "PutKey"
		}
		t.Run(api, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					m := New[string, string]()
					r := m.Ref()
					defer r.Close()
					if err := r.Put("value-key", tc.old); err != nil {
						t.Fatal(err)
					}
					var err error
					if prepared {
						err = r.PutKey(PrepareKey("value-key"), tc.next)
					} else {
						err = r.Put("value-key", tc.next)
					}
					if err != nil {
						t.Fatal(err)
					}
					got, ok := r.Get("value-key")
					if !ok || got != tc.next || data(got) != data(tc.next) || m.Len() != 1 {
						t.Fatalf("value=%q present=%v data=%x want=%x length=%d", got, ok, data(got), data(tc.next), m.Len())
					}
				})
			}
		})
	}
}
