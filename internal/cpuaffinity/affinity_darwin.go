//go:build darwin && amd64 && cgo

// Package cpuaffinity provides best-effort scheduler affinity on macOS.
// Unlike Linux, macOS does not permit binding a thread to a specific CPU.
package cpuaffinity

/*
#include <mach/mach.h>
#include <mach/thread_policy.h>

static kern_return_t affinity_get(integer_t *tag) {
    thread_affinity_policy_data_t policy;
    mach_msg_type_number_t count = THREAD_AFFINITY_POLICY_COUNT;
    boolean_t is_default = FALSE;
    kern_return_t kr = thread_policy_get(mach_thread_self(),
        THREAD_AFFINITY_POLICY, (thread_policy_t)&policy, &count, &is_default);
    if (kr == KERN_SUCCESS) {
        *tag = is_default ? THREAD_AFFINITY_TAG_NULL : policy.affinity_tag;
    }
    return kr;
}

static kern_return_t affinity_set(integer_t tag) {
    thread_affinity_policy_data_t policy = { .affinity_tag = tag };
    return thread_policy_set(mach_thread_self(), THREAD_AFFINITY_POLICY,
        (thread_policy_t)&policy, THREAD_AFFINITY_POLICY_COUNT);
}
*/
import "C"

import (
	"fmt"
	"runtime"
)

// CPUs returns logical CPU indices as convenient *tag labels*.
// These are NOT macOS CPU affinity identifiers.
func CPUs() ([]int, error) {
	n := runtime.NumCPU()
	if n == 0 {
		return nil, fmt.Errorf("no logical CPUs reported")
	}
	cpus := make([]int, n)
	for i := range cpus {
		cpus[i] = i
	}
	return cpus, nil
}

// Pin locks the goroutine to an OS thread and sets a Mach affinity tag.
// The cpu argument is a tag label, NOT a physical/logical CPU number.
// Equal tags encourage cache locality; they do not guarantee CPU placement.
// The returned restore must be called from the same goroutine.
func Pin(cpu int) (restore func() error, err error) {
	if cpu < 0 || cpu >= runtime.NumCPU() {
		return nil, fmt.Errorf("affinity tag label %d outside logical CPU range", cpu)
	}

	runtime.LockOSThread()
	var saved C.integer_t
	if kr := C.affinity_get(&saved); kr != C.KERN_SUCCESS {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("get Mach affinity: kern_return_t=%d", int(kr))
	}

	target := C.integer_t(cpu + 1) // zero is the default/no-affinity tag
	if kr := C.affinity_set(target); kr != C.KERN_SUCCESS {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("set Mach affinity tag %d: kern_return_t=%d", cpu+1, int(kr))
	}

	var actual C.integer_t
	if kr := C.affinity_get(&actual); kr != C.KERN_SUCCESS || actual != target {
		restoreKR := C.affinity_set(saved)
		if restoreKR == C.KERN_SUCCESS {
			runtime.UnlockOSThread()
		}
		return nil, fmt.Errorf("verify Mach affinity tag %d: actual=%d read=%d restore=%d",
			cpu+1, int(actual), int(kr), int(restoreKR))
	}

	return func() error {
		if kr := C.affinity_set(saved); kr != C.KERN_SUCCESS {
			// Keep the OS thread locked if we cannot restore its policy.
			return fmt.Errorf("restore Mach affinity: kern_return_t=%d", int(kr))
		}
		runtime.UnlockOSThread()
		return nil
	}, nil
}
