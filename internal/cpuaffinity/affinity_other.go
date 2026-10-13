//go:build !linux && !darwin

package cpuaffinity

import "fmt"

func CPUs() ([]int, error) {
	return nil, fmt.Errorf("CPU pinning requires Linux or Darwin/amd64")
}

func Pin(cpu int) (func() error, error) {
	return nil, fmt.Errorf("CPU pinning requires Linux or Darwin/amd64")
}
