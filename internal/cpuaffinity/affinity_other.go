//go:build !linux && !(darwin && amd64 && cgo)

package cpuaffinity

import (
	"fmt"
	"runtime"
)

func CPUs() ([]int, error) {
	return nil, fmt.Errorf("CPU pinning unavailable on %s/%s: requires Linux or Darwin/amd64 with cgo", runtime.GOOS, runtime.GOARCH)
}

func Pin(cpu int) (func() error, error) {
	return nil, fmt.Errorf("CPU pinning unavailable on %s/%s: requires Linux or Darwin/amd64 with cgo", runtime.GOOS, runtime.GOARCH)
}
