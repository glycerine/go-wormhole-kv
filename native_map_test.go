//go:build native

package wormhole

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestNativeMapDifferential(t *testing.T) {
	m := New[string, string]()
	r := m.Ref()
	defer r.Close()
	var input, want bytes.Buffer
	boolInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	put := func(k, v string) {
		fmt.Fprintf(&input, "P %s %s\n", hexKey(k), hexKey(v))
		if err := r.Put(k, v); err != nil {
			t.Fatal(err)
		}
		want.WriteString("P 1\n")
	}
	scan := func(start string) {
		fmt.Fprintf(&input, "S %s -\n", hexKey(start))
		want.WriteString("S\n")
		it := r.Iterator()
		it.Seek(start)
		for it.Valid() {
			k, v, _ := it.Next()
			fmt.Fprintf(&want, "K %s %s\n", hexKey(k), hexKey(v))
		}
		it.Close()
		want.WriteString("E\n")
	}
	inspectIterator := func(start string) {
		fmt.Fprintf(&input, "T %s -\n", hexKey(start))
		it := r.Iterator()
		it.Seek(start)
		calls := 0
		for range 2 {
			exists := it.Inspect(func(_ string, value string, exists bool) {
				calls++
				fmt.Fprintf(&want, "I %d %s %d\n", boolInt(exists), hexKey(value), calls)
			})
			fmt.Fprintf(&want, "T %d\n", boolInt(exists))
		}
		it.Close()
	}
	inspectIterator("") // native callback also runs on an empty iterator
	layout := func() {
		// Full scans synchronize both implementations' lazy key-order arrays.
		scan("")
		input.WriteString("A - -\n")
		for l := m.first; l != nil; l = l.next {
			fmt.Fprintf(&want, "A %s %d %d\n", hexKey(l.anchor), l.n, l.sorted)
			for i := range leafCapacity {
				e := l.entry(i)
				if e.tag != 0 {
					fmt.Fprintf(&want, "H %d %d %s\n", i, e.tag, hexKey(e.kv.key))
				}
			}
			want.WriteString("O")
			for i := 0; i < l.n; i++ {
				fmt.Fprintf(&want, " %d", l.ss[i])
			}
			want.WriteByte('\n')
		}
		want.WriteString("E\n")
	}
	keys := make([]string, 2000)
	for i := range keys {
		keys[i] = fmt.Sprintf("%c-key-%04d", byte(i), i)
	}
	for i := 0; i < 140; i++ {
		keys[i] = "p" + strings.Repeat("\x00", i)
	}
	keys[140], keys[141], keys[142] = "", "\xff", "\x00"
	rng := rand.New(rand.NewPCG(31, 77))
	for j, i := range rng.Perm(len(keys)) {
		put(keys[i], fmt.Sprint(i))
		if j%251 == 0 {
			layout()
		}
	}
	layout()
	for step := 0; step < 3000; step++ {
		key := keys[rng.IntN(len(keys))]
		switch rng.IntN(20) {
		case 19:
			inspectIterator(key)
		case 15:
			value, calls := fmt.Sprintf("merged-%d", step), 0
			fmt.Fprintf(&input, "M %s %s\n", hexKey(key), hexKey(value))
			if err := r.Merge(key, func(string, bool) (string, bool) { calls++; return value, true }); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&want, "M 1 %d\n", calls)
		case 16:
			calls := 0
			fmt.Fprintf(&input, "N %s -\n", hexKey(key))
			if err := r.Merge(key, func(string, bool) (string, bool) { calls++; return "ignored", false }); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&want, "N 1 %d\n", calls)
		case 17:
			calls := 0
			fmt.Fprintf(&input, "I %s -\n", hexKey(key))
			r.Inspect(key, func(value string, exists bool) {
				calls++
				fmt.Fprintf(&want, "I %d %s %d\n", boolInt(exists), hexKey(value), calls)
			})
		case 18:
			calls := 0
			fmt.Fprintf(&input, "U %s -\n", hexKey(key))
			exists := r.Update(key, func(value string, _ bool) string {
				calls++
				buf := []byte(value)
				for i := range buf {
					buf[i] ^= 1
				}
				return string(buf)
			})
			fmt.Fprintf(&want, "U %d %d\n", boolInt(exists), calls)
		case 0:
			end := keys[rng.IntN(len(keys))]
			if end < key {
				key, end = end, key
			}
			if end == key {
				end += "\x00"
			}
			fmt.Fprintf(&input, "R %s %s\n", hexKey(key), hexKey(end))
			fmt.Fprintf(&want, "R %d\n", r.DeleteRange(key, end))
		case 1:
			fmt.Fprintf(&input, "R %s *\n", hexKey(key))
			fmt.Fprintf(&want, "R %d\n", r.DeleteFrom(key))
		case 2, 3:
			fmt.Fprintf(&input, "D %s -\n", hexKey(key))
			fmt.Fprintf(&want, "D %d\n", boolInt(r.Delete(key)))
		case 4:
			fmt.Fprintf(&input, "G %s -\n", hexKey(key))
			v, ok := r.Get(key)
			fmt.Fprintf(&want, "G %d %s\n", boolInt(ok), hexKey(v))
		case 5:
			fmt.Fprintf(&input, "B %s -\n", hexKey(key))
			fmt.Fprintf(&want, "B %d\n", boolInt(r.Probe(key)))
		case 6:
			scan(key)
		default:
			put(key, fmt.Sprintf("value-%d\x00", step))
		}
		if step%73 == 0 {
			layout()
		}
	}
	layout()
	input.WriteString("R - *\n")
	fmt.Fprintf(&want, "R %d\n", r.DeleteFrom(""))
	layout()
	inspectIterator("")
	out := runNativeOracle(t, "map_oracle.c", input.Bytes())
	if m.first.dense {
		// Stable slots intentionally differ from native magnet positions.
		// Compare each leaf's membership, tags and sorted key order instead.
		compareNativeTrace(t, canonicalNativeLayout(t, out), canonicalNativeLayout(t, want.Bytes()))
	} else {
		compareNativeTrace(t, out, want.Bytes())
	}
}

