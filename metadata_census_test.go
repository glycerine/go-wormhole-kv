package wormhole

import (
	"encoding/binary"
	"encoding/json"
	"math/bits"
	"os"
	"testing"

	"github.com/glycerine/go-wormhole-kv/internal/benchworkload"
)

type metadataCandidateCensus struct {
	Queries             uint64
	Probes              uint64
	PositiveHits        uint64
	RootFinal           uint64
	ShortFinal          uint64
	FinalCandidateExact uint64
	FinalCandidateOther uint64
	ExactRetry          uint64
	PossibleEarlyHints  uint64
	UsefulEarlyHints    uint64
	UsefulNonShortHints uint64
	RemainingProbes     map[int]uint64
}

// This is an untimed replay of optimistic fingerprint search, not a benchmark.
// Original get validates its final candidate. Single-threaded diagnostics keep
// the metadata table stable while inspecting it before each ordinary Put.
func censusMetadataCandidate(t *testing.T, table *metaTable[string, string], key string, stats *metadataCandidateCensus) {
	t.Helper()
	stats.Queries++
	gap := min(table.maxPrefix, len(key)) + 1
	lo, h := 0, crcSeed
	steps, lastPositive := 0, 0
	lastEarly := false
	var candidate *metadata[string, string]
	for gap > 1 {
		steps++
		stats.Probes++
		inc := gap / 2
		if gap > 7 {
			inc = gap >> 3 << 2
		}
		hash := crcExtend(h, key[lo:lo+inc])
		a, b := table.choices(hash)
		tag := hashTag(hash)
		var hit *metadata[string, string]
		for _, bucket := range [2]uint32{a, b} {
			if mask := matchTagMask(&table.tags[bucket], tag); mask != 0 {
				hit = table.buckets[bucket][bits.TrailingZeros32(mask)>>1]
				if hit == nil {
					t.Fatal("fingerprint points to an empty metadata slot")
				}
				break
			}
		}
		if (hit != nil) != table.peek(hash) {
			t.Fatal("census fingerprint search disagrees with original peek")
		}
		if hit != nil {
			stats.PositiveHits++
			candidate = hit
			lo, h = lo+inc, hash
			gap -= inc
			lastPositive = steps
			lastEarly = gap > 1
			if lastEarly {
				stats.PossibleEarlyHints++
			}
		} else {
			gap = inc
		}
	}
	exact := table.get(key[:lo], h)
	if exact == nil {
		stats.ExactRetry++
		return
	}
	if lo == 0 {
		stats.RootFinal++
		return
	}
	if lo <= 1 {
		stats.ShortFinal++
	}
	if candidate == exact {
		stats.FinalCandidateExact++
		if stats.RemainingProbes == nil {
			stats.RemainingProbes = make(map[int]uint64)
		}
		stats.RemainingProbes[steps-lastPositive]++
		if lastEarly {
			stats.UsefulEarlyHints++
			if lo > 1 {
				stats.UsefulNonShortHints++
			}
		}
	} else {
		stats.FinalCandidateOther++
	}
}

func TestMetadataCandidateCensus(t *testing.T) {
	path := os.Getenv("WORMHOLE_METADATA_CENSUS_CORPUS")
	if path == "" {
		t.Skip("diagnostic corpus required")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	words, err := benchworkload.ReadWords(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	keys, sha, err := benchworkload.Corpus(words, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, initial, fresh []string, fingerprint string) {
		m := New[string, string]()
		r := m.Ref()
		defer r.Close()
		phases := make(map[string]*metadataCandidateCensus)
		load := func(name string, keys []string) {
			stats := new(metadataCandidateCensus)
			phases[name] = stats
			for _, key := range keys {
				censusMetadataCandidate(t, m.current.Load().table, key, stats)
				if err := r.PutKey(PrepareKey(key), ""); err != nil {
					t.Fatal(err)
				}
			}
		}
		load("prospective_initial_load", initial)
		query := func(name string, populations ...[]string) {
			stats := new(metadataCandidateCensus)
			phases[name] = stats
			table := m.current.Load().table
			for _, population := range populations {
				for _, key := range population {
					censusMetadataCandidate(t, table, key, stats)
				}
			}
		}
		if fresh != nil {
			query("initial_map_queries", initial)
			load("prospective_fresh_put", fresh)
			query("grown_map_queries", initial, fresh)
		} else {
			query("full_map_queries", initial)
		}
		if m.Len() != int64(len(initial)+len(fresh)) {
			t.Fatal("incorrect loaded count")
		}
		table := m.current.Load().table
		data, err := json.Marshal(map[string]any{
			"dataset": name, "keys": m.Len(), "sha256": fingerprint,
			"meta_cache": false, "buckets": len(table.tags),
			"max_prefix": table.maxPrefix, "phases": phases,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("metadata-candidate-census: %s", data)
	}
	run("concbench million words", keys, nil, sha)

	// Same reversible permutation and even/odd keys as make bench string mode.
	const n = 1 << 16
	initial, fresh := make([]string, n), make([]string, n)
	for i := range initial {
		const mask = uint64(1<<63 - 1)
		x := uint64(i) & mask
		x ^= x >> 30
		x = x * 0x3f58476d1ce4e5b9 & mask
		x ^= x >> 27
		x = x * 0x14d049bb133111eb & mask
		x ^= x >> 31
		x <<= 1
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], x)
		initial[i] = string(key[:])
		binary.BigEndian.PutUint64(key[:], x|1)
		fresh[i] = string(key[:])
	}
	run("make bench binary strings", initial, fresh, "")
}
