package wormhole

import (
	"cmp"
	"encoding/binary"
	"reflect"
	"unsafe"
)

// Key retains a native key and its precomputed routing data. It may be shared
// across goroutines. Its zero value represents the zero value of K.
type Key[K cmp.Ordered] struct {
	key        K
	bits       uint64
	prefix     uint32
	tagAndKind uint32 // low 16 bits: nonzero tag; high 16 bits: reflect.Kind
}

func PrepareKey[K cmp.Ordered](key K) Key[K] {
	prepared := Key[K]{key: key}
	kind := reflect.TypeFor[K]().Kind()
	if kind == reflect.String {
		text := *(*string)(unsafe.Pointer(&key))
		prepared.tagAndKind = uint32(keyTag(text)) | uint32(kind)<<16
		prepared.prefix = keyPrefix(text)
		return prepared
	}
	prepared.bits = numericOrderBits(key, kind)
	var buf [8]byte
	text := prepared.route(&buf, kind)
	prepared.tagAndKind = uint32(keyTag(text)) | uint32(kind)<<16
	prepared.prefix = keyPrefix(text)
	return prepared
}

// route borrows the caller's scratch for numeric keys. String routing uses
// the native key's existing header instead of retaining a duplicate header.
func (k Key[K]) route(buf *[8]byte, kind reflect.Kind) string {
	if kind == reflect.String {
		return *(*string)(unsafe.Pointer(&k.key))
	}
	binary.BigEndian.PutUint64(buf[:], k.bits)
	return unsafe.String(&buf[0], 8)
}

// String returns the routing bytes (the original text for string keys).
func (k Key[K]) String() string {
	if k.tagAndKind>>16 == uint32(reflect.String) {
		return *(*string)(unsafe.Pointer(&k.key))
	}
	return numericKeyString(k.bits, k.tagAndKind != 0)
}

// Keep the allocating numeric conversion outside the string accessor's
// inlining budget. ProbeKey and PutKey use caller-owned scratch instead.
//
//go:noinline
func numericKeyString(bits uint64, prepared bool) string {
	if !prepared {
		return ""
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], bits)
	return string(buf[:])
}

func (k Key[K]) tag() uint16 {
	if k.tagAndKind == 0 {
		return uint16(PrepareKey(k.key).tagAndKind)
	}
	return uint16(k.tagAndKind)
}
func (r *Ref[K, V]) ProbeKey(key Key[K]) bool {
	r.checkAvailable()
	if key.tagAndKind == 0 {
		key = PrepareKey(key.key)
	}
	return r.probe(key.key, key.route(&r.routing, r.m.keyKind), uint16(key.tagAndKind))
}
func (r *Ref[K, V]) PutKey(key Key[K], value V) error {
	r.checkAvailable()
	if key.tagAndKind == 0 {
		key = PrepareKey(key.key)
	}
	text := key.route(&r.routing, r.m.keyKind)
	if key.prefix == r.putPrefix {
		prefetchWriteKey(unsafe.StringData(text))
	}
	return r.putWithPrefix(key.key, value, text, uint16(key.tagAndKind), key.prefix)
}
func keyPrefix(text string) uint32 {
	if len(text) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32([]byte(text[:4]))
}
