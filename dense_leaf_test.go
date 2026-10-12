package wormhole

import (
	"math/bits"
	"math/rand/v2"
	"sync"
	"testing"
)

func checkDenseLeaf(t *testing.T, l *leaf[uint64, []byte], want map[uint64]uint16) {
	t.Helper()
	if l.n != len(want) || bits.OnesCount64(l.occupied[0])+bits.OnesCount64(l.occupied[1]) != l.n {
		t.Fatal("dense count")
	}
	var seen [leafCapacity]bool
	count := 0
	for slot, tag := range l.tags {
		occupied := l.occupied[slot>>6]&(uint64(1)<<(slot&63)) != 0
		if occupied != (tag != 0) {
			t.Fatal("tag/occupancy mismatch", slot)
		}
		if tag == 0 {
			continue
		}
		seen[slot] = true
		count++
		kv := l.recordAtSlot(slot)
		wantTag, ok := want[kv.key]
		if !ok || wantTag != tag || l.match(kv.key, tag) != slot {
			t.Fatal("wrong dense key", slot)
		}
	}
	if count != l.n || l.hs != nil {
		t.Fatal("stable-slot coverage")
	}
	if !useLeafSIMD {
		var linked [leafCapacity]bool
		count := 0
		for bucket, first := range l.buckets {
			for link := first; link != 0; link = l.chain[link-1] {
				slot := int(link) - 1
				if slot >= leafCapacity || linked[slot] || !seen[slot] || byte(l.tags[slot]) != byte(bucket) {
					t.Fatal("invalid or cyclic bucket chain", bucket, slot)
				}
				linked[slot] = true
				count++
			}
		}
		if count != l.n {
			t.Fatal("bucket coverage")
		}
	}
	var ordered [leafCapacity]bool
	for _, slot := range l.ss[:l.n] {
		if !seen[slot] || ordered[slot] {
			t.Fatal("sorted-index coverage")
		}
		ordered[slot] = true
	}
	for i := range seen {
		if !seen[i] && (l.hs != nil || l.denseStorage != nil && l.records[i].value != nil) {
			t.Fatal("vacant slot retains a value", i)
		}
	}
}

func TestDenseLeafCollisionAndReuse(t *testing.T) {
	l := &leaf[uint64, []byte]{dense: true}
	want := make(map[uint64]uint16)
	for i := 0; i < leafCapacity; i++ {
		key := uint64(i)
		tag := uint16(i*256 + 7)
		l.insertDenseEntry(key, []byte{byte(i)}, tag)
		want[key] = tag
		checkDenseLeaf(t, l, want)
	}
	for i := 0; i < leafCapacity; i += 2 {
		key := uint64(i)
		slot := l.match(key, want[key])
		old := l.remove(slot)
		if old.key != key {
			t.Fatal("remove snapshot")
		}
		delete(want, key)
		checkDenseLeaf(t, l, want)
	}
	for i := 0; i < leafCapacity; i += 2 {
		key := uint64(i + leafCapacity)
		tag := uint16(i*256 + 7)
		l.insertDenseEntry(key, []byte{byte(i)}, tag)
		want[key] = tag
		checkDenseLeaf(t, l, want)
	}
	l.syncSorted()
	for i := 1; i < l.n; i++ {
		if l.at(i-1).key >= l.at(i).key {
			t.Fatal("dense sort")
		}
	}
}

func TestDenseMapRandomMutation(t *testing.T) {
	for _, metaCache := range []bool{false, true} {
		name := "no-cache"
		if metaCache {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) { testDenseMapRandomMutation(t, Options{MetaCache: metaCache}) })
	}
}

func testDenseMapRandomMutation(t *testing.T, options Options) {
	m := NewWithOptions[uint64, uint64](options)
	r := m.Ref()
	defer r.Close()
	want := make(map[uint64]uint64)
	rng := rand.New(rand.NewPCG(51, 97))
	for step := 0; step < 20000; step++ {
		key := rng.Uint64N(2000)
		switch rng.IntN(4) {
		case 0:
			_, exists := want[key]
			if r.Delete(key) != exists {
				t.Fatal("delete membership")
			}
			delete(want, key)
		case 1:
			end := key + rng.Uint64N(100)
			var n int64
			for k := range want {
				if k >= key && k < end {
					delete(want, k)
					n++
				}
			}
			if r.DeleteRange(key, end) != n {
				t.Fatal("range count")
			}
		default:
			value := rng.Uint64()
			if err := r.Put(key, value); err != nil {
				t.Fatal(err)
			}
			want[key] = value
		}
		if step%100 == 0 {
			if m.Len() != int64(len(want)) {
				t.Fatal("length")
			}
			for k, v := range want {
				if got, ok := r.Get(k); !ok || got != v {
					t.Fatal("lost value", step, k)
				}
			}
		}
	}
}

