package wormhole

import (
	"fmt"
	"testing"
)

func TestPutHintStructuralChanges(t *testing.T) {
	fixture := func(t *testing.T) (*Map[string, string], *Ref[string, string], *Ref[string, string]) {
		m := New[string, string]()
		loader, writer := m.Ref(), m.Ref()
		t.Cleanup(loader.Close)
		t.Cleanup(writer.Close)
		for i := range 512 {
			if err := loader.Put(fmt.Sprintf("%08d", i*2), "old"); err != nil {
				t.Fatal(err)
			}
		}
		return m, loader, writer
	}
	t.Run("split moves the next key out of the cached leaf", func(t *testing.T) {
		m, other, writer := fixture(t)
		if err := writer.Put("00000002", "prime"); err != nil {
			t.Fatal(err)
		}
		cached, version := writer.putLeaf, writer.putVersion
		for i := 1; i < 192; i += 2 {
			if err := other.Put(fmt.Sprintf("%08d", i), "odd"); err != nil {
				t.Fatal(err)
			}
		}
		if cached.version == version {
			t.Fatal("fixture did not split cached leaf")
		}
		if err := writer.Put("00000188", "new"); err != nil {
			t.Fatal(err)
		}
		if writer.putLeaf == cached {
			t.Fatal("write stayed in old interval")
		}
		if value, ok := other.Get("00000188"); !ok || value != "new" || m.Len() != 608 {
			t.Fatalf("updated value=%q found=%v length=%d", value, ok, m.Len())
		}
	})
	t.Run("merge retires cached right leaf", func(t *testing.T) {
		m, other, writer := fixture(t)
		if err := writer.Put("00000200", "prime"); err != nil {
			t.Fatal(err)
		}
		cached, version := writer.putLeaf, writer.putVersion
		if cached == m.first {
			t.Fatal("fixture must cache a right leaf")
		}
		deleted := 0
		for i := 0; cached.version == version && i < 100; i++ {
			if !other.Delete(fmt.Sprintf("%08d", i*2)) {
				t.Fatal("missing fixture key")
			}
			deleted++
		}
		if cached.version == version {
			t.Fatal("fixture did not retire cached leaf")
		}
		if err := writer.Put("00000200", "new"); err != nil {
			t.Fatal(err)
		}
		if writer.putLeaf == cached {
			t.Fatal("write stayed in retired leaf")
		}
		if value, ok := other.Get("00000200"); !ok || value != "new" || m.Len() != int64(512-deleted) {
			t.Fatalf("updated value=%q found=%v length=%d", value, ok, m.Len())
		}
	})
	t.Run("upper anchor belongs to next leaf", func(t *testing.T) {
		_, other, writer := fixture(t)
		if err := writer.Put("00000002", "prime"); err != nil {
			t.Fatal(err)
		}
		cached := writer.putLeaf
		upper := cached.next.anchor
		if upper == "" {
			t.Fatal("fixture needs a bounded leaf")
		}
		if err := writer.Put(upper, "boundary"); err != nil {
			t.Fatal(err)
		}
		if writer.putLeaf == cached {
			t.Fatal("upper bound treated as inclusive")
		}
		if value, ok := other.Get(upper); !ok || value != "boundary" {
			t.Fatalf("boundary value=%q found=%v", value, ok)
		}
		writer.Close()
		if writer.putLeaf != nil {
			t.Fatal("closed handle retains cached leaf or key bounds")
		}
	})
}

func TestPutHintPrefixCoverage(t *testing.T) {
	t.Run("split invalidates a fully covered prefix", func(t *testing.T) {
		m := New[string, string]()
		writer, other := m.Ref(), m.Ref()
		defer writer.Close()
		defer other.Close()
		for _, key := range []string{"aaaa0000", "aaaa0001"} {
			if err := writer.PutKey(PrepareKey(key), "old"); err != nil {
				t.Fatal(err)
			}
		}
		if !writer.putPrefixChecked || !writer.putPrefixCovered {
			t.Fatal("root leaf should cover the entire prefix")
		}
		cached, version := writer.putLeaf, writer.putVersion
		for i := range 512 {
			if err := other.Put(fmt.Sprintf("aaaa%04d", i), "old"); err != nil {
				t.Fatal(err)
			}
		}
		if cached.version == version {
			t.Fatal("fixture did not split cached leaf")
		}
		if err := writer.PutKey(PrepareKey("aaaa0400"), "new"); err != nil {
			t.Fatal(err)
		}
		if writer.putLeaf == cached {
			t.Fatal("covered hint survived interval change")
		}
		if value, ok := other.Get("aaaa0400"); !ok || value != "new" || m.Len() != 512 {
			t.Fatalf("value=%q found=%v length=%d", value, ok, m.Len())
		}
	})
	t.Run("a partial prefix still checks the upper bound", func(t *testing.T) {
		m := New[string, string]()
		writer, other := m.Ref(), m.Ref()
		defer writer.Close()
		defer other.Close()
		for i := range 512 {
			if err := other.Put(fmt.Sprintf("aaaa%04d", i), "old"); err != nil {
				t.Fatal(err)
			}
		}
		for _, key := range []string{"aaaa0000", "aaaa0001"} {
			if err := writer.PutKey(PrepareKey(key), "prime"); err != nil {
				t.Fatal(err)
			}
		}
		if !writer.putPrefixChecked || writer.putPrefixCovered {
			t.Fatal("upper bound splits the prefix")
		}
		cached, version := writer.putLeaf, writer.putVersion
		upper := cached.next.anchor
		if err := writer.PutKey(PrepareKey(upper), "boundary"); err != nil {
			t.Fatal(err)
		}
		if cached.version != version {
			t.Fatal("fixture unexpectedly changed cached interval")
		}
		if writer.putLeaf == cached {
			t.Fatal("partial prefix bypassed bounds")
		}
		if value, ok := other.Get(upper); !ok || value != "boundary" {
			t.Fatalf("value=%q found=%v", value, ok)
		}
	})
}

