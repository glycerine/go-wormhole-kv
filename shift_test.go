package wormhole

import (
	"fmt"
	"testing"
)

func TestShiftIndices(t *testing.T) {
	t.Run("given every slot interval when shifting either way then repair every live index", func(t *testing.T) {
		var original [leafCapacity]uint8
		for i := range original {
			original[i] = uint8(i * 73 % leafCapacity)
		}
		for _, n := range []int{0, 1, 15, 16, 17, 63, 64, 65, 127, 128} {
			for from := 0; from <= leafCapacity; from++ {
				for count := 0; count <= leafCapacity-from; count++ {
					for _, delta := range []int{-1, 1} {
						// Guards also catch overrun of the rounded final vector.
						guarded := struct {
							before [16]byte
							ss     [leafCapacity]uint8
							after  [16]byte
						}{ss: original}
						shiftIndices(&guarded.ss, n, from, count, delta)
						for i := 0; i < n; i++ {
							want := original[i]
							if int(want) >= from && int(want) < from+count {
								want = uint8(int(want) + delta)
								// Edge wraparound checks the byte arithmetic too.
							}
							if guarded.ss[i] != want {
								t.Fatalf("n=%d from=%d count=%d delta=%d index=%d got=%d want=%d", n, from, count, delta, i, guarded.ss[i], want)
							}
						}
						if guarded.before != [16]byte{} || guarded.after != [16]byte{} {
							t.Fatal("write outside index array")
						}
					}
				}
			}
		}
	})
}

func BenchmarkShiftIndices(b *testing.B) {
	for _, n := range []int{32, 96, 128} {
		for _, impl := range []struct {
			name string
			fn   func(*[leafCapacity]uint8, int, int, int, int)
		}{{"Selected", shiftIndices}, {"Scalar", scalarShiftIndices}} {
			b.Run(fmt.Sprintf("%d/%s", n, impl.name), func(b *testing.B) {
				var ss [leafCapacity]uint8
				for i := range ss {
					ss[i] = uint8(i * 73 % leafCapacity)
				}
				b.ReportAllocs()
				for i := range b.N {
					// Alternate the move with its inverse to avoid index drift.
					if i&1 == 0 {
						impl.fn(&ss, n, 32, 32, 1)
					} else {
						impl.fn(&ss, n, 33, 32, -1)
					}
				}
			})
		}
	}
}
