// Package cpuaffinity pins benchmark workers to Linux CPUs.
package cpuaffinity

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Linux's cpu_set_t supports 1024 CPUs, as in the native benchmark.
type mask [128]byte

func current() (mask, error) {
	var m mask
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(len(m)), uintptr(unsafe.Pointer(&m[0])))
	if errno != 0 {
		return m, errno
	}
	return m, nil
}

func set(m *mask) error {
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(m)), uintptr(unsafe.Pointer(&m[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

// CPUs returns the current thread's allowed CPUs in ascending order. Call it
// before pinning workers; worker i uses cpus[i % len(cpus)], like native C.
func CPUs() ([]int, error) {
	m, err := current()
	if err != nil {
		return nil, err
	}
	var cpus []int
	for i, bits := range m {
		for bit := range 8 {
			if bits&(1<<bit) != 0 {
				cpus = append(cpus, i*8+bit)
			}
		}
	}
	if len(cpus) == 0 {
		return nil, fmt.Errorf("empty CPU affinity")
	}
	return cpus, nil
}

// Pin locks the goroutine to its OS thread, sets and verifies single-CPU
// affinity, and returns a function that restores the original affinity and
// unlocks the thread. The caller must invoke restore on the same goroutine.
func Pin(cpu int) (restore func() error, err error) {
	if cpu < 0 || cpu >= 1024 {
		return nil, fmt.Errorf("CPU %d outside cpu_set_t", cpu)
	}
	runtime.LockOSThread()
	saved, err := current()
	if err != nil {
		runtime.UnlockOSThread()
		return nil, err
	}
	var target mask
	target[cpu/8] = 1 << (cpu % 8)
	if err = set(&target); err != nil {
		runtime.UnlockOSThread()
		return nil, err
	}
	actual, err := current()
	if err != nil || actual != target {
		restoreErr := set(&saved)
		// Never return a thread with unintended affinity to the runtime.
		if restoreErr == nil {
			runtime.UnlockOSThread()
		}
		return nil, fmt.Errorf("verify CPU %d affinity: read=%v restore=%v", cpu, err, restoreErr)
	}
	return func() error {
		if err := set(&saved); err != nil {
			return err
		}
		runtime.UnlockOSThread()
		return nil
	}, nil
}
