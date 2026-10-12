package bench

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	wormhole "github.com/glycerine/go-wormhole-kv"
	"github.com/glycerine/go-wormhole-kv/internal/cpuaffinity"
	"github.com/tidwall/btree"
)

type writer interface {
	Put(string, string)
	Close()
}

type store interface {
	Writer() writer
	Get(string) (string, bool)
	Len() int
}

type wormStore struct{ m *wormhole.Map[string, string] }
type wormWriter struct{ r *wormhole.Ref[string, string] }

func (s *wormStore) Writer() writer              { return &wormWriter{s.m.Ref()} }
func (s *wormStore) Len() int                    { return int(s.m.Len()) }
func (s *wormStore) Get(k string) (string, bool) { r := s.m.Ref(); defer r.Close(); return r.Get(k) }
func (w *wormWriter) Put(k, v string) {
	if err := w.r.Put(k, v); err != nil {
		panic(err)
	}
}
func (w *wormWriter) Close() { w.r.Close() }

type tidwallStore struct {
	mu     sync.RWMutex
	tree   btree.Map[string, string]
	locked bool
}

func (s *tidwallStore) Writer() writer { return s }
func (s *tidwallStore) Put(k, v string) {
	if s.locked {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	s.tree.Set(k, v)
}
func (s *tidwallStore) Get(k string) (string, bool) {
	if s.locked {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	return s.tree.Get(k)
}
func (s *tidwallStore) Len() int {
	if s.locked {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	return s.tree.Len()
}
func (s *tidwallStore) Close() {}

type layout struct {
	name       string
	make       func() store
	concurrent bool
}

var layouts = []layout{
	{"Wormhole", func() store { return &wormStore{wormhole.New[string, string]()} }, true},
	{"Tidwall", func() store { return &tidwallStore{} }, false},
	{"TidwallLocked", func() store { return &tidwallStore{locked: true} }, true},
}

func loadSize() int {
	if s := os.Getenv("WORMHOLE_BENCH_N"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 2 {
			panic("WORMHOLE_BENCH_N must be an integer >= 2")
		}
		return n
	}
	return 1 << 16
}

// Same reversible 63-bit permutation as ~/bufftree/bench/helpers_test.go.
// Even keys are the initial load; adjacent odd keys are guaranteed fresh.
func permutedKey(rank int) uint64 {
	const mask = uint64(1<<63 - 1)
	x := uint64(rank) & mask
	x ^= x >> 30
	x = x * 0x3f58476d1ce4e5b9 & mask
	x ^= x >> 27
	x = x * 0x14d049bb133111eb & mask
	x ^= x >> 31
	return x << 1
}

func binaryKey(x uint64) string {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], x)
	return string(data[:])
}

func writeTrace(n int, scenario string) (loaded, keys []string) {
	keys = make([]string, n)
	if scenario != "Ascending" {
		loaded = make([]string, n)
	}
	for i := range keys {
		x := permutedKey(i)
		switch scenario {
		case "Fresh":
			loaded[i], keys[i] = binaryKey(x), binaryKey(x|1)
		case "Update":
			loaded[i], keys[i] = binaryKey(x), binaryKey(x)
		case "Ascending":
			keys[i] = binaryKey(uint64(i))
		default:
			panic("unknown scenario")
		}
	}
	return
}

func BenchmarkWrites(b *testing.B) {
	for _, scenario := range []string{"Fresh", "Ascending"} {
		loaded, keys := writeTrace(loadSize(), scenario)
		for _, l := range layouts {
			b.Run(scenario+"/"+l.name, func(b *testing.B) { benchmarkWrites(b, l, scenario, loaded, keys, 1, false) })
		}
	}
}

func BenchmarkWritesParallel(b *testing.B) {
	for _, scenario := range []string{"Fresh"} {
		loaded, keys := writeTrace(loadSize(), scenario)
		for _, l := range layouts {
			if !l.concurrent {
				continue
			}
			b.Run(scenario+"/"+l.name, func(b *testing.B) { benchmarkWrites(b, l, scenario, loaded, keys, runtime.GOMAXPROCS(0), false) })
		}
	}
}

// Clock every operation up to a million samples, then uniformly subsample.
// These raw wall-clock quantiles include clock overhead. Throughput should be
// taken from BenchmarkWrites/Parallel, whose loop does not read the clock.
func BenchmarkWriteLatency(b *testing.B) {
	for _, scenario := range []string{"Fresh", "Ascending"} {
		loaded, keys := writeTrace(loadSize(), scenario)
		for _, l := range layouts {
			for _, workers := range []int{1, 4} {
				if workers > 1 && (!l.concurrent || scenario == "Ascending") {
					continue
				}
				b.Run(fmt.Sprintf("%s/%s/Workers%d", scenario, l.name, workers), func(b *testing.B) { benchmarkWrites(b, l, scenario, loaded, keys, workers, true) })
			}
		}
	}
}

func benchmarkWrites(b *testing.B, l layout, scenario string, loaded, keys []string, workers int, latency bool) {
	b.Helper()
	b.StopTimer()
	cpus, err := cpuaffinity.CPUs()
	if err != nil {
		b.Fatal(err)
	}
	assigned := make([]int, workers)
	for i := range assigned {
		assigned[i] = cpus[i%len(cpus)]
	}
	b.Logf("worker CPU assignments (verified on pin): %v", assigned)
	if workers == 1 {
		restore, err := cpuaffinity.Pin(assigned[0])
		if err != nil {
			b.Fatal(err)
		}
		defer func() {
			b.StopTimer()
			if err := restore(); err != nil {
				b.Fatal(err)
			}
		}()
	}
	background := context.Background()
	timed := pprof.WithLabels(background, pprof.Labels("phase", "write", "container", l.name, "scenario", scenario))
	defer pprof.SetGoroutineLabels(background)
	stride := max(1, (b.N+(1<<20)-1)/(1<<20))
	var samples []int64
	if latency {
		samples = make([]int64, (b.N+stride-1)/stride)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for done := 0; done < b.N; {
		pprof.SetGoroutineLabels(background)
		s := l.make()
		loader := s.Writer()
		for _, k := range loaded {
			loader.Put(k, "initial")
		}
		loader.Close()
		count := min(len(keys), b.N-done)
		handles := make([]writer, workers)
		for i := range handles {
			handles[i] = s.Writer()
		}
		write := func(worker int) {
			h := handles[worker]
			lo, hi := count*worker/workers, count*(worker+1)/workers
			for i := lo; i < hi; i++ {
				value := "written-a"
				if i&1 != 0 {
					value = "written-b"
				}
				global := done + i
				if latency && global%stride == 0 {
					start := time.Now()
					h.Put(keys[i], value)
					samples[global/stride] = time.Since(start).Nanoseconds()
				} else {
					h.Put(keys[i], value)
				}
			}
		}
		pprof.SetGoroutineLabels(timed)
		if workers == 1 {
			b.StartTimer()
			write(0)
			b.StopTimer()
		} else {
			ready := make(chan error, workers)
			start, release := make(chan struct{}), make(chan struct{})
			var finished, cleaned sync.WaitGroup
			finished.Add(workers)
			cleaned.Add(workers)
			cleanupErrors := make(chan error, workers)
			for worker := range workers {
				go func() {
					defer cleaned.Done()
					restore, err := cpuaffinity.Pin(assigned[worker])
					ready <- err
					<-start
					if err == nil {
						write(worker)
					}
					finished.Done()
					<-release
					if err == nil {
						cleanupErrors <- restore()
					}
				}()
			}
			var pinErr error
			for range workers {
				if err := <-ready; err != nil {
					pinErr = err
				}
			}
			if pinErr == nil {
				b.StartTimer()
			}
			close(start)
			finished.Wait()
			b.StopTimer()
			close(release)
			cleaned.Wait()
			close(cleanupErrors)
			for err := range cleanupErrors {
				if err != nil {
					b.Fatal(err)
				}
			}
			if pinErr != nil {
				b.Fatal(pinErr)
			}
		}
		pprof.SetGoroutineLabels(background)
		for _, h := range handles {
			h.Close()
		}
		wantLen := len(loaded) + count
		if scenario == "Update" {
			wantLen = len(loaded)
		}
		if s.Len() != wantLen {
			b.Fatalf("count=%d want=%d", s.Len(), wantLen)
		}
		for _, i := range []int{0, count - 1} {
			want := "written-a"
			if i&1 != 0 {
				want = "written-b"
			}
			if v, ok := s.Get(keys[i]); !ok || v != want {
				b.Fatal("write lost", i, v, ok)
			}
		}
		done += count
	}
	if latency {
		slices.Sort(samples)
		for _, p := range []int{50, 95, 99, 100} {
			i := min(len(samples)-1, (len(samples)*p+99)/100-1)
			b.ReportMetric(float64(samples[i]), fmt.Sprintf("p%d-ns", p))
		}
		b.ReportMetric(float64(len(samples)), "samples")
	}
}

func BenchmarkLatencyClock(b *testing.B) {
	b.StopTimer()
	cpus, err := cpuaffinity.CPUs()
	if err != nil {
		b.Fatal(err)
	}
	restore, err := cpuaffinity.Pin(cpus[0])
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		b.StopTimer()
		if err := restore(); err != nil {
			b.Fatal(err)
		}
	}()
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		start := time.Now()
		_ = time.Since(start)
	}
}

