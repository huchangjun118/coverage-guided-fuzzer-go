package fuzz

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// branchyTarget reports edges based on input content, giving the engine
// a large but finite edge space to explore.
func branchyTarget(input []byte, cov *Coverage) {
	cov.Hit(0)
	for i, b := range input {
		cov.Hit(uint32(1 + (int(b)*31+i)%97))
	}
	if len(input) >= 2 && input[0] == 'O' && input[1] == 'K' {
		cov.Hit(500)
	}
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// --- 1. Crash isolation, minimization, replay ---

func TestCrashIsolatedAndMinimized(t *testing.T) {
	target := func(input []byte, cov *Coverage) {
		cov.Hit(0)
		if bytes.Contains(input, []byte("BUG")) {
			panic("boom")
		}
	}
	dir := t.TempDir()
	eng := New(Config{
		Seed:          7,
		Workers:       2,
		ExecTimeout:   100 * time.Millisecond,
		MaxDuration:   5 * time.Second,
		MaxExecutions: 10,
		CorpusDir:     dir,
	}, target)
	stats := eng.Run([][]byte{[]byte("xxBUGxx")})
	if stats.Crashes == 0 {
		t.Fatal("expected at least one crash")
	}
	// Engine survived and produced a stop reason.
	if stats.StopReason == "" {
		t.Fatal("missing stop reason")
	}
	// A minimal reproducer must have been persisted.
	crashes := listFiles(t, filepath.Join(dir, crashesSubdir))
	if len(crashes) == 0 {
		t.Fatal("no crash file persisted")
	}
	var repro []byte
	for _, n := range crashes {
		if filepath.Ext(n) == ".txt" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, crashesSubdir, n))
		if err != nil {
			t.Fatal(err)
		}
		repro = data
	}
	if string(repro) != "BUG" {
		t.Fatalf("expected minimal repro %q, got %q", "BUG", repro)
	}
	// Replay reproduces the crash deterministically, even with a
	// different worker count in a fresh engine.
	eng2 := New(Config{Workers: 8}, target)
	res := eng2.Replay(repro)
	if res.Outcome != OutcomeCrash {
		t.Fatalf("replay outcome = %v, want crash", res.Outcome)
	}
	if res.Err == "" {
		t.Fatal("replay missing panic message")
	}
}

// --- 2. Timeout isolation ---

func TestTimeoutIsolated(t *testing.T) {
	target := func(input []byte, cov *Coverage) {
		cov.Hit(0)
		if len(input) > 0 && input[0] == 'S' {
			time.Sleep(10 * time.Second) // hang
		}
	}
	eng := New(Config{
		Seed:          3,
		Workers:       2,
		ExecTimeout:   50 * time.Millisecond,
		MaxDuration:   3 * time.Second,
		MaxExecutions: 20,
	}, target)
	start := time.Now()
	stats := eng.Run([][]byte{[]byte("S"), []byte("ok")})
	if stats.Timeouts == 0 {
		t.Fatal("expected at least one timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("hung input dragged the engine: %v", elapsed)
	}
	// The timeout outcome is distinguishable and replayable.
	res := eng.Replay([]byte("S"))
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("replay outcome = %v, want timeout", res.Outcome)
	}
}

// --- 3. Deterministic evolution across runs and worker counts ---

func TestDeterministicEvolution(t *testing.T) {
	runOnce := func(workers int) (Stats, []string, [][]byte) {
		dir := t.TempDir()
		eng := New(Config{
			Seed:          42,
			Workers:       workers,
			ExecTimeout:   time.Second,
			MaxDuration:   30 * time.Second,
			MaxExecutions: 300,
			MaxInputLen:   64,
			CorpusDir:     dir,
		}, branchyTarget)
		stats := eng.Run([][]byte{[]byte("seed")})
		names := listFiles(t, filepath.Join(dir, corpusSubdir))
		var contents [][]byte
		for _, n := range names {
			data, err := os.ReadFile(filepath.Join(dir, corpusSubdir, n))
			if err != nil {
				t.Fatal(err)
			}
			contents = append(contents, data)
		}
		return stats, names, contents
	}
	s1, n1, c1 := runOnce(1)
	s2, n2, c2 := runOnce(4)
	_, n3, c3 := runOnce(1) // repeat to confirm reproducibility

	if s1.Executions != s2.Executions || s1.CorpusSize != s2.CorpusSize || s1.EdgesTotal != s2.EdgesTotal {
		t.Fatalf("stats diverge across worker counts: %+v vs %+v", s1, s2)
	}
	if len(n1) != len(n2) || len(n1) != len(n3) {
		t.Fatalf("corpus sizes diverge: %d %d %d", len(n1), len(n2), len(n3))
	}
	for i := range n1 {
		if n1[i] != n2[i] || n1[i] != n3[i] {
			t.Fatalf("corpus entry %d diverges across runs", i)
		}
		if !bytes.Equal(c1[i], c2[i]) || !bytes.Equal(c1[i], c3[i]) {
			t.Fatalf("corpus content %d diverges across runs", i)
		}
	}
	if s1.StopReason != StopMaxExecutions {
		t.Fatalf("stop reason = %v, want %v", s1.StopReason, StopMaxExecutions)
	}
}

// --- 4. Corpus bounded by coverage (no unbounded growth) ---

