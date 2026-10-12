package benchworkload

import (
	"strings"
	"testing"
)

func TestLibcRandom(t *testing.T) {
	t.Run("given glibc default seed when drawing then reproduce its published sequence", func(t *testing.T) {
		r := newLibcRandom()
		for i, want := range []uint32{1804289383, 846930886, 1681692777, 1714636915, 1957747793, 424238335, 719885386, 1649760492, 596516649, 1189641421} {
			if got := r.next(); got != want {
				t.Fatalf("draw %d: %d != %d", i, got, want)
			}
		}
	})
}

func TestReadWords(t *testing.T) {
	t.Run("given CRLF and unterminated input when reading then match native byte removal", func(t *testing.T) {
		words, err := ReadWords(strings.NewReader("alpha\r\nbeta\nlast"))
		if err != nil || strings.Join(words, "|") != "alpha\r|beta|las" {
			t.Fatalf("%q %v", words, err)
		}
	})
	for _, input := range []string{"", "a\x00b\n"} {
		if _, err := ReadWords(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestCorpus(t *testing.T) {
	t.Run("given two dictionary words when generating then consume six consecutive draws per key", func(t *testing.T) {
		keys, fingerprint, err := Corpus([]string{"a", "b"}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if keys[0] != "b a b b b b!" || keys[1] != "a a b b a b!" {
			t.Fatal(keys)
		}
		_, again, _ := Corpus([]string{"a", "b"}, 2)
		if len(fingerprint) != 64 || fingerprint != again {
			t.Fatal(fingerprint, again)
		}
	})
	for _, words := range [][]string{nil, {"a\n"}, {"a\x00"}} {
		if _, _, err := Corpus(words, 1); err == nil {
			t.Fatal("accepted", words)
		}
	}
}