func TestWriteAdapters(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.name+" given binary keys when inserting and replacing then match the reference map", func(t *testing.T) {
			s := l.make()
			h := s.Writer()
			defer h.Close()
			want := map[string]string{}
			for i := range 4096 {
				k, v := binaryKey(permutedKey(i%1024)), fmt.Sprint(i)
				h.Put(k, v)
				want[k] = v
			}
			if s.Len() != len(want) {
				t.Fatal("wrong count")
			}
			for k, want := range want {
				if v, ok := s.Get(k); !ok || v != want {
					t.Fatal("wrong value")
				}
			}
		})
		if !l.concurrent {
			continue
		}
		t.Run(l.name+" given four writers when growing shared leaves then retain every write", func(t *testing.T) {
			s := l.make()
			var wg sync.WaitGroup
			for worker := range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					h := s.Writer()
					defer h.Close()
					for i := worker; i < 4000; i += 4 {
						h.Put(binaryKey(permutedKey(i)), fmt.Sprint(i))
					}
				}()
			}
			wg.Wait()
			if s.Len() != 4000 {
				t.Fatal("lost keys")
			}
			for i := range 4000 {
				if v, ok := s.Get(binaryKey(permutedKey(i))); !ok || v != fmt.Sprint(i) {
					t.Fatal("lost value")
				}
			}
		})
	}
}

func TestWriteTraces(t *testing.T) {
	for _, scenario := range []string{"Fresh", "Update", "Ascending"} {
		loaded, keys := writeTrace(4096, scenario)
		seen := map[string]bool{}
		for _, k := range loaded {
			if seen[k] {
				t.Fatal("duplicate loaded key")
			}
			seen[k] = true
		}
		for i, k := range keys {
			if len(k) != 8 {
				t.Fatal("not an eight-byte key")
			}
			if scenario == "Update" {
				if !seen[k] {
					t.Fatal("update is an insert")
				}
			} else {
				if seen[k] {
					t.Fatal("fresh write is a replacement")
				}
				seen[k] = true
			}
			if scenario == "Ascending" && i > 0 && keys[i-1] >= k {
				t.Fatal("not ascending")
			}
		}
	}
}
