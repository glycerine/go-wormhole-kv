// Package benchworkload reproduces concbench.c's corpus and lib.c's probe RNG.
package benchworkload

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"math/bits"
	"strings"
)

// ReadWords preserves concbench.c's getline behavior, including removing the
// last byte of an unterminated final line and retaining CR on CRLF input.
func ReadWords(r io.Reader) ([]string, error) {
	reader := bufio.NewReader(r)
	var words []string
	for {
		line, err := reader.ReadString('\n')
		if len(line) != 0 {
			if strings.IndexByte(line, 0) >= 0 {
				return nil, fmt.Errorf("dictionary contains a NUL byte")
			}
			words = append(words, line[:len(line)-1])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("empty dictionary")
	}
	return words, nil
}

// libcRandom is glibc's default unseeded random(): degree 31, separation 3,
// initialized by the Park-Miller recurrence with seed 1 and 310 warmup draws.
type libcRandom struct {
	state [31]uint32
	i, j  int
}

func newLibcRandom() *libcRandom {
	r := &libcRandom{i: 3}
	r.state[0] = 1
	for i := 1; i < len(r.state); i++ {
		r.state[i] = uint32(uint64(r.state[i-1]) * 16807 % 2147483647)
	}
	for range 310 {
		r.next()
	}
	return r
}

func (r *libcRandom) next() uint32 {
	r.state[r.i] += r.state[r.j]
	v := r.state[r.i] >> 1
	r.i = (r.i + 1) % 31
	r.j = (r.j + 1) % 31
	return v
}

// Corpus generates the six-word keys used by native concbench on glibc Linux.
// The SHA256 fingerprint covers the keys in generation order, each followed
// by a newline; dictionary words must not contain newlines or NUL bytes.
func Corpus(words []string, n int) ([]string, string, error) {
	if len(words) == 0 || n <= 0 {
		return nil, "", fmt.Errorf("positive key count and nonempty dictionary required")
	}
	for _, word := range words {
		if strings.ContainsAny(word, "\n\x00") {
			return nil, "", fmt.Errorf("invalid dictionary word")
		}
	}
	r := newLibcRandom()
	keys := make([]string, n)
	hash := sha256.New()
	out := bufio.NewWriter(hash)
	for i := range keys {
		var b strings.Builder
		for j := range 6 {
			if j != 0 {
				b.WriteByte(' ')
			}
			b.WriteString(words[int(r.next())%len(words)])
		}
		b.WriteByte('!')
		keys[i] = b.String()
		out.WriteString(keys[i])
		out.WriteByte('\n')
	}
	out.Flush() // hash.Hash writes cannot fail
	return keys, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Lehmer is the 128-bit multiplicative generator from lib.c. Each native new
// pthread starts with this default state. Native worker zero instead inherits
// its caller's state, which was seeded from the clock during loading.
type Lehmer struct{ lo, hi uint64 }

func NewLehmer() Lehmer { return Lehmer{4294967291, 1549556881} }

// Seed matches srandom_u64, used by native worker zero during loading.
func (r *Lehmer) Seed(seed uint64) {
	r.lo, r.hi = seed|1, ^seed
	r.Next()
}

func (r *Lehmer) Next() uint64 {
	const multiplier = uint64(0xda942042e4dd58b5)
	v := r.hi
	carry, lo := bits.Mul64(r.lo, multiplier)
	r.lo, r.hi = lo, r.hi*multiplier+carry
	return v
}
