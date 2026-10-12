package wormhole

import (
	"os"
	"strings"
)

var shortCRCEnabled = func() bool {
	// Respect the standard runtime's feature-disabling diagnostic switches.
	for _, option := range strings.Split(os.Getenv("GODEBUG"), ",") {
		if option == "cpu.all=off" || option == "cpu.sse42=off" {
			return false
		}
	}
	return hasCRC32()
}()

func crcExtend(seed uint32, key string) uint32 {
	// Metadata hashing mostly extends by 1–16 bytes. The unaligned native
	// instruction loop avoids the generic checksum dispatch/alignment work.
	// Keep the standard library's parallel CRC path for longer keys.
	if shortCRCEnabled && len(key) <= 64 {
		return crcShort(seed, key)
	}
	return crcGeneric(seed, key)
}

//go:noescape
func crcShort(seed uint32, key string) uint32

func hasCRC32() bool
