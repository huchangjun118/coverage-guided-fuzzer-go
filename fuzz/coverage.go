package fuzz

import "sync"

// Coverage 记录一次执行命中的边集合。
type Coverage struct {
	edges map[uint64]struct{}
}

func newCoverage() *Coverage { return &Coverage{edges: make(map[uint64]struct{})} }

func (c *Coverage) add(id uint64) {
	if c != nil {
		c.edges[id] = struct{}{}
	}
}

func (c *Coverage) len() int { return len(c.edges) }

// coverageShard 是覆盖率位图的一个分片，降低并发写竞争。
type coverageShard struct {
	mu    sync.Mutex
	edges map[uint64]struct{}
}

const numShards = 64

// coverageTracker 汇总全局覆盖率。
// 统计准确性通过“提交时串行合并”保证：worker 各自收集本次执行的
// 覆盖，提交阶段按确定性逻辑序逐条 merge，新覆盖判定不受调度抖动影响。
type coverageTracker struct {
	shards [numShards]coverageShard
	total  int
}

func newCoverageTracker() *coverageTracker {
	t := &coverageTracker{}
	for i := range t.shards {
		t.shards[i].edges = make(map[uint64]struct{})
	}
	return t
}

// merge 将一次执行的覆盖并入全局位图，返回本次执行新命中的边数。
// 调用方需保证按逻辑顺序串行调用（引擎在提交点持有顺序锁）。
func (t *coverageTracker) merge(c *Coverage) (newEdges int) {
	if c == nil {
		return 0
	}
	for id := range c.edges {
		s := &t.shards[id%numShards]
		s.mu.Lock()
		if _, ok := s.edges[id]; !ok {
			s.edges[id] = struct{}{}
			t.total++
			newEdges++
		}
		s.mu.Unlock()
	}
	return newEdges
}

// totalEdges 返回全局已覆盖的边数。
func (t *coverageTracker) totalEdges() int {
	n := 0
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()
		n += len(s.edges)
		s.mu.Unlock()
	}
	return n
}

// covers 判断 c 中是否存在全局尚未覆盖的边（用于快速预检）。
func (t *coverageTracker) covers(c *Coverage) bool {
	for id := range c.edges {
		s := &t.shards[id%numShards]
		s.mu.Lock()
		_, ok := s.edges[id]
		s.mu.Unlock()
		if !ok {
			return false
		}
	}
	return true
}
