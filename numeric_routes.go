package wormhole

import "cmp"

// numericRoutes caches leaves for complete two-byte routing prefixes. A nil
// entry marks a prefix containing a separator and uses trie routing instead.
// Whole first-byte pages need only one pointer; partial pages are allocated
// lazily. Like the prefix table, this structure is protected by metadata epochs.
type numericRoutes[K cmp.Ordered, V any] struct {
	uniform [256]*leaf[K, V]
	pages   [256]*[256]*leaf[K, V]
}

func newNumericRoutes[K cmp.Ordered, V any](first *leaf[K, V]) *numericRoutes[K, V] {
	d := new(numericRoutes[K, V])
	for i := range d.uniform {
		d.uniform[i] = first
	}
	return d
}

func (d *numericRoutes[K, V]) lookup(key string) *leaf[K, V] {
	if page := d.pages[key[0]]; page != nil {
		return page[key[1]]
	}
	return d.uniform[key[0]]
}

func (d *numericRoutes[K, V]) page(index int) *[256]*leaf[K, V] {
	if page := d.pages[index]; page != nil {
		return page
	}
	page := new([256]*leaf[K, V])
	for i := range page {
		page[i] = d.uniform[index]
	}
	d.pages[index] = page
	return page
}

func (d *numericRoutes[K, V]) fill(start, end int, l *leaf[K, V]) {
	for start < end {
		index := start >> 8
		if start&255 == 0 && end-start >= 256 {
			d.uniform[index] = l
			d.pages[index] = nil
			start += 256
			continue
		}
		page := d.page(index)
		stop := min(end, (index+1)*256)
		for ; start < stop; start++ {
			page[start&255] = l
		}
	}
}

func numericPrefixIndex(anchor string) int {
	if len(anchor) == 0 {
		return 0
	}
	index := int(anchor[0]) << 8
	if len(anchor) > 1 {
		index |= int(anchor[1])
	}
	return index
}

// assign replaces the leaf covering [anchor,next). Cells containing a boundary
// longer than two bytes remain uncached. Updates touch at most 256 whole-page
// descriptors plus two partial pages, even for unbounded sequential inserts.
func (d *numericRoutes[K, V]) assign(anchor, next string, l *leaf[K, V]) {
	start := numericPrefixIndex(anchor)
	end := 1 << 16
	if next != "" {
		end = numericPrefixIndex(next)
	}
	if len(anchor) > 2 {
		start++
	}
	d.fill(start, end, l)
	if len(anchor) > 2 {
		index := numericPrefixIndex(anchor)
		d.page(index >> 8)[index&255] = nil
	}
	if len(next) > 2 {
		index := numericPrefixIndex(next)
		d.page(index >> 8)[index&255] = nil
	}
}
