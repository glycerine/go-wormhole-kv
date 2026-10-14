package wormhole

import "unsafe"

const numericRouteLocalLeaves = 16

// numericRoutePage uses compact page-local IDs while a page refers to at most
// numericRouteLocalLeaves distinct leaves. A page with more fanout promotes to
// direct pointers so routing remains exact for every split history.
type numericRoutePage struct {
	direct *[256]unsafe.Pointer
	leaves [numericRouteLocalLeaves]unsafe.Pointer
	refs   [numericRouteLocalLeaves]uint16
	cells  [256]uint8 // one-based index into leaves; zero means uncached
}

func newNumericRoutePage(l unsafe.Pointer) *numericRoutePage {
	p := new(numericRoutePage)
	if l != nil {
		p.leaves[0] = l
		p.refs[0] = 256
		for i := range p.cells {
			p.cells[i] = 1
		}
	}
	return p
}

func (p *numericRoutePage) lookup(cell byte) unsafe.Pointer {
	if p.direct != nil {
		return p.direct[cell]
	}
	id := p.cells[cell]
	if id == 0 {
		return nil
	}
	return p.leaves[id-1]
}

func (p *numericRoutePage) promote() {
	direct := new([256]unsafe.Pointer)
	for i, id := range p.cells {
		if id != 0 {
			direct[i] = p.leaves[id-1]
		}
	}
	p.direct = direct
	for i := range p.leaves {
		p.leaves[i] = nil
		p.refs[i] = 0
	}
}

func (p *numericRoutePage) localID(l unsafe.Pointer) uint8 {
	for i, candidate := range p.leaves {
		if p.refs[i] != 0 && candidate == l {
			return uint8(i + 1)
		}
	}
	for i, refs := range p.refs {
		if refs == 0 {
			p.leaves[i] = l
			return uint8(i + 1)
		}
	}
	return 0
}

func (p *numericRoutePage) fill(start, end int, l unsafe.Pointer) {
	if p.direct != nil {
		for i := start; i < end; i++ {
			p.direct[i] = l
		}
		return
	}
	id := uint8(0)
	if l != nil {
		id = p.localID(l)
		if id == 0 {
			p.promote()
			p.fill(start, end, l)
			return
		}
	}
	for i := start; i < end; i++ {
		old := p.cells[i]
		if old == id {
			continue
		}
		if old != 0 {
			oldIndex := old - 1
			p.refs[oldIndex]--
			if p.refs[oldIndex] == 0 {
				p.leaves[oldIndex] = nil
			}
		}
		p.cells[i] = id
		if id != 0 {
			p.refs[id-1]++
		}
	}
}

// numericRoutes caches leaves for complete two-byte routing prefixes. A nil
// leaf marks a prefix containing a separator and uses trie routing instead.
// Whole first-byte pages need one pointer; partial pages are allocated lazily.
// Like the prefix table, this structure is protected by metadata epochs.
type numericRoutes struct {
	uniform [256]unsafe.Pointer
	pages   [256]*numericRoutePage
}

func newNumericRoutes(first unsafe.Pointer) *numericRoutes {
	d := new(numericRoutes)
	for i := range d.uniform {
		d.uniform[i] = first
	}
	return d
}

func (d *numericRoutes) lookup(key string) unsafe.Pointer {
	if page := d.pages[key[0]]; page != nil {
		return page.lookup(key[1])
	}
	return d.uniform[key[0]]
}

func (d *numericRoutes) page(index int) *numericRoutePage {
	if page := d.pages[index]; page != nil {
		return page
	}
	page := newNumericRoutePage(d.uniform[index])
	d.pages[index] = page
	return page
}

func (d *numericRoutes) fill(start, end int, l unsafe.Pointer) {
	for start < end {
		index := start >> 8
		if start&255 == 0 && end-start >= 256 {
			d.uniform[index] = l
			d.pages[index] = nil
			start += 256
			continue
		}
		stop := min(end, (index+1)*256)
		localStart := start & 255
		localEnd := localStart + stop - start
		d.page(index).fill(localStart, localEnd, l)
		start = stop
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
func (d *numericRoutes) assign(anchor, next string, l unsafe.Pointer) {
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
		cell := index & 255
		d.page(index>>8).fill(cell, cell+1, nil)
	}
	if len(next) > 2 {
		index := numericPrefixIndex(next)
		cell := index & 255
		d.page(index>>8).fill(cell, cell+1, nil)
	}
}