func TestNumericRoutingMatchesGeneralTrie(t *testing.T) {
	for _, metaCache := range []bool{false, true} {
		name := "no-cache"
		if metaCache {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) { testNumericRoutingMatchesGeneralTrie(t, Options{MetaCache: metaCache}) })
	}
}

func testNumericRoutingMatchesGeneralTrie(t *testing.T, options Options) {
	m := NewWithOptions[uint64, uint64](options)
	r := m.Ref()
	defer r.Close()
	rng := rand.New(rand.NewPCG(13, 17))
	var keys []uint64
	check := func() {
		table := m.current.Load().table
		for i := 0; i < 300; i++ {
			key := rng.Uint64()
			if i < 10 {
				key = uint64(i)
			}
			var buf [8]byte
			route := routingKey(key, &buf)
			if table.jumpFixed(route) != table.jump(route) {
				t.Fatal("numeric/general routing disagreement", key)
			}
		}
	}
	check()
	for i := 0; i < 3000; i++ {
		key := rng.Uint64()
		if i%2 == 0 {
			key = uint64(i)
		}
		keys = append(keys, key)
		if err := r.Put(key, key); err != nil {
			t.Fatal(err)
		}
		if i%100 == 0 {
			check()
		}
	}
	for i, key := range keys {
		if !r.Delete(key) {
			t.Fatal("delete")
		}
		if i%100 == 0 {
			check()
		}
	}
	check()
}

func TestNumericSeparatorMatchesStringPolicy(t *testing.T) {
	for _, sequential := range []bool{false, true} {
		for _, rightmost := range []bool{false, true} {
			dense := &leaf[uint64, uint64]{dense: true}
			original := &leaf[string, uint64]{}
			if !rightmost {
				dense.next = &leaf[uint64, uint64]{}
				original.next = &leaf[string, uint64]{}
			}
			rng := rand.New(rand.NewPCG(71, 103))
			for i := 0; i < leafCapacity; i++ {
				key := uint64(i * 2)
				if !sequential {
					key = rng.Uint64() &^ 1
				}
				var buf [8]byte
				route := string([]byte(routingKey(key, &buf)))
				tag := keyTag(route)
				dense.insertDenseEntry(key, key, tag)
				original.insert(&record[string, uint64]{key: route, value: key}, tag)
			}
			key := uint64(1)
			var buf [8]byte
			route := string([]byte(routingKey(key, &buf)))
			tag := keyTag(route)
			right := dense.split(&record[uint64, uint64]{key: key, value: key}, tag)
			want := original.split(&record[string, uint64]{key: route, value: key}, tag)
			if dense.n != original.n || right.n != want.n || right.anchor != want.anchor {
				t.Fatal("numeric separator policy changed", sequential, rightmost)
			}
			for i := 0; i < dense.n; i++ {
				if dense.at(i).value != original.at(i).value {
					t.Fatal("left split values")
				}
			}
			for i := 0; i < right.n; i++ {
				if right.at(i).value != want.at(i).value {
					t.Fatal("right split values")
				}
			}
		}
	}
}

func TestNumericConcurrentRoutePublication(t *testing.T) {
	for _, metaCache := range []bool{false, true} {
		name := "no-cache"
		if metaCache {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) { testNumericConcurrentRoutePublication(t, Options{MetaCache: metaCache}) })
	}
}

func testNumericConcurrentRoutePublication(t *testing.T, options Options) {
	m := NewWithOptions[uint64, uint64](options)
	const workers, each = 4, 1000
	keyFor := func(i int) uint64 { return uint64(i) * 0x9e3779b97f4a7c15 }
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Go(func() {
			r := m.Ref()
			defer r.Close()
			for i := 0; i < each; i++ {
				key := keyFor(i*workers + worker)
				if err := r.Put(key, key^0xdeadbeef); err != nil {
					t.Error(err)
					return
				}
				if value, ok := r.Get(key); !ok || value != key^0xdeadbeef {
					t.Error("concurrent directory lookup")
					return
				}
				if i%3 == 0 && !r.Delete(key) {
					t.Error("concurrent directory delete")
					return
				}
			}
		})
	}
	wg.Wait()
	r := m.Ref()
	defer r.Close()
	var count int64
	for i := 0; i < each; i++ {
		for worker := 0; worker < workers; worker++ {
			key := keyFor(i*workers + worker)
			value, ok := r.Get(key)
			if ok != (i%3 != 0) || ok && value != key^0xdeadbeef {
				t.Fatal("published directory membership", i, worker)
			}
			if ok {
				count++
			}
		}
	}
	if m.Len() != count {
		t.Fatal("concurrent directory count")
	}
}
