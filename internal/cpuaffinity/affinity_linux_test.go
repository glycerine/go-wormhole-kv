package cpuaffinity

import (
	"runtime"
	"slices"
	"sync"
	"testing"
)

func TestPinAndRestore(t *testing.T) {
	cpus, err := CPUs()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := range min(4, len(cpus)) {
		wg.Go(func() {
			// Keep this thread stable while checking the restoration too.
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			before, err := CPUs()
			if err != nil {
				t.Error(err)
				return
			}
			restore, err := Pin(cpus[worker])
			if err != nil {
				t.Error(err)
				return
			}
			pinned, err := CPUs()
			if err != nil || !slices.Equal(pinned, []int{cpus[worker]}) {
				t.Errorf("pin: %v %v", pinned, err)
			}
			if err := restore(); err != nil {
				t.Error(err)
				return
			}
			after, err := CPUs()
			if err != nil || !slices.Equal(before, after) {
				t.Errorf("restore: before=%v after=%v err=%v", before, after, err)
			}
		})
	}
	wg.Wait()
}