func TestCorpusBoundedByEdges(t *testing.T) {
	// Target with exactly 5 reachable edges.
	target := func(input []byte, cov *Coverage) {
		cov.Hit(0)
		for _, b := range input {
			if int(b) < 4 {
				cov.Hit(uint32(1 + b))
			}
		}
	}
	eng := New(Config{
		Seed:          9,
		Workers:       4,
		ExecTimeout:   time.Second,
		MaxDuration:   30 * time.Second,
		MaxExecutions: 2000,
		MaxInputLen:   32,
	}, target)
	stats := eng.Run([][]byte{[]byte{0, 1, 2, 3}})
	if stats.CorpusSize > 5 {
		t.Fatalf("corpus grew to %d entries with only 5 edges", stats.CorpusSize)
	}
	if stats.EdgesTotal != 5 {
		t.Fatalf("edges = %d, want 5", stats.EdgesTotal)
	}
}

// --- 5. Concurrent coverage accounting ---

func TestCoverageSetConcurrent(t *testing.T) {
	global := newCoverageSet()
	const workers = 16
	const perWorker = 1000
	var wg sync.WaitGroup
	totalNew := 0
	var mu sync.Mutex
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			local := make(map[uint32]struct{})
			for i := 0; i < perWorker; i++ {
				// Edge ids overlap heavily across workers.
				local[uint32((w*7+i)%500)] = struct{}{}
			}
			n := global.countNewInto(local)
			mu.Lock()
			totalNew += n
			mu.Unlock()
		}(w)
	}
	wg.Wait()
	if global.len() != 500 {
		t.Fatalf("global edges = %d, want 500", global.len())
	}
	if totalNew != 500 {
		t.Fatalf("sum of new edges = %d, want 500 (double counting?)", totalNew)
	}
}

// --- 6. Persistence, restart recovery, quarantine ---

func TestRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Seed:          11,
		Workers:       2,
		ExecTimeout:   time.Second,
		MaxDuration:   30 * time.Second,
		MaxExecutions: 100,
		MaxInputLen:   32,
		CorpusDir:     dir,
	}
	eng := New(cfg, branchyTarget)
	s1 := eng.Run([][]byte{[]byte("seed")})
	before := listFiles(t, filepath.Join(dir, corpusSubdir))
	if len(before) == 0 {
		t.Fatal("corpus was not persisted")
	}

	// Plant a corrupt entry (name does not match content hash) and a
	// half-written temp file from an interrupted write.
	if err := os.WriteFile(filepath.Join(dir, corpusSubdir, "deadbeef"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, corpusSubdir, "abc.tmp"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Loading must succeed and isolate the corrupt entries.
	seeds, err := LoadCorpusInputs(dir)
	if err != nil {
		t.Fatalf("load failed on corrupt corpus: %v", err)
	}
	if len(seeds) != len(before) {
		t.Fatalf("loaded %d entries, want %d", len(seeds), len(before))
	}
	q := listFiles(t, filepath.Join(dir, quarantineSubdir))
	if len(q) != 2 {
		t.Fatalf("quarantined %d entries, want 2", len(q))
	}

	// Resume from the persisted corpus: previously discovered coverage
	// is re-executed, and already-persisted entries are not duplicated.
	eng2 := New(cfg, branchyTarget)
	s2 := eng2.Run(seeds)
	after := listFiles(t, filepath.Join(dir, corpusSubdir))
	seen := make(map[string]int)
	for _, n := range after {
		seen[n]++
		if seen[n] > 1 {
			t.Fatalf("duplicate corpus entry %s after resume", n)
		}
	}
	for _, n := range before {
		if seen[n] != 1 {
			t.Fatalf("previously persisted entry %s lost after resume", n)
		}
	}
	if s2.EdgesTotal < s1.EdgesTotal {
		t.Fatalf("coverage regressed across restart: %d -> %d", s1.EdgesTotal, s2.EdgesTotal)
	}
}

// --- 7. Resource limits ---

func TestMaxExecutionsLimit(t *testing.T) {
	eng := New(Config{
		Workers:       3,
		ExecTimeout:   time.Second,
		MaxDuration:   time.Minute,
		MaxExecutions: 50,
	}, branchyTarget)
	stats := eng.Run([][]byte{[]byte("x")})
	if stats.StopReason != StopMaxExecutions {
		t.Fatalf("stop reason = %v, want %v", stats.StopReason, StopMaxExecutions)
	}
	// 1 seed + 50 mutated executions.
	if stats.Executions != 51 {
		t.Fatalf("executions = %d, want 51", stats.Executions)
	}
}

func TestMemoryLimit(t *testing.T) {
	// Force heap usage above 1 MiB so the 1 MiB limit trips immediately.
	ballast := make([]byte, 8<<20)
	for i := range ballast {
		ballast[i] = byte(i)
	}
	defer func() { _ = ballast[0] }()
	eng := New(Config{
		Workers:       2,
		ExecTimeout:   time.Second,
		MaxDuration:   time.Minute,
		MaxMemoryMB:   1,
		MaxExecutions: 100000,
	}, branchyTarget)
	stats := eng.Run([][]byte{[]byte("x")})
	if stats.StopReason != StopMemoryLimit {
		t.Fatalf("stop reason = %v, want %v", stats.StopReason, StopMemoryLimit)
	}
}

func TestMaxDurationLimit(t *testing.T) {
	eng := New(Config{
		Workers:     2,
		ExecTimeout: 20 * time.Millisecond,
		MaxDuration: 200 * time.Millisecond,
	}, branchyTarget)
	start := time.Now()
	stats := eng.Run(nil) // no seeds: engine must synthesize one
	if stats.StopReason != StopMaxDuration {
		t.Fatalf("stop reason = %v, want %v", stats.StopReason, StopMaxDuration)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("engine did not stop near MaxDuration")
	}
	if stats.Executions == 0 {
		t.Fatal("no executions recorded")
	}
}
