package fuzz

import "sync"

// corpusEntry is one interesting input plus the edges it covers.
type corpusEntry struct {
	input []byte
	edges map[uint32]struct{}
}

// Corpus holds inputs that each discovered at least one new edge.
// It is safe for concurrent use.
type Corpus struct {
	mu      sync.Mutex
	entries []corpusEntry
}

func newCorpus() *Corpus { return &Corpus{} }

// add appends an entry; callers must only add inputs with new coverage.
func (c *Corpus) add(e corpusEntry) {
	c.mu.Lock()
	c.entries = append(c.entries, e)
	c.mu.Unlock()
}

// len returns the number of entries.
func (c *Corpus) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// get returns the input at index i (copying it).
func (c *Corpus) get(i int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	in := c.entries[i].input
	out := make([]byte, len(in))
	copy(out, in)
	return out
}

// snapshot returns copies of all entries.
func (c *Corpus) snapshot() []corpusEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]corpusEntry, len(c.entries))
	copy(out, c.entries)
	return out
}
