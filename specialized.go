package wormhole

import (
	"cmp"
	"encoding/binary"
	"reflect"
	"unsafe"
)

//go:generate python3 internal/genspecialize/generate.py

// On 64-bit targets strings are the only Ordered keys wider than eight bytes.
// The compiler knows the size of each Go shape and can fold this selection.
// On 32-bit targets strings overlap 64-bit numbers in size, so use the kind.
func isStringKey[K cmp.Ordered]() bool {
	var key K
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return unsafe.Sizeof(key) > 8
	}
	return reflect.TypeFor[K]().Kind() == reflect.String
}

// stringKey is used only after the string key family has been selected.
// Named string types have the same representation; the view borrows the key.
func stringKey[K cmp.Ordered](key K) string { return *(*string)(unsafe.Pointer(&key)) }

func numericRoutingKey[K cmp.Ordered](key K, buf *[8]byte, kind reflect.Kind) string {
	binary.BigEndian.PutUint64(buf[:], numericOrderBits(key, kind))
	return unsafe.String(&buf[0], 8)
}

func numericKeysEqual[K cmp.Ordered](a, b K) bool { return a == b || (a != a && b != b) }

// rememberNumericPutLeaf avoids publishing a GC-visible leaf pointer for a
// one-off random prefix. A repeated prefix arms the existing write finger;
// a single-leaf map remains cached immediately.
func (r *Ref[K, V]) rememberNumericPutLeaf(l *leaf[K, V], prefix uint32) {
	whole := l.anchor == "" && l.next == nil
	r.putWhole = whole
	if whole || r.putCandidateSeen && r.putCandidate == prefix {
		r.putLeaf, r.putVersion, r.putPrefix = l, l.version, prefix
		r.putPrefixChecked, r.putPrefixCovered = whole, whole
		r.putCandidateSeen = false
		return
	}
	r.putPrefixChecked, r.putPrefixCovered = false, false
	r.putCandidate, r.putCandidateSeen = prefix, true
}
