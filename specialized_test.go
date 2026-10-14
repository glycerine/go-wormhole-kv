package wormhole

import (
	"cmp"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

func checkStringKeyShape[K cmp.Ordered](t *testing.T) {
	t.Helper()
	if got, want := isStringKey[K](), reflect.TypeFor[K]().Kind() == reflect.String; got != want {
		t.Fatalf("key type %v: string family=%v, want %v", reflect.TypeFor[K](), got, want)
	}
}

func TestSpecializedStringKeyShape(t *testing.T) {
	checkStringKeyShape[string](t)
	checkStringKeyShape[namedString](t)
	checkStringKeyShape[int](t)
	checkStringKeyShape[int8](t)
	checkStringKeyShape[int16](t)
	checkStringKeyShape[int32](t)
	checkStringKeyShape[int64](t)
	checkStringKeyShape[namedInt](t)
	checkStringKeyShape[uint](t)
	checkStringKeyShape[uint8](t)
	checkStringKeyShape[uint16](t)
	checkStringKeyShape[uint32](t)
	checkStringKeyShape[uint64](t)
	checkStringKeyShape[namedUint](t)
	checkStringKeyShape[uintptr](t)
	checkStringKeyShape[float32](t)
	checkStringKeyShape[float64](t)
	checkStringKeyShape[namedFloat](t)
}

// Colliding tags exercise full-key matching, including holes and named types,
// against the existing matcher used by structural/callback operations.
func checkSpecializedMatcher[K cmp.Ordered](t *testing.T, keys []K) {
	t.Helper()
	for _, dense := range []bool{false, true} {
		l := &leaf[K, int]{dense: dense}
		for i, key := range keys[:len(keys)-1] {
			l.insert(&record[K, int]{key: key, value: i}, 42)
		}
		check := func() {
			for _, key := range keys {
				want := l.match(key, 42)
				var got int
				if reflect.TypeFor[K]().Kind() == reflect.String {
					got = l.matchString(stringKey(key), 42)
				} else {
					got = l.matchNumeric(key, 42)
				}
				if got != want {
					t.Fatalf("dense=%v key=%v got slot %d, want %d", dense, key, got, want)
				}
			}
		}
		check()
		for _, key := range keys[: len(keys)-1 : len(keys)-1] {
			if slot := l.match(key, 42); slot != leafCapacity && slot%3 == 0 {
				l.remove(slot)
			}
		}
		check()
	}
}

func TestSpecializedMatchers(t *testing.T) {
	var text []namedString
	var ints []namedUint
	var floats []namedFloat
	for i := range 128 {
		text = append(text, namedString(strings.Repeat("x", i)+"\x00\xff"))
		ints = append(ints, namedUint(i*7919))
		floats = append(floats, namedFloat(i))
	}
	floats[0], floats[1], floats[2] = namedFloat(math.NaN()), namedFloat(math.Float64frombits(0xfff8000000001234)), namedFloat(math.Copysign(0, -1))
	t.Run("named strings", func(t *testing.T) { checkSpecializedMatcher(t, text) })
	t.Run("named integers", func(t *testing.T) { checkSpecializedMatcher(t, ints) })
	t.Run("named floats", func(t *testing.T) { checkSpecializedMatcher(t, floats) })
}

func checkOrderedEqual[K cmp.Ordered](t *testing.T, values []K) {
	t.Helper()
	for _, a := range values {
		for _, b := range values {
			if got, want := numericKeysEqual(a, b), orderedCompare(a, b) == 0; got != want {
				t.Fatalf("equality(%v, %v)=%v, ordering equality=%v", a, b, got, want)
			}
		}
	}
}

func TestSpecializedEqualityAgreesWithOrdering(t *testing.T) {
	rng := rand.New(rand.NewPCG(71, 19))
	f64 := []float64{0, math.Copysign(0, -1), -1, 1, math.Inf(-1), math.Inf(1), math.SmallestNonzeroFloat64, math.MaxFloat64, math.NaN(), math.Float64frombits(0xfff8000000001234), math.Float64frombits(0x7ff0000000000001)}
	f32 := []float32{0, float32(math.Copysign(0, -1)), -1, 1, float32(math.Inf(-1)), float32(math.Inf(1)), math.SmallestNonzeroFloat32, math.MaxFloat32, float32(math.NaN()), math.Float32frombits(0xffc01234), math.Float32frombits(0x7f800001)}
	i64 := []int64{0, -1, 1, math.MinInt64, math.MaxInt64}
	u64 := []uint64{0, 1, math.MaxUint64}
	for range 128 {
		f64 = append(f64, math.Float64frombits(rng.Uint64()))
		f32 = append(f32, math.Float32frombits(rng.Uint32()))
		i64 = append(i64, int64(rng.Uint64()))
		u64 = append(u64, rng.Uint64())
	}
	t.Run("float64", func(t *testing.T) { checkOrderedEqual(t, f64) })
	t.Run("float32", func(t *testing.T) { checkOrderedEqual(t, f32) })
	t.Run("int64", func(t *testing.T) { checkOrderedEqual(t, i64) })
	t.Run("uint64", func(t *testing.T) { checkOrderedEqual(t, u64) })
	t.Run("uintptr", func(t *testing.T) { checkOrderedEqual(t, []uintptr{0, 1, ^uintptr(0)}) })
	t.Run("named float", func(t *testing.T) {
		var values []namedFloat
		for _, v := range f64 {
			values = append(values, namedFloat(v))
		}
		checkOrderedEqual(t, values)
	})
	text := strings.Repeat("shared prefix", 20)
	keys := []string{"", "\x00", "a", "a\x00", "aa", "\xff", text, strings.Clone(text), text + "suffix"}
	t.Run("string", func(t *testing.T) { checkOrderedEqual(t, keys) })
	t.Run("named string", func(t *testing.T) {
		var values []namedString
		for _, v := range keys {
			values = append(values, namedString(v))
		}
		checkOrderedEqual(t, values)
	})
}
