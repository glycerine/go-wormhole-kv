package wormhole

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

type pointHistory struct {
	kind       int // 0 put, 1 get, 2 delete, 3 probe
	value      string
	present    bool
	start, end uint64
}

// Check all serializations permitted by real-time precedence. This is an
// independent single-key specification, not a replay in completion order:
// overlapping operations may linearize in either order.
func historyLinearizable(history []pointHistory) bool {
	type state struct {
		done    uint64
		value   string
		present bool
	}
	prereq := make([]uint64, len(history))
	for i, op := range history {
		for j, other := range history {
			if other.end < op.start {
				prereq[i] |= uint64(1) << j
			}
		}
	}
	failed := map[state]bool{}
	all := uint64(1)<<len(history) - 1
	var visit func(state) bool
	visit = func(s state) bool {
		if s.done == all {
			return true
		}
		if failed[s] {
			return false
		}
		for i, op := range history {
			bit := uint64(1) << i
			if s.done&bit != 0 || s.done&prereq[i] != prereq[i] {
				continue
			}
			next := s
			next.done |= bit
			switch op.kind {
			case 0:
				next.value, next.present = op.value, true
			case 1:
				if op.present != s.present || op.value != s.value {
					continue
				}
			case 2:
				if op.present != s.present {
					continue
				}
				next.value, next.present = "", false
			case 3:
				if op.present != s.present {
					continue
				}
			}
			if visit(next) {
				return true
			}
		}
		failed[s] = true
		return false
	}
	return visit(state{})
}

func TestHistoryChecker(t *testing.T) {
	t.Run("given an impossible completed read when checking serializations then reject it", func(t *testing.T) {
		if historyLinearizable([]pointHistory{{kind: 0, value: "v", start: 1, end: 2}, {kind: 1, start: 3, end: 4}}) {
			t.Fatal("accepted absent read after completed put")
		}
		if !historyLinearizable([]pointHistory{{kind: 0, value: "v", start: 1, end: 4}, {kind: 1, start: 2, end: 3}}) {
			t.Fatal("rejected overlapping read before put")
		}
	})
}

func TestMapConcurrentHistories(t *testing.T) {
	t.Run("given shared key callers when writes reads and deletes overlap then admit a legal real-time serialization", func(t *testing.T) {
		for seed := uint64(0); seed < 100; seed++ {
			checkConcurrentPointHistory(t, seed, false)
		}
	})
}

func TestMapConcurrentStructuralHistories(t *testing.T) {
	t.Run("given neighboring leaves splitting and merging when shared-key operations overlap then admit a legal real-time serialization", func(t *testing.T) {
		for seed := uint64(0); seed < 100; seed++ {
			checkConcurrentPointHistory(t, seed, true)
		}
	})
}

func checkConcurrentPointHistory(t *testing.T, seed uint64, structural bool) {
	t.Helper()
	m := New[string, string]()
	var clock atomic.Uint64
	var wg sync.WaitGroup
	var history [19]pointHistory
	start := make(chan struct{})
	var churn sync.WaitGroup
	var phases [6]chan struct{}
	var phaseDone [6]sync.WaitGroup
	if structural {
		for i := range phases {
			phases[i] = make(chan struct{})
			phaseDone[i].Add(3)
		}
		churn.Add(1)
		go func() {
			defer churn.Done()
			r := m.Ref()
			defer r.Close()
			<-start
			// Put noise on both sides of shared, so it crosses changing
			// leaf intervals. Removing all noise forces merges back to one
			// leaf. Gate each point-operation step on a new full-leaf cycle,
			// so the history cannot finish before structural churn starts.
			// Every cycle uses production publication/grace waits.
			for cycle := 0; cycle < 6; cycle++ {
				for i := 0; i < 256; i++ {
					if i == 128 {
						close(phases[cycle])
					}
					key := fmt.Sprintf("%c%04d", "az"[i%2], i)
					if err := r.Put(key, "noise"); err != nil {
						t.Error(err)
					}
					if i%16 == 0 {
						runtime.Gosched()
					}
				}
				for i := 0; i < 256; i++ {
					key := fmt.Sprintf("%c%04d", "az"[i%2], i)
					if !r.Delete(key) {
						t.Errorf("seed %d: noise disappeared", seed)
					}
					if i%16 == 0 {
						runtime.Gosched()
					}
				}
				phaseDone[cycle].Wait()
			}
		}()
	}
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			r := m.Ref()
			defer r.Close()
			rng := rand.New(rand.NewPCG(seed, uint64(worker)))
			<-start
			for step := 0; step < 6; step++ {
				if structural {
					<-phases[step]
				}
				op := &history[worker*6+step]
				op.kind = rng.IntN(4)
				op.start = clock.Add(1)
				runtime.Gosched()
				switch op.kind {
				case 0:
					op.value = fmt.Sprintf("%d-%d", worker, step)
					if err := r.Put("shared", op.value); err != nil {
						t.Error(err)
					}
				case 1:
					op.value, op.present = r.Get("shared")
				case 2:
					op.present = r.Delete("shared")
				case 3:
					op.present = r.Probe("shared")
				}
				op.end = clock.Add(1)
				if structural {
					phaseDone[step].Done()
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	churn.Wait()
	final := &history[18]
	final.kind, final.start = 1, clock.Add(1)
	r := m.Ref()
	final.value, final.present = r.Get("shared")
	r.Close()
	final.end = clock.Add(1)
	if !historyLinearizable(history[:]) {
		t.Fatalf("seed %d has no legal serialization: %+v", seed, history)
	}
	if structural {
		if m.current.Load().version < 13 {
			t.Fatalf("seed %d: structural churn did not publish enough versions", seed)
		}
		if m.first.next != nil {
			t.Fatalf("seed %d: noise removal did not merge leaves", seed)
		}
		want := map[string]string{}
		if final.present {
			want["shared"] = final.value
		}
		checkMap(t, m, want)
	}
}
