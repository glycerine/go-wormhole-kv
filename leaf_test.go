package wormhole

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// checkLeaf deliberately checks representation as well as observable results:
// a lookup-only oracle can miss stale ss indices until the next range query.
func checkLeaf(t testing.TB, l *leaf[string, string], want map[string]string, tags map[string]uint16) {
	t.Helper()
	if l.n != len(want) || l.sorted < 0 || l.sorted > l.n {
		t.Fatalf("counts: n=%d sorted=%d want=%d", l.n, l.sorted, len(want))
	}
	occupied := 0
	var occupiedBits [2]uint64
	lastTag := uint16(0)
	for i := range l.tags {
		e := l.entry(i)
		if e.tag == 0 {
			if !l.dense && e.kv != nil || l.dense && (e.kv.key != "" || e.kv.value != "") {
				t.Fatalf("vacant slot %d retains record", i)
			}
			continue
		}
		occupied++
		occupiedBits[i>>6] |= uint64(1) << (i & 63)
		if e.kv == nil || !l.dense && e.tag < lastTag {
			t.Fatalf("invalid hash order at %d", i)
		}
		lastTag = e.tag
		v, ok := want[e.kv.key]
		if !ok || v != e.kv.value {
			t.Fatalf("unexpected hash entry at %d: %+v", i, e.kv)
		}
		if found := l.match(e.kv.key, e.tag); found != i {
			t.Fatalf("key %q in slot %d found at %d", e.kv.key, i, found)
		}
	}
	if l.occupied != occupiedBits {
		t.Fatalf("occupancy bits=%x want=%x", l.occupied, occupiedBits)
	}
	if occupied != l.n {
		t.Fatalf("occupied=%d n=%d", occupied, l.n)
	}
	if l.dense && !useLeafSIMD {
		var linked [leafCapacity]bool
		count := 0
		for bucket, first := range l.buckets {
			for link := first; link != 0; link = l.chain[link-1] {
				slot := int(link) - 1
				if slot >= leafCapacity || linked[slot] || l.tags[slot] == 0 || byte(l.tags[slot]) != byte(bucket) {
					t.Fatal("invalid dense chain", bucket, slot)
				}
				linked[slot] = true
				count++
			}
		}
		if count != l.n {
			t.Fatal("dense chain coverage")
		}
	}
	var seen [leafCapacity]bool
	for i := 0; i < l.n; i++ {
		s := l.ss[i]
		if seen[s] || l.tags[s] == 0 {
			t.Fatalf("invalid ss[%d]=%d", i, s)
		}
		seen[s] = true
		if i > 0 && i < l.sorted && l.at(i-1).key >= l.at(i).key {
			t.Fatalf("sorted prefix out of order at %d", i)
		}
	}
	for k, v := range want {
		i := l.match(k, tags[k])
		if i == leafCapacity || l.recordAtSlot(i).value != v {
			t.Fatalf("missing key %q", k)
		}
	}
}

func TestCRC32C(t *testing.T) {
	t.Run("given raw native seed when hashing then match independent bitwise CRC", func(t *testing.T) {
		for _, key := range []string{"", "123456789", "\x00\xff\x80", strings.Repeat("prefix", 1000)} {
			want := crcSeed
			for i := 0; i < len(key); i++ {
				want ^= uint32(key[i])
				for j := 0; j < 8; j++ {
					if want&1 != 0 {
						want = want>>1 ^ 0x82f63b78
					} else {
						want >>= 1
					}
				}
			}
			if got := crcExtend(crcSeed, key); got != want {
				t.Fatalf("CRC(%q)=%x want %x", key, got, want)
			}
			for cut := 0; cut <= len(key); cut++ {
				if got := crcExtend(crcExtend(crcSeed, key[:cut]), key[cut:]); got != want {
					t.Fatalf("incremental CRC at %d=%x want %x", cut, got, want)
				}
			}
		}
		if keyTag("") == 0 {
			t.Fatal("zero tag reserved for vacancy")
		}
	})
}

func TestLeafCollisions(t *testing.T) {
	for _, tag := range []uint16{1, 511, 512, 32768, 65024, 65535} {
		t.Run(fmt.Sprintf("given 128 colliding tags %d when deleting then every remaining key is reachable", tag), func(t *testing.T) {
			var l leaf[string, string]
			want, tags := map[string]string{}, map[string]uint16{}
			for i := 0; i < leafCapacity; i++ {
				k := fmt.Sprintf("key-%03d", i)
				l.insert(&record[string, string]{k, k}, tag)
				want[k], tags[k] = k, tag
				checkLeaf(t, &l, want, tags)
			}
			if l.sorted != l.n {
				t.Fatal("sequential inserts should stay sorted")
			}
			if l.match("absent", tag) != leafCapacity {
				t.Fatal("collision caused false positive")
			}
			r := rand.New(rand.NewPCG(uint64(tag), 17))
			for _, x := range r.Perm(leafCapacity) {
				k := fmt.Sprintf("key-%03d", x)
				if old := l.remove(l.match(k, tag)); old.key != k {
					t.Fatalf("removed %q want %q", old.key, k)
				}
				delete(want, k)
				checkLeaf(t, &l, want, tags)
				if l.match(k, tag) != leafCapacity {
					t.Fatal("deleted key still present")
				}
			}
		})
	}
}

