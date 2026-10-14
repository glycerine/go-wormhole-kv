//go:build amd64 && !race

package wormhole

import (
	"os"
	"strings"
)

var leafAVX2 = func() bool {
	for _, option := range strings.Split(os.Getenv("GODEBUG"), ",") {
		if option == "cpu.all=off" || option == "cpu.avx=off" || option == "cpu.avx2=off" {
			return false
		}
	}
	return hasLeafAVX2()
}()

func hasLeafAVX2() bool

const useLeafSIMD = true

// matchLeafTags compares all stable-slot fingerprints using AVX2 when the CPU
// and OS support it, and baseline SSE2 otherwise.
// The caller holds the leaf lock, including for this assembly read.
//
//go:noescape
func matchLeafTags(tags *[leafCapacity]uint16, tag uint16) (lo, hi uint64)
