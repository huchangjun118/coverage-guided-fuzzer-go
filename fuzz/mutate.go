package fuzz

import "math/rand/v2"

// Mutator produces deterministic mutations of inputs. A Mutator is not
// safe for concurrent use; each worker owns one built from a derived seed.
type Mutator struct {
	rng    *rand.Rand
	maxLen int
}

// newMutator builds a Mutator whose randomness derives from seed.
func newMutator(seed uint64, maxLen int) *Mutator {
	return &Mutator{rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), maxLen: maxLen}
}

// mutate applies a small number of random mutations to a copy of base.
func (m *Mutator) mutate(base []byte) []byte {
	out := make([]byte, len(base))
	copy(out, base)
	if len(out) == 0 {
		out = []byte{0}
	}
	n := 1 + m.rng.IntN(4)
	for i := 0; i < n; i++ {
		out = m.step(out)
	}
	if len(out) > m.maxLen {
		out = out[:m.maxLen]
	}
	return out
}

// step applies one randomly chosen mutation strategy.
func (m *Mutator) step(b []byte) []byte {
	switch m.rng.IntN(6) {
	case 0: // flip a random byte
		b[m.rng.IntN(len(b))] ^= byte(1 << m.rng.IntN(8))
	case 1: // set a random byte
		b[m.rng.IntN(len(b))] = byte(m.rng.IntN(256))
	case 2: // insert a random byte
		i := m.rng.IntN(len(b) + 1)
		b = append(b[:i], append([]byte{byte(m.rng.IntN(256))}, b[i:]...)...)
	case 3: // delete a byte
		if len(b) > 1 {
			i := m.rng.IntN(len(b))
			b = append(b[:i], b[i+1:]...)
		}
	case 4: // duplicate a slice
		if len(b) > 1 {
			i := m.rng.IntN(len(b))
			j := i + m.rng.IntN(len(b)-i)
			b = append(b[:j], append(b[i:j], b[j:]...)...)
		}
	case 5: // splice two halves
		if len(b) > 2 {
			i := 1 + m.rng.IntN(len(b)-1)
			b = append(b[:i:i], b[i:]...)
		}
	}
	return b
}
