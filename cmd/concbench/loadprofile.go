package main

import (
	"os"
	"runtime"
	"runtime/pprof"
)

// profileLoad isolates timed writes from dictionary/key construction. Full
// contention sampling is diagnostic and perturbs throughput; leave it off
// when comparing unprofiled benchmark rates.
func profileLoad(prefix string) (func() error, error) {
	if prefix == "" {
		return func() error { return nil }, nil
	}
	f, err := os.Create(prefix + ".cpu")
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return nil, err
	}
	oldMutexRate := runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)
	return func() error {
		pprof.StopCPUProfile()
		runtime.SetMutexProfileFraction(oldMutexRate)
		runtime.SetBlockProfileRate(0)
		if err := f.Close(); err != nil {
			return err
		}
		for _, name := range []string{"mutex", "block"} {
			file, err := os.Create(prefix + "." + name)
			if err != nil {
				return err
			}
			writeErr := pprof.Lookup(name).WriteTo(file, 0)
			closeErr := file.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	}, nil
}
