package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glycerine/go-wormhole-kv/internal/benchworkload"
)

// Use the same six-word generator as concbench, then split one deterministic
// stream into the fixture and a disjoint fresh-write trace. Unlike concbench,
// these benchmarks use ordinary Put and string values for both containers.
func corpusWriteTrace(b testing.TB, n int) (loaded, keys []string) {
	b.Helper()
	path := os.Getenv("WORMHOLE_BENCH_WORDS")
	if path == "" {
		path = "../../words.txt"
	}
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	words, err := benchworkload.ReadWords(f)
	if err != nil {
		b.Fatal(err)
	}
	all, _, err := benchworkload.Corpus(words, 2*n)
	if err != nil {
		b.Fatal(err)
	}
	return all[:n], all[n:]
}

func BenchmarkCorpusWrites(b *testing.B)       { benchmarkCorpus(b, false) }
func BenchmarkCorpusWriteLatency(b *testing.B) { benchmarkCorpus(b, true) }

func benchmarkCorpus(b *testing.B, latency bool) {
	loaded, keys := corpusWriteTrace(b, loadSize())
	for _, l := range layouts {
		for _, workers := range []int{1, 4} {
			if workers > 1 && !l.concurrent {
				continue
			}
			name := l.name + "/Workers1"
			if workers == 4 {
				name = l.name + "/Workers4"
			}
			b.Run(name, func(b *testing.B) {
				benchmarkWrites(b, l, "CorpusFresh", loaded, keys, workers, latency)
			})
		}
	}
}

func TestCorpusWriteTrace(t *testing.T) {
	t.Run("given the concbench dictionary when dividing the generated stream then keep fresh writes disjoint", func(t *testing.T) {
		var words strings.Builder
		for i := range 256 {
			fmt.Fprintf(&words, "word-%03d\n", i)
		}
		path := filepath.Join(t.TempDir(), "words.txt")
		if err := os.WriteFile(path, []byte(words.String()), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WORMHOLE_BENCH_WORDS", path)
		loaded, keys := corpusWriteTrace(t, 2048)
		if len(loaded) != 2048 || len(keys) != 2048 {
			t.Fatal("incorrect trace sizes")
		}
		seen := make(map[string]bool)
		for _, part := range [][]string{loaded, keys} {
			for _, key := range part {
				if seen[key] {
					t.Fatal("trace contains a duplicate", key)
				}
				seen[key] = true
			}
		}
	})
}