// canonicalNativeLayout preserves anchors, occupancy, tags and ordered leaf
// contents while translating implementation-specific slot IDs back to keys.
func canonicalNativeLayout(t *testing.T, trace []byte) []byte {
	t.Helper()
	var out strings.Builder
	slots := map[int]string{}
	var members []string
	for _, line := range strings.Split(strings.TrimSuffix(string(trace), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			t.Fatal("empty oracle line")
		}
		switch fields[0] {
		case "A":
			slots = map[int]string{}
			members = nil
			out.WriteString(line + "\n")
		case "H":
			if len(fields) != 4 {
				t.Fatalf("malformed slot: %q", line)
			}
			slot, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := slots[slot]; exists {
				t.Fatalf("duplicate slot %d", slot)
			}
			slots[slot] = fields[3]
			members = append(members, "H "+fields[2]+" "+fields[3])
		case "O":
			sort.Strings(members)
			for _, member := range members {
				out.WriteString(member + "\n")
			}
			if len(fields)-1 != len(slots) {
				t.Fatalf("order coverage: %q", line)
			}
			out.WriteString("O")
			seen := map[int]bool{}
			for _, field := range fields[1:] {
				slot, err := strconv.Atoi(field)
				if err != nil {
					t.Fatal(err)
				}
				key, exists := slots[slot]
				if !exists || seen[slot] {
					t.Fatalf("invalid ordered slot %d", slot)
				}
				seen[slot] = true
				out.WriteString(" " + key)
			}
			out.WriteByte('\n')
		default:
			out.WriteString(line + "\n")
		}
	}
	return []byte(out.String())
}
