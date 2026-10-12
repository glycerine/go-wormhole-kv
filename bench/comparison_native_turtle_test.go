package bench

import (
	"github.com/glycerine/turtle/go"
)

// The benchmark adapter only discards Get's presence bit and Scan's count,
// matching the Tidwall adapter. Keys and values stay native uint64 throughout.
type nativeTurtle struct{ store *turtle.Map[uint64, uint64] }

func newNativeTurtle() *nativeTurtle {
	return &nativeTurtle{store: turtle.NewMap[uint64, uint64](turtleOptions())}
}
func (m *nativeTurtle) Get(k uint64) uint64 { v, _ := m.store.Get(k); return v }
func (m *nativeTurtle) Put(k, v uint64)     { m.store.Put(k, v) }
func (m *nativeTurtle) Len() int            { return m.store.Len() }
func (m *nativeTurtle) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	m.store.Scan(start, length, visit)
}
