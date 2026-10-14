//go:build amd64 && !race

package wormhole

import "unsafe"

// prefetchStringSplit hints string bodies before a full-leaf split sort.
// The first key's size is only a cost heuristic; mixed short and empty keys
// remain valid for the non-faulting hints.
func (l *leaf[K, V]) prefetchStringSplit() {
	if isStringKey[K]() && l.n-l.sorted > 1 && len(stringKey(l.at(0).key)) > 8 {
		for _, slot := range l.ss[:l.n] {
			prefetchWriteKey(unsafe.StringData(stringKey(l.recordAtSlot(int(slot)).key)))
		}
	}
}
