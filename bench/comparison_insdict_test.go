// Adapted from github.com/glycerine/bufftree/bench.
// Copyright (c) 2026 Jason E. Aten, Ph.D. MIT license: BUFFTREE_BENCH_LICENSE.

package bench

import (
	"cmp"

	"github.com/glycerine/insdict"
)

// Give Dict the same initial capacity hint as the built-in map. Ascend's
// sorted index stays lazy, so scan timings include preparation when needed.
type comparisonInsdict[K cmp.Ordered, V any] struct{ *insdict.Dict[K, V] }

func newComparisonInsdict[K cmp.Ordered, V any](n int) *comparisonInsdict[K, V] {
	return &comparisonInsdict[K, V]{insdict.NewDictSize[K, V](n)}
}
func (m *comparisonInsdict[K, V]) Put(k K, v V) { m.Dict.Put(k, v) }
func (m *comparisonInsdict[K, V]) Scan(start K, length int, visit func(K, V) bool) {
	if length <= 0 {
		return
	}
	m.Ascend(start)(func(k K, v V) bool {
		length--
		return visit(k, v) && length > 0
	})
}