func TestPutHintCoverageBounds(t *testing.T) {
	for _, tc := range []struct {
		lower, upper, key string
		covered           bool
	}{
		{"abcd", "abce", "abcd0", true},
		{"abc", "abce", "abcd0", true},
		{"abcd0", "abce", "abcd1", false},
		{"abc", "abcd9", "abcd0", false},
		{"\x00\x00\x00\x00", "\x00\x00\x00\x01", "\x00\x00\x00\x00\xff", true},
		{"\xff\xff\xff\xff", "", "\xff\xff\xff\xff\x00", true},
	} {
		t.Run(fmt.Sprintf("%x-%x", tc.lower, tc.upper), func(t *testing.T) {
			l := &leaf[string, string]{anchor: tc.lower, version: 1}
			if tc.upper != "" {
				l.next = &leaf[string, string]{anchor: tc.upper}
			}
			r := &Ref[string, string]{putLeaf: l, putVersion: 1, putPrefix: keyPrefix(tc.key)}
			got := r.lockPutLeaf(tc.key, keyTag(tc.key), keyPrefix(tc.key))
			got.mu.Unlock()
			if got != l || !r.putPrefixChecked || r.putPrefixCovered != tc.covered {
				t.Fatalf("checked=%v covered=%v want=%v", r.putPrefixChecked, r.putPrefixCovered, tc.covered)
			}
		})
	}
}

func TestPutHintWholeKeyspace(t *testing.T) {
	m := New[string, string]()
	writer, other := m.Ref(), m.Ref()
	defer writer.Close()
	defer other.Close()
	keys := []string{"aaaa", "zzzz", "", "\x00\xff"}
	for _, key := range keys {
		if err := writer.PutKey(PrepareKey(key), "old"); err != nil {
			t.Fatal(err)
		}
		if !writer.putWhole || writer.putLeaf != m.first {
			t.Fatal("single leaf must cover different prefixes and short binary keys")
		}
	}
	cached, version := writer.putLeaf, writer.putVersion
	for i := range 512 {
		if err := other.Put(fmt.Sprintf("mmmm%04d", i), "old"); err != nil {
			t.Fatal(err)
		}
	}
	if cached.version == version {
		t.Fatal("fixture did not split the cached root")
	}
	if err := writer.PutKey(PrepareKey("zzzz"), "new"); err != nil {
		t.Fatal(err)
	}
	if writer.putWhole || writer.putLeaf == cached {
		t.Fatal("whole-keyspace proof survived a split")
	}
	for _, key := range keys {
		want := "old"
		if key == "zzzz" {
			want = "new"
		}
		if got, ok := other.Get(key); !ok || got != want {
			t.Fatalf("key=%x got=%q found=%v want=%q", key, got, ok, want)
		}
	}
	if m.Len() != 516 {
		t.Fatalf("length=%d want 516", m.Len())
	}
}

func TestNumericPutHintArmsAfterRepeatedPrefix(t *testing.T) {
	l := &leaf[uint64, uint64]{anchor: "\x10", next: &leaf[uint64, uint64]{anchor: "\x20"}, version: 7}
	r := new(Ref[uint64, uint64])
	r.rememberNumericPutLeaf(l, 1)
	if r.putLeaf != nil || !r.putCandidateSeen || r.putCandidate != 1 {
		t.Fatal("first random prefix published a leaf pointer")
	}
	r.rememberNumericPutLeaf(l, 2)
	if r.putLeaf != nil || !r.putCandidateSeen || r.putCandidate != 2 {
		t.Fatal("different random prefix published a leaf pointer")
	}
	r.rememberNumericPutLeaf(l, 2)
	if r.putLeaf != l || r.putVersion != 7 || r.putPrefix != 2 || r.putCandidateSeen {
		t.Fatal("repeated prefix did not arm the numeric write finger")
	}

	whole := &leaf[uint64, uint64]{version: 9}
	r = new(Ref[uint64, uint64])
	r.rememberNumericPutLeaf(whole, 3)
	if r.putLeaf != whole || !r.putWhole || !r.putPrefixCovered {
		t.Fatal("whole-keyspace leaf was not cached immediately")
	}
}
