package wormhole

import (
	"runtime"
	"sync/atomic"
)

const leafWriterBit = uint32(1 << 31)

// leafRWMutex gates new readers as soon as a writer reserves the high bit.
// Existing readers drain before that writer enters. Waiters yield after a
// bounded poll so preempted holders can progress, including with one P.
// Unlock uses a release store after validating exclusive ownership. The
// acquire operations and release store synchronize protected record accesses.
// It must not be copied after first use.
type leafRWMutex struct {
	state                  uint32
	writerSync, readerSync uint32   // race synchronization addresses, not counters
	_                      [12]byte // preserve the existing leaf layout
}

func (m *leafRWMutex) Lock() {
	leafRaceDisable()
	if !atomic.CompareAndSwapUint32(&m.state, 0, leafWriterBit) {
		m.lockSlow()
	}
	leafRaceEnable()
	leafRaceWriteAcquire(m)
}

func (m *leafRWMutex) lockSlow() {
	spins := 0
	for {
		v := atomic.LoadUint32(&m.state)
		if v&leafWriterBit == 0 && atomic.CompareAndSwapUint32(&m.state, v, v|leafWriterBit) {
			break
		}
		leafLockWait(&spins)
	}
	for atomic.LoadUint32(&m.state) != leafWriterBit {
		leafLockWait(&spins)
	}
}

func (m *leafRWMutex) TryLock() bool {
	leafRaceDisable()
	ok := atomic.CompareAndSwapUint32(&m.state, 0, leafWriterBit)
	leafRaceEnable()
	if ok {
		leafRaceWriteAcquire(m)
	}
	return ok
}

func (m *leafRWMutex) Unlock() {
	leafRaceWriteRelease(m)
	leafRaceDisable()
	old := atomic.LoadUint32(&m.state)
	if old != leafWriterBit {
		leafRaceEnable()
		panic("wormhole: invalid leaf write unlock")
	}
	leafStoreRelease(&m.state)
	leafRaceEnable()
}

func (m *leafRWMutex) RLock() {
	leafRaceDisable()
	// Most point reads acquire an otherwise idle leaf. Avoid a shared load
	// before requesting cache ownership; concurrent readers use the slow path.
	if !atomic.CompareAndSwapUint32(&m.state, 0, 1) {
		m.rlockSlow()
	}
	leafRaceEnable()
	leafRaceReadAcquire(m)
}

func (m *leafRWMutex) rlockSlow() {
	spins := 0
	for {
		v := atomic.LoadUint32(&m.state)
		if v == leafWriterBit-1 {
			panic("wormhole: too many leaf readers")
		}
		if v < leafWriterBit-1 && atomic.CompareAndSwapUint32(&m.state, v, v+1) {
			return
		}
		leafLockWait(&spins)
	}
}

func (m *leafRWMutex) RUnlock() {
	leafRaceReadRelease(m)
	leafRaceDisable()
	v := atomic.AddUint32(&m.state, ^uint32(0))
	leafRaceEnable()
	if v&(leafWriterBit-1) == leafWriterBit-1 {
		panic("wormhole: invalid leaf read unlock")
	}
}

func leafLockWait(spins *int) {
	*spins++
	if *spins%16 == 0 {
		if *spins == 1024 || runtime.GOMAXPROCS(0) == 1 {
			runtime.Gosched()
			*spins = 0
		} else {
			leafOSYield()
		}
	}
}
