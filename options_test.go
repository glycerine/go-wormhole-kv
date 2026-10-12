package wormhole

import "testing"

func TestMetadataCacheDefaultsOff(t *testing.T) {
	defaultMap := New[uint64, uint64]()
	zeroOptions := NewWithOptions[uint64, uint64](Options{})
	cached := NewWithOptions[uint64, uint64](Options{MetaCache: true})
	for i := range defaultMap.tables {
		for _, m := range []*Map[uint64, uint64]{defaultMap, zeroOptions} {
			if m.tables[i].second != nil {
				t.Fatal("default construction allocated the optional metadata cache")
			}
			if len(m.tables[i].buckets) >= len(cached.tables[i].buckets) {
				t.Fatal("default construction must use the smaller initial reservation")
			}
		}
		if cached.tables[i].second == nil {
			t.Fatal("MetaCache=true did not enable the metadata cache")
		}
		for _, m := range []*Map[uint64, uint64]{defaultMap, zeroOptions, cached} {
			if m.tables[i].routes == nil {
				t.Fatal("numeric leaf-route cache must remain enabled")
			}
		}
	}
}
