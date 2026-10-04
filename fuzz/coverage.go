package fuzz

import "sync"

// coverageSet is a concurrency-safe set of coverage edge ids.
type coverageSet struct {
	mu   sync.Mutex
	bits map[uint32]struct{}
}

func newCoverageSet() *coverageSet {
	return &coverageSet{bits: make(map[uint32]struct{})}
}

// hit records an edge id.
func (s *coverageSet) hit(id uint32) {
	s.mu.Lock()
	s.bits[id] = struct{}{}
	s.mu.Unlock()
}

// snapshot returns a copy of the current edge ids.
func (s *coverageSet) snapshot() map[uint32]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[uint32]struct{}, len(s.bits))
	for k := range s.bits {
		out[k] = struct{}{}
	}
	return out
}

// countNewInto merges src into s and returns how many edges were new.
// It is atomic with respect to other merges so concurrent workers never
// double-count the same edge.
func (s *coverageSet) countNewInto(src map[uint32]struct{}) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id := range src {
		if _, ok := s.bits[id]; !ok {
			s.bits[id] = struct{}{}
			n++
		}
	}
	return n
}

// len returns the number of distinct edges.
func (s *coverageSet) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bits)
}
