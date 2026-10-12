//go:build !race

package wormhole

func leafRaceDisable()                    {}
func leafRaceEnable()                     {}
func leafRaceReadAcquire(m *leafRWMutex)  {}
func leafRaceReadRelease(m *leafRWMutex)  {}
func leafRaceWriteAcquire(m *leafRWMutex) {}
func leafRaceWriteRelease(m *leafRWMutex) {}
