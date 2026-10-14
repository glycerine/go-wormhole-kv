// concbench reproduces the native benchmark's corpus, configurable load workers,
// random-probe pipeline, 10,000-operation batches and three timed rounds.
package main

import (
	"cmp"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"sync"
	"time"

	wormhole "github.com/glycerine/go-wormhole-kv"
	"github.com/glycerine/go-wormhole-kv/internal/benchworkload"
	"github.com/glycerine/go-wormhole-kv/internal/cpuaffinity"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	pinWorkers := flag.Bool("pin", runtime.GOOS == "linux" || runtime.GOOS == "darwin" && runtime.GOARCH == "amd64", "pin workers using OS affinity (unavailable on Darwin/arm64)")
	profile := flag.String("cpuprofile", "", "write CPU profile of probe rounds")
	loadOnly := flag.Bool("load-only", false, "exit after loading, for external write profiling")
	loadProfile := flag.String("loadprofile", "", "write load CPU/mutex/block profiles with this path prefix")
	loadLatency := flag.Bool("load-latency", false, "time every Put during load and report merged latency quantiles (clock overhead included)")
	loadWorkers := flag.Int("load-workers", 0, "number of load workers (defaults to the positional threads argument)")
	hotKeys := flag.Int("hot-keys", 0, "diagnostic: repeat the first N corpus keys in the operation trace (0 disables)")
	loadWorkload := flag.String("load-workload", "fresh", "fresh, update (preloaded), or ascending (sorted corpus)")
	duration := flag.Duration("duration", 3*time.Second, "probe round duration")
	rounds := flag.Int("rounds", 3, "number of probe rounds")
	prefetch := flag.Bool("prefetch", true, "prefetch upcoming query bytes and samples as native does (amd64)")
	metaCache := flag.Bool("meta", false, "use memory hungry metadata-cache for a tiny bit of read improvement")

	flag.Parse()
	if flag.NArg() != 3 {
		return fmt.Errorf("usage: concbench [flags] words-file keys threads")
	}
	n, err := strconv.Atoi(flag.Arg(1))
	if err != nil || n <= 0 {
		return fmt.Errorf("invalid key count")
	}
	threads, err := strconv.Atoi(flag.Arg(2))
	if err != nil || threads <= 0 {
		return fmt.Errorf("invalid thread count")
	}
	loadWorkersSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "load-workers" {
			loadWorkersSet = true
		}
	})
	if !loadWorkersSet {
		*loadWorkers = threads
	}
	if *duration <= 0 || *rounds <= 0 {
		return fmt.Errorf("positive duration and rounds required")
	}
	if *loadWorkers <= 0 || *loadWorkload != "fresh" && *loadWorkload != "update" && *loadWorkload != "ascending" {
		return fmt.Errorf("positive load-workers and fresh/update/ascending load-workload required")
	}
	if *hotKeys < 0 || *hotKeys > n || *hotKeys > 0 && *loadWorkload == "ascending" {
		return fmt.Errorf("hot-keys must be between 0 and keys and cannot be used with ascending")
	}
	cpus := []int{0}
	pin := func(int) (func() error, error) {
		return func() error { return nil }, nil
	}
	if *pinWorkers {
		cpus, err = cpuaffinity.CPUs()
		if err != nil {
			return err
		}
		pin = cpuaffinity.Pin
		fmt.Printf("worker CPUs=%v (worker i uses CPUs[i %% %d], verified after pinning)\n", cpus, len(cpus))
	} else {
		fmt.Println("worker affinity disabled; OS scheduler chooses CPUs")
	}
	runtime.GOMAXPROCS(max(*loadWorkers, threads))
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		return err
	}
	words, err := benchworkload.ReadWords(f)
	f.Close()
	if err != nil {
		return err
	}
	texts, fingerprint, err := benchworkload.Corpus(words, n)
	if err != nil {
		return err
	}
	keys := make([]wormhole.Key[string], n)
	for i, text := range texts {
		keys[i] = wormhole.PrepareKey(text)
	}
	if *loadWorkload == "ascending" {
		slices.SortFunc(keys, compareKeys)
	}
	wantKeys := n
	if *hotKeys > 0 {
		wantKeys = *hotKeys
		for i := *hotKeys; i < len(keys); i++ {
			keys[i] = keys[i%*hotKeys]
		}
	}
	texts, words = nil, nil
	// Native frees the corpus-builder dictionary before timing the load.
	// Collect those Go fixtures here rather than charging their GC to writes.
	runtime.GC()
	fmt.Printf("corpus keys=%d sha256=%s\n", n, fingerprint)
	fmt.Printf("Go %s GOMAXPROCS=%d; prepared keys, automatic epoch parking\n", runtime.Version(), runtime.GOMAXPROCS(0))
	fmt.Printf("workload prefetch=%v\n", *prefetch)
	fmt.Printf("use metadata-cache=%v\n", *metaCache)
	if *hotKeys > 0 {
		fmt.Printf("diagnostic trace repeats the first %d prepared corpus keys; distinct=%d\n", *hotKeys, wantKeys)
	}
	// Native concbench stores zero-length values and probes only for key
	// presence. Use a zero-size Go value so both drivers measure a key set.

	m := wormhole.NewWithOptions[string, struct{}](wormhole.Options{MetaCache: *metaCache})

	if *loadWorkload == "update" {
		r := m.Ref()
		for _, key := range keys {
			if err := r.PutKey(key, struct{}{}); err != nil {
				r.Close()
				return err
			}
		}
		r.Close()
		runtime.GC()
	}
	var latencies []int64
	if *loadLatency {
		latencies = make([]int64, n)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	stopProfile, err := profileLoad(*loadProfile)
	if err != nil {
		return err
	}
	start := time.Now()
	for worker := range *loadWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			restore, err := pin(cpus[worker%len(cpus)])
			if err != nil {
				errors <- err
				return
			}
			defer func() {
				if err := restore(); err != nil {
					errors <- err
				}
			}()
			r := m.Ref()
			defer r.Close()
			lo, hi := n / *loadWorkers * worker, n / *loadWorkers * (worker+1)
			if worker == *loadWorkers-1 {
				hi = n
			}
			fmt.Printf("load worker %d %d\n", lo, hi)
			if *loadLatency {
				for i, k := range keys[lo:hi] {
					before := time.Now()
					if err := r.PutKey(k, struct{}{}); err != nil {
						errors <- err
						return
					}
					latencies[lo+i] = time.Since(before).Nanoseconds()
				}
			} else {
				for _, k := range keys[lo:hi] {
					if err := r.PutKey(k, struct{}{}); err != nil {
						errors <- err
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	fmt.Printf("load x%d %.2f mops count=%d dt_ns=%d clocked=%v workload=%s\n", *loadWorkers, float64(n)/elapsed.Seconds()/1e6, n, elapsed.Nanoseconds(), *loadLatency, *loadWorkload)
	if err := stopProfile(); err != nil {
		return err
	}
	close(errors)
	for err := range errors {
		return err
	}
	if m.Len() != int64(wantKeys) {
		return fmt.Errorf("load size: got %d want %d", m.Len(), wantKeys)
	}
	if *loadLatency {
		slices.Sort(latencies)
		quantile := func(p int) int64 { return latencies[(len(latencies)-1)*p/1000] }
		fmt.Printf("load latency ns count=%d p50=%d p95=%d p99=%d p999=%d max=%d (clock overhead included)\n", n, quantile(500), quantile(950), quantile(990), quantile(999), latencies[n-1])
		restore, err := pin(cpus[0])
		if err != nil {
			return err
		}
		control := latencies[:min(n, 100000)]
		before := time.Now()
		for i := range control {
			start := time.Now()
			control[i] = time.Since(start).Nanoseconds()
		}
		controlElapsed := time.Since(before)
		if err := restore(); err != nil {
			return err
		}
		slices.Sort(control)
		fmt.Printf("clock control affinity=%v cpu-label=%d count=%d ns/pair=%.2f p50=%d p99=%d (not subtracted)\n", *pinWorkers, cpus[0], len(control), float64(controlElapsed.Nanoseconds())/float64(len(control)), control[(len(control)-1)/2], control[(len(control)-1)*99/100])
	}
	if *loadOnly {
		return nil
	}
	// Collect corpus-builder garbage before the read-only measurement. Automatic
	// GC remains enabled throughout both load and probe phases.
	runtime.GC()
	if *profile != "" {
		file, err := os.Create(*profile)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := pprof.StartCPUProfile(file); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	fmt.Printf("probe with %d threads. each round takes %s\n", threads, *duration)
	master := benchworkload.NewLehmer()
	master.Seed(uint64(time.Now().UnixNano()))
	for round := 0; round < *rounds; round++ {
		counts, misses := make([]uint64, threads), make([]uint64, threads)
		pinErrors := make(chan error, threads)
		start = time.Now()
		end := start.Add(*duration)
		for worker := range threads {
			wg.Add(1)
			go func() {
				defer wg.Done()
				restore, err := pin(cpus[worker%len(cpus)])
				if err != nil {
					pinErrors <- err
					return
				}
				defer func() {
					if err := restore(); err != nil {
						pinErrors <- err
					}
				}()
				r := m.Ref()
				defer r.Close()
				rng := benchworkload.NewLehmer()
				if worker == 0 {
					rng = master
				}
				next := keys[rng.Next()%uint64(n)]
				rnext := rng.Next() % uint64(n)
				var count, failed uint64
				for {
					for range 10000 {
						key := next
						next = keys[rnext]
						rnext = rng.Next() % uint64(n)
						if *prefetch {
							prefetchProbe(next.String(), &keys[rnext])
						}
						if !r.ProbeKey(key) {
							failed++
						}
					}
					count += 10000
					if !time.Now().Before(end) {
						break
					}
				}
				counts[worker], misses[worker] = count, failed
				if worker == 0 {
					master = rng
				}
			}()
		}
		wg.Wait()
		elapsed := time.Since(start)
		close(pinErrors)
		for err := range pinErrors {
			return err
		}
		var count, failed uint64
		for i := range counts {
			count += counts[i]
			failed += misses[i]
		}
		fmt.Printf("probe x%d %.2f mops count=%d misses=%d\n", threads, float64(count)/elapsed.Seconds()/1e6, count, failed)
		if failed != 0 {
			return fmt.Errorf("probe missed %d loaded keys", failed)
		}
		time.Sleep(time.Second)
	}
	return nil
}

func compareKeys(a, b wormhole.Key[string]) int { return cmp.Compare(a.String(), b.String()) }
