//go:build !linux && !(darwin && amd64 && cgo)

package cpuaffinity

import "testing"

func TestUnsupportedAffinity(t *testing.T) {
	if cpus, err := CPUs(); err == nil || cpus != nil {
		t.Fatalf("CPUs() = %v, %v; want nil and unsupported error", cpus, err)
	}
	for _, cpu := range []int{-1, 0, 1024} {
		if restore, err := Pin(cpu); err == nil || restore != nil {
			t.Fatalf("Pin(%d) returned restore=%v, err=%v; want nil and unsupported error", cpu, restore != nil, err)
		}
	}
}
