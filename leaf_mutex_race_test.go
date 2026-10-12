//go:build race

package wormhole

import (
	"bytes"
	"os"
	"os/exec"
	"sync"
	"testing"
)

// Reader bookkeeping must not create a happens-before edge between readers.
// The intentionally racy child must be diagnosed even though both callers
// hold a read lock; the normal suite separately checks valid protected access.
func TestLeafMutexRaceDoesNotSynchronizeReaders(t *testing.T) {
	if os.Getenv("WORMHOLE_READ_RACE_CHILD") == "1" {
		var m leafRWMutex
		var value int
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for range 10000 {
					m.RLock()
					value++
					m.RUnlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		if value == 0 {
			t.Fatal("child did not execute")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeafMutexRaceDoesNotSynchronizeReaders$", "-test.count=1")
	cmd.Env = append(os.Environ(), "WORMHOLE_READ_RACE_CHILD=1", "GORACE=halt_on_error=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("DATA RACE")) {
		t.Fatalf("reader race was not diagnosed: err=%v\n%s", err, out)
	}
}
