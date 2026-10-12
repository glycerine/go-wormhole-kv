package wormhole

import (
	"cmp"
	"encoding/binary"
	"math"
	"reflect"
	"unsafe"
)

// orderedCompare gives floating point keys a total order: all NaNs are equal
// and follow +Inf. The two representations of zero identify the same key.
func orderedCompare[K cmp.Ordered](a, b K) int {
	if a != a {
		if b != b {
			return 0
		}
		return 1
	}
	if b != b {
		return -1
	}
	return cmp.Compare(a, b)
}

// routingKey encodes numeric keys in order-preserving byte order. Its result
// borrows buf; only metadata anchors and prepared keys retain owned copies.
// The kind switch also supports user-defined types with ordered underlying types.
func routingKey[K cmp.Ordered](key K, buf *[8]byte) string {
	return routingKeyKind(key, buf, reflect.TypeFor[K]().Kind())
}

func routingKeyKind[K cmp.Ordered](key K, buf *[8]byte, kind reflect.Kind) string {
	if kind == reflect.String {
		return *(*string)(unsafe.Pointer(&key))
	}
	binary.BigEndian.PutUint64(buf[:], numericOrderBits(key, kind))
	return unsafe.String(&buf[0], 8)
}

func numericOrderBits[K cmp.Ordered](key K, kind reflect.Kind) uint64 {
	var u uint64
	p := unsafe.Pointer(&key)
	switch kind {
	case reflect.Int:
		u = uint64(*(*int)(p)) ^ (1 << 63)
	case reflect.Int8:
		u = uint64(*(*int8)(p)) ^ (1 << 63)
	case reflect.Int16:
		u = uint64(*(*int16)(p)) ^ (1 << 63)
	case reflect.Int32:
		u = uint64(*(*int32)(p)) ^ (1 << 63)
	case reflect.Int64:
		u = uint64(*(*int64)(p)) ^ (1 << 63)
	case reflect.Uint:
		u = uint64(*(*uint)(p))
	case reflect.Uint8:
		u = uint64(*(*uint8)(p))
	case reflect.Uint16:
		u = uint64(*(*uint16)(p))
	case reflect.Uint32:
		u = uint64(*(*uint32)(p))
	case reflect.Uint64:
		u = *(*uint64)(p)
	case reflect.Uintptr:
		u = uint64(*(*uintptr)(p))
	case reflect.Float32:
		u = floatOrder(float64(*(*float32)(p)))
	case reflect.Float64:
		u = floatOrder(*(*float64)(p))
	default:
		panic("wormhole: unsupported ordered key type")
	}
	return u
}

func floatOrder(f float64) uint64 {
	if math.IsNaN(f) {
		return math.MaxUint64
	}
	if f == 0 {
		f = 0
	}
	u := math.Float64bits(f)
	if u>>63 != 0 {
		return ^u
	}
	return u ^ (1 << 63)
}
