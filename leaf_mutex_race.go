//go:build race

package wormhole

import (
	"runtime"
	"unsafe"
)

// As in sync.RWMutex, hide bookkeeping atomics from the race detector so
// independent readers do not accidentally synchronize with each other.
func leafRaceDisable() { runtime.RaceDisable() }
func leafRaceEnable()  { runtime.RaceEnable() }

func leafRaceReadAcquire(m *leafRWMutex) {
	runtime.RaceAcquire(unsafe.Pointer(&m.writerSync))
}
func leafRaceReadRelease(m *leafRWMutex) {
	runtime.RaceReleaseMerge(unsafe.Pointer(&m.readerSync))
}
func leafRaceWriteAcquire(m *leafRWMutex) {
	runtime.RaceAcquire(unsafe.Pointer(&m.writerSync))
	runtime.RaceAcquire(unsafe.Pointer(&m.readerSync))
}
func leafRaceWriteRelease(m *leafRWMutex) {
	runtime.RaceRelease(unsafe.Pointer(&m.writerSync))
}