func runLeafProperty(t testing.TB, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed^0x1234))
	var l leaf[string, string]
	want, tags := map[string]string{}, map[string]uint16{}
	keys := make([]string, leafCapacity*2)
	for i := range keys {
		keys[i] = fmt.Sprintf("%c-key-%03d", byte(i), i)
	}
	keys[0], keys[1], keys[2] = "", "\x00", "\x00\x00"
	for step := 0; step < steps; step++ {
		k := keys[rng.IntN(len(keys))]
		tag := keyTag(k)
		// Mix random tags, true CRC tags, and deliberate collisions. Entries
		// retain the same tag during each lifetime.
		if _, ok := want[k]; !ok {
			switch rng.IntN(3) {
			case 0:
				tag = uint16(rng.IntN(65535) + 1)
			case 1:
				tag = 32768
			}
		} else {
			tag = tags[k]
		}
		i := l.match(k, tag)
		switch rng.IntN(5) {
		case 0:
			if i < leafCapacity {
				l.remove(i)
				delete(want, k)
			}
		case 1:
			l.syncSorted()
			ordered := make([]string, 0, len(want))
			for key := range want {
				ordered = append(ordered, key)
			}
			slices.Sort(ordered)
			for j, key := range ordered {
				if l.at(j).key != key {
					t.Fatalf("seed=%d step=%d order", seed, step)
				}
			}
			for _, key := range keys {
				pos, _ := slices.BinarySearch(ordered, key)
				if got := l.seek(key); got != pos {
					t.Fatalf("seek %q=%d want %d", key, got, pos)
				}
			}
		default:
			v := fmt.Sprint(step)
			if i < leafCapacity {
				l.hs[i] = &record[string, string]{k, v}
			} else if l.n < leafCapacity {
				l.insert(&record[string, string]{k, v}, tag)
			} else {
				break
			}
			want[k], tags[k] = v, tag
		}
		checkLeaf(t, &l, want, tags)
	}
}

func TestLeafRandomized(t *testing.T) {
	for seed := uint64(0); seed < 24; seed++ {
		t.Run(fmt.Sprintf("given seed %d when mixing operations then match reference map and sorted keys", seed), func(t *testing.T) {
			runLeafProperty(t, seed, 2500)
		})
	}
}

func FuzzLeafOperations(f *testing.F) {
	for _, seed := range []uint64{0, 1, 42, 65535} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { runLeafProperty(t, seed, 500) })
}

func TestLeafSplitAndMerge(t *testing.T) {
	for _, middle := range []bool{false, true} {
		for _, shape := range []string{"sequential", "prefix", "binary", "skewed"} {
			t.Run(fmt.Sprintf("given full %s leaf middle=%v when splitting and merging then preserve keys and shortest anchor", shape, middle), func(t *testing.T) {
				keys := make([]string, leafCapacity+1)
				for i := range keys {
					switch shape {
					case "sequential":
						keys[i] = fmt.Sprintf("%04d", i)
					case "prefix":
						keys[i] = "p" + strings.Repeat("\x00", i)
					case "binary":
						keys[i] = string([]byte{byte(i), 0xff, 0})
					case "skewed":
						keys[i] = fmt.Sprintf("a%04d", i)
						if i == leafCapacity {
							keys[i] = "z"
						}
					}
				}
				// Exercise insertion at both extremes and in the middle.
				for _, added := range []int{0, 63, 128} {
					var l leaf[string, string]
					if middle {
						l.next = &leaf[string, string]{anchor: "zz"}
					}
					rng := rand.New(rand.NewPCG(uint64(added), 42))
					for _, i := range rng.Perm(len(keys)) {
						if i != added {
							l.insert(&record[string, string]{keys[i], keys[i]}, keyTag(keys[i]))
						}
					}
					right := l.split(&record[string, string]{keys[added], keys[added]}, keyTag(keys[added]))
					if right == nil {
						t.Fatal("split unexpectedly refused")
					}
					if l.n == 0 || right.n == 0 || l.n+right.n != len(keys) {
						t.Fatal("invalid split counts")
					}
					if right.prev != &l || right.next != l.next {
						t.Fatal("invalid unpublished links")
					}
					want, tags := map[string]string{}, map[string]uint16{}
					for _, k := range keys {
						want[k], tags[k] = k, keyTag(k)
					}
					checkPart := func(part *leaf[string, string], start, end int) {
						w := map[string]string{}
						for _, k := range keys[start:end] {
							w[k] = k
						}
						checkLeaf(t, part, w, tags)
						if part.sorted != part.n {
							t.Fatal("split left dirty keys")
						}
					}
					checkPart(&l, 0, l.n)
					checkPart(right, l.n, len(keys))
					if !(l.at(l.n-1).key < right.anchor && right.anchor <= right.at(0).key) {
						t.Fatal("anchor does not separate leaves")
					}
					minLen := commonPrefix(keys[0], keys[len(keys)-1]) + 1
					if len(right.anchor) != minLen {
						t.Fatalf("anchor length=%d minimum=%d", len(right.anchor), minLen)
					}
					// Remove one record so the pair fits in a single native leaf.
					k := right.at(right.n - 1).key
					right.remove(right.match(k, tags[k]))
					delete(want, k)
					l.mergeRight(right)
					checkLeaf(t, &l, want, tags)
					l.syncSorted()
					checkLeaf(t, &l, want, tags)
				}
			})
		}
	}
}

func TestLeafSplitLengthLimit(t *testing.T) {
	t.Run("given anchor beyond native length limit when splitting then leave records intact", func(t *testing.T) {
		var l leaf[string, string]
		prefix := strings.Repeat("x", 65535)
		want, tags := map[string]string{}, map[string]uint16{}
		for i := 0; i < leafCapacity; i++ {
			k := prefix + string(byte(i))
			want[k], tags[k] = "", keyTag(k)
			l.insert(&record[string, string]{k, ""}, tags[k])
		}
		key := prefix + "\xff"
		if right := l.split(&record[string, string]{key, ""}, keyTag(key)); right != nil {
			t.Fatal("accepted oversized anchor")
		}
		checkLeaf(t, &l, want, tags)
	})
}
