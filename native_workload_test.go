//go:build native

package wormhole

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/glycerine/go-wormhole-kv/internal/benchworkload"
)

func TestNativeBenchmarkWorkload(t *testing.T) {
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"}
	const nkeys = 10000
	var input, want bytes.Buffer
	fmt.Fprintf(&input, "%d %d\n", len(words), nkeys)
	for _, word := range words {
		fmt.Fprintln(&input, word)
	}
	keys, _, err := benchworkload.Corpus(words, nkeys)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		fmt.Fprintln(&want, key)
	}
	rng := benchworkload.NewLehmer()
	for range 10000 {
		fmt.Fprintln(&want, rng.Next())
	}
	compareNativeTrace(t, runNativeOracle(t, "corpus_oracle.c", input.Bytes()), want.Bytes())
}
