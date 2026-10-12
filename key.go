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
	text       string
	encodedTag uint16
	prefix     uint32
	ready      bool
}

func PrepareKey[K cmp.Ordered](key K) Key[K] {
	var text string
	kind := reflect.TypeFor[K]().Kind()
	if kind == reflect.String {
		text = *(*string)(unsafe.Pointer(&key))
	} else {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], numericOrderBits(key, kind))
		text = string(buf[:])
	}
	return Key[K]{key: key, text: text, encodedTag: keyTag(text), prefix: keyPrefix(text), ready: true}
}

// String returns the routing bytes (the original text for string keys).
func (k Key[K]) String() string { return k.text }
func (k Key[K]) tag() uint16 {
	if !k.ready {
		return PrepareKey(k.key).encodedTag
	}
	return k.encodedTag
}
func (r *Ref[K, V]) ProbeKey(key Key[K]) bool {
	r.checkAvailable()
	if !key.ready {
		key = PrepareKey(key.key)
	}
	return r.probe(key.key, key.text, key.encodedTag)
}
func (r *Ref[K, V]) PutKey(key Key[K], value V) error {
	r.checkAvailable()
	if !key.ready {
		key = PrepareKey(key.key)
	}
	if key.prefix == r.putPrefix {
		prefetchWriteKey(unsafe.StringData(key.text))
	}
	return r.putWithPrefix(key.key, value, key.text, key.encodedTag, key.prefix)
}
func keyPrefix(text string) uint32 {
	if len(text) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32([]byte(text[:4]))
}
