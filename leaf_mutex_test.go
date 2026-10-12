package wormhole

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeafMutexConcurrent(t *testing.T) {
	for _, procs := range []int{1, 4} {
		old := runtime.GOMAXPROCS(procs)
		var m leafRWMutex
		var count int
		complement := ^count
		var wg sync.WaitGroup
		bad := make(chan struct{}, 20)
		for worker := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 2000; i++ {
					if (i+worker)%5 == 0 {
						m.Lock()
						count++
						complement = ^count
						m.Unlock()
					} else {
						m.RLock()
						ok := count == ^complement
						m.RUnlock()
						if !ok {
							bad <- struct{}{}
							return
						}
					}
				}
			}()
		}
		wg.Wait()
		runtime.GOMAXPROCS(old)
		if count != 8000 || len(bad) != 0 {
			t.Fatalf("P=%d count=%d inconsistent readers=%d", procs, count, len(bad))
		}
	}
}

func TestLeafMutexWriterGatesReaders(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	var m leafRWMutex
	m.RLock()
	writerEntered, releaseWriter, writerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		m.Lock()
		close(writerEntered)
		<-releaseWriter
		m.Unlock()
		close(writerDone)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadUint32(&m.state)&leafWriterBit == 0 {
		if time.Now().After(deadline) {
			t.Fatal("writer did not reserve the leaf")
		}
		runtime.Gosched()
	}
	readerEntered, readerDone := make(chan struct{}), make(chan struct{})
	go func() {
		m.RLock()
		close(readerEntered)
		m.RUnlock()
		close(readerDone)
	}()
	if m.TryLock() {
		t.Fatal("TryLock bypassed an existing reader/pending writer")
	}
	m.RUnlock()
	select {
	case <-writerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter after existing reader left")
	}
	select {
	case <-readerEntered:
		t.Fatal("new reader bypassed reserved writer")
	default:
	}
	close(releaseWriter)
	select {
	case <-readerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not enter after writer left")
	}
	<-writerDone
	<-readerDone
	if !m.TryLock() {
		t.Fatal("TryLock failed on an idle lock")
	}
	m.Unlock()
}

func TestLeafMutexTryLockActiveReaders(t *testing.T) {
	var m leafRWMutex
	m.RLock()
	if m.TryLock() {
		t.Fatal("TryLock entered while a reader was active")
	}
	entered := make(chan struct{})
	go func() { m.RLock(); close(entered); m.RUnlock() }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("failed TryLock left a writer reservation")
	}
	m.RUnlock()
	if !m.TryLock() {
		t.Fatal("TryLock failed after readers left")
	}
	m.Unlock()
}
