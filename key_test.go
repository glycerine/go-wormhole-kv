package wormhole

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"testing"
)

func TestPreparedKey(t *testing.T) {
	t.Run("given prepared binary keys when split loaded probed replaced and deleted then agree with string operations", func(t *testing.T) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		rng := rand.New(rand.NewPCG(155, 42))
		keys := make([]Key[string], 2000)
		for i := range keys {
			text := fmt.Sprintf("%08d%c%c", i, byte(rng.Uint32()), byte(rng.Uint32()))
			keys[i] = PrepareKey(text)
			if keys[i].String() != text || keys[i].tag() != keyTag(text) {
				t.Fatal("key changed")
			}
			if r.ProbeKey(keys[i]) {
				t.Fatal("present before insert")
			}
			if err := r.PutKey(keys[i], text); err != nil {
				t.Fatal(err)
			}
		}
		for _, k := range keys {
			if !r.ProbeKey(k) || !r.Probe(k.String()) {
				t.Fatal("missing", k)
			}
			if err := r.PutKey(k, "replacement"); err != nil {
				t.Fatal(err)
			}
			if v, ok := r.Get(k.String()); !ok || v != "replacement" {
				t.Fatal(v, ok)
			}
			if !r.Delete(k.String()) || r.ProbeKey(k) {
				t.Fatal("delete failed")
			}
		}
		var zero Key[string]
		if zero.String() != "" || zero.tag() != PrepareKey("").tag() {
			t.Fatal("zero Key is not empty key")
		}
		if err := r.PutKey(zero, "empty"); err != nil {
			t.Fatal(err)
		}
		if !r.Probe("") || !r.ProbeKey(zero) {
			t.Fatal("missing empty key")
		}
		checkMap(t, m, map[string]string{"": "empty"})
	})
}

// Check prepared insertion as well as probing: numeric routes borrow Ref scratch
// and must not escape into metadata when leaves split.
func checkPreparedWrites[K cmp.Ordered](t *testing.T, keys []K) {
	t.Helper()
	m := New[K, int]()
	r := m.Ref()
	defer r.Close()
	prepared := make([]Key[K], len(keys))
	for i, key := range keys {
		prepared[i] = PrepareKey(key)
		var buf [8]byte
		if got, want := prepared[i].String(), routingKey(key, &buf); got != want {
			t.Fatalf("routing %v: %x != %x", key, got, want)
		}
		if err := r.PutKey(prepared[i], i); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	for _, key := range prepared {
		if !r.ProbeKey(key) {
			t.Fatalf("missing prepared key %v", key)
		}
		if err := r.PutKey(key, -1); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range keys {
		if value, ok := r.Get(key); !ok || value != -1 {
			t.Fatalf("Get(%v) = %d, %v", key, value, ok)
		}
	}
	var zero Key[K]
	if err := r.PutKey(zero, -2); err != nil {
		t.Fatal(err)
	}
	if !r.ProbeKey(zero) {
		t.Fatal("missing zero prepared key")
	}
}

func TestPreparedWrites(t *testing.T) {
	t.Run("named unsigned split", func(t *testing.T) {
		keys := make([]namedUint, 2048)
		for i := range keys {
			keys[i] = namedUint(uint64(i) * 0x9e3779b97f4a7c15)
		}
		checkPreparedWrites(t, keys)
	})
	t.Run("signed boundaries", func(t *testing.T) { checkPreparedWrites(t, []int64{math.MinInt64, -1, 0, 1, math.MaxInt64}) })
	t.Run("named strings", func(t *testing.T) { checkPreparedWrites(t, []namedString{"", "a", "a\x00", "b", "\xff"}) })
	t.Run("float canonicalization", func(t *testing.T) {
		checkPreparedWrites(t, []namedFloat{namedFloat(math.Inf(-1)), -1, 0, namedFloat(math.Copysign(0, -1)), 1, namedFloat(math.Inf(1)), namedFloat(math.NaN()), namedFloat(math.Float64frombits(0x7ff8000000000042))})
	})
}

func TestPreparedStringBorrowsKey(t *testing.T) {
	var prepared Key[string]
	allocations := testing.AllocsPerRun(100, func() {
		prepared = PrepareKey("borrowed immutable string")
	})
	if allocations != 0 {
		t.Fatalf("string preparation allocated %g objects", allocations)
	}
	if prepared.String() != "borrowed immutable string" {
		t.Fatal("prepared key changed")
	}
}
