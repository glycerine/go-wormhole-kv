//go:build native

package wormhole

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run explicitly with go test -tags=native. A C compiler and the repository's
// native sources are required; ordinary Go package users need neither.
func TestNativeLeafDifferential(t *testing.T) {
	for _, width := range []int{3, 8} {
		t.Run(fmt.Sprintf("given key width %d when churning and sorting then match every native slot", width), func(t *testing.T) {
			checkNativeLeafTrace(t, width)
		})
	}
}

func checkNativeLeafTrace(t *testing.T, width int) {
	t.Helper()
	var input, want bytes.Buffer
	fmt.Fprintf(&input, "F 0 %d\n", width)
	var l leaf[string, string]
	var present [256]bool
	var tags [256]uint16
	rng := rand.New(rand.NewPCG(17, 42))
	for step := 0; step < 6000; step++ {
		id := rng.IntN(256)
		op := byte('S')
		tag := uint16(0)
		key := fmt.Sprintf("key-%0*d", width, id)
		if present[id] {
			op = 'D'
			l.remove(l.match(key, tags[id]))
			present[id] = false
		} else if l.n < leafCapacity && step%5 != 0 {
			op = 'I'
			tag = uint16(rng.IntN(65535) + 1)
			if step%3 == 0 {
				tag = 32768
			}
			l.insert(&record[string, string]{key, ""}, tag)
			tags[id], present[id] = tag, true
		} else {
			l.syncSorted()
		}
		fmt.Fprintf(&input, "%c %d %d\n", op, id, tag)
		fmt.Fprintf(&want, "%d %d", l.n, l.sorted)
		for i := range l.hs {
			e := l.entry(i)
			if e.tag != 0 {
				fmt.Fprintf(&want, " %d:%d:%s", i, e.tag, e.kv.key)
			}
		}
		want.WriteString(" |")
		for i := 0; i < l.n; i++ {
			fmt.Fprintf(&want, " %d", l.ss[i])
		}
		want.WriteByte('\n')
	}
	out := runNativeOracle(t, "leaf_oracle.c", input.Bytes())
	compareNativeTrace(t, out, want.Bytes())
}

func runNativeOracle(t *testing.T, source string, input []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "oracle")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cc", "-std=gnu11", "-O2", "-march=native", "-DNOSIGNAL", "-pthread", "-o", bin,
		filepath.Join("test_vectors", source), "../kv.c", "../lib.c", "-lm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native oracle: %v\n%s", err, out)
	}
	// Use files for the large trace in both directions: this avoids coupling
	// the oracle to pipe buffer sizes and also leaves reproducible artifacts
	// until the test's temporary directory is cleaned up.
	inPath, outPath := filepath.Join(dir, "trace.in"), filepath.Join(dir, "trace.out")
	if err := os.WriteFile(inPath, input, 0600); err != nil {
		t.Fatal(err)
	}
	inFile, err := os.Open(inPath)
	if err != nil {
		t.Fatal(err)
	}
	defer inFile.Close()
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	cmd = exec.CommandContext(ctx, bin)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inFile, outFile, outFile
	if err := cmd.Run(); err != nil {
		t.Fatalf("native oracle: %v", err)
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func compareNativeTrace(t *testing.T, got, want []byte) {
	t.Helper()
	gotLines, wantLines := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("got %d lines, want %d", len(gotLines), len(wantLines))
	}
	for i := range gotLines {
		if gotLines[i] != wantLines[i] {
			t.Fatalf("step %d\nnative %s\nGo     %s", i, gotLines[i], wantLines[i])
		}
	}
}
