package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Run the real command parser and load path in a fresh process, since run uses
// the process-wide flag set and changes GOMAXPROCS.
func TestConcbenchThreadArgument(t *testing.T) {
	words := t.TempDir() + "/words.txt"
	if err := os.WriteFile(words, []byte("alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\neta\ntheta\niota\nkappa\nlambda\nmu\nnu\nxi\nomicron\npi\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
		bad   bool
	}{
		{"explicit-unpinned", []string{"-pin=false"}, "worker affinity disabled", false},
		{"positional", nil, "load x4 ", false},
		{"explicit-single-loader", []string{"-load-workers=1"}, "load x1 ", false},
		{"reject-zero", []string{"-load-workers=0"}, "positive load-workers", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-test.run=^TestConcbenchProcess$", "--", "-load-only"}
			args = append(args, tc.flags...)
			args = append(args, words, "128", "4")
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "WORMHOLE_CONCBENCH_TEST=1")
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.bad || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v, output:\n%s", err, out)
			}
		})
	}
}

func TestConcbenchProcess(t *testing.T) {
	if os.Getenv("WORMHOLE_CONCBENCH_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"concbench"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("concbench", flag.ExitOnError)
	main()
}
