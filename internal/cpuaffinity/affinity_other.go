//go:build !linux

package cpuaffinity

import "fmt"

func CPUs() ([]int, error)              { return nil, fmt.Errorf("CPU pinning requires Linux") }
func Pin(cpu int) (func() error, error) { return nil, fmt.Errorf("CPU pinning requires Linux") }
