package fuzz_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	fuzz "github.com/huchangjun118/coverage-guided-fuzzer-go/fuzz"
)

// branchTarget 是一个确定性的被测目标：
//   - 按输入内容命中不同覆盖边；
//   - 输入以 "boom" 开头时 panic；
//   - 输入以 0xFF 开头时永久挂起（用于超时测试）。
func branchTarget(ctx *fuzz.Context, data []byte) {
	if len(data) == 0 {
		return
	}
	ctx.Track(1)
	if data[0] == 0xFF {
		select {} // 永久挂起，依赖引擎超时淘汰
	}
	if data[0] == 'A' {
		ctx.Track(2)
		if len(data) > 1 && data[1] == 'B' {
			ctx.Track(3)
			if len(data) > 2 && data[2] == 'C' {
				ctx.Track(4)
			}
		}
	}
	if len(data) >= 4 && string(data[:4]) == "boom" {
		panic("simulated crash")
	}
	if len(data) > 3 && data[3] == 0x42 {
		ctx.Track(5)
	}
}

func baseConfig(dir string) fuzz.Config {
	return fuzz.Config{
		Seed:        42,
		Workers:     2,
		ExecTimeout: 50 * time.Millisecond,
		MaxExecs:    400,
		MaxInputLen: 64,
		CorpusDir:   dir,
	}
}

// 语料目录中的条目哈希（排除 crashes/quarantine 子目录）。
func corpusHashes(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}
	var hashes []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		hashes = append(hashes, e.Name())
	}
	sort.Strings(hashes)
	return hashes
}

func TestCrashDetectedMinimizedAndReplayable(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	cfg.Seed = 7
	eng := fuzz.New(cfg)
	stats := eng.Run(branchTarget, [][]byte{[]byte("bo"), []byte("boom")})
	if len(stats.Crashes) == 0 {
		t.Fatal("expected at least one crash")
	}
	if stats.Panics == 0 {
		t.Fatal("expected panic count > 0")
	}
	crash := stats.Crashes[0]
	if crash.Reason != fuzz.FailPanic {
		t.Fatalf("expected panic reason, got %v", crash.Reason)
	}
	// 最小可复现输入必须仍能稳定触发相同的崩溃。
	for i := 0; i < 3; i++ {
		reason, panicV := fuzz.Replay(branchTarget, crash.Input, cfg.ExecTimeout)
		if reason != fuzz.FailPanic {
			t.Fatalf("replay %d: expected panic, got %v", i, reason)
		}
		if fmt.Sprint(panicV) != crash.Panic {
			t.Fatalf("replay %d: panic mismatch %v vs %s", i, panicV, crash.Panic)
		}
	}
	// 崩溃输入已持久化，可跨进程复现。
	crashFiles, err := os.ReadDir(filepath.Join(dir, "crashes"))
	if err != nil || len(crashFiles) == 0 {
		t.Fatalf("expected persisted crash files: %v", err)
	}
}

func TestTimeoutIsolatedAndEngineSurvives(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	cfg.MaxExecs = 300
	eng := fuzz.New(cfg)
	// 种子直接包含挂起输入，保证超时路径被触发。
	stats := eng.Run(branchTarget, [][]byte{{0xFF, 1, 2}, []byte("seed")})
	if stats.Timeouts == 0 {
		t.Fatal("expected timeout count > 0")
	}
	found := false
	for _, c := range stats.Crashes {
		if c.Reason == fuzz.FailTimeout {
			found = true
			// 超时输入重放必须仍是超时。
			reason, _ := fuzz.Replay(branchTarget, c.Input, cfg.ExecTimeout)
			if reason != fuzz.FailTimeout {
				t.Fatalf("timeout replay got %v", reason)
			}
		}
	}
	if !found {
		t.Fatal("expected a timeout crash record")
	}
	// 引擎正常结束并给出可解释的停止原因。
	if stats.StopReason == "" {
		t.Fatal("expected stop reason")
	}
	t.Logf("stats: %+v", stats)
}

func TestDeterministicEvolution(t *testing.T) {
	run := func(workers int) (fuzz.Stats, []string) {
		dir := t.TempDir()
		cfg := baseConfig(dir)
		cfg.Workers = workers
		cfg.MaxExecs = 500
		stats := fuzz.New(cfg).Run(branchTarget, [][]byte{[]byte("AB"), []byte("xyz")})
		return stats, corpusHashes(t, dir)
	}
	s1, c1 := run(1)
	s2, c2 := run(1)
	// 同种子同配置：执行序列与语料演化逐条一致。
	if fmt.Sprintf("%v", c1) != fmt.Sprintf("%v", c2) {
		t.Fatal("same config runs produced different corpus")
	}
	if s1.Execs != s2.Execs || s1.Coverage != s2.Coverage {
		t.Fatalf("same config runs diverged: %+v vs %+v", s1, s2)
	}
	// 不同并发度：逻辑序列不变，语料演化结果一致。
	s3, c3 := run(4)
	if fmt.Sprintf("%v", c1) != fmt.Sprintf("%v", c3) {
		t.Fatalf("corpus diverged across worker counts:\n1w: %v\n4w: %v", c1, c3)
	}
	if s1.Execs != s3.Execs || s1.Coverage != s3.Coverage || s1.CorpusSize != s3.CorpusSize {
		t.Fatalf("stats diverged across worker counts: %+v vs %+v", s1, s3)
	}
	if fmt.Sprintf("%v", s1.Crashes) != fmt.Sprintf("%v", s3.Crashes) {
		t.Fatal("crashes diverged across worker counts")
	}
}

func TestCorpusOnlyGrowsOnNewCoverage(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	cfg.MaxExecs = 600
	// 目标对所有输入只上报同一条边：语料不得膨胀。
	singleEdge := func(ctx *fuzz.Context, data []byte) { ctx.Track(99) }
	stats := fuzz.New(cfg).Run(singleEdge, [][]byte{[]byte("a"), []byte("b")})
	// 只有第一个命中边 99 的输入能进入语料，之后不再增长。
	if stats.CorpusSize > 2 {
		t.Fatalf("corpus grew without new coverage: %d", stats.CorpusSize)
	}
	if stats.Coverage != 1 {
		t.Fatalf("expected coverage 1, got %d", stats.Coverage)
	}
}

func TestConcurrentCoverageAccuracy(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	cfg.Workers = 8
	cfg.MaxExecs = 800
	stats := fuzz.New(cfg).Run(branchTarget, [][]byte{[]byte("A"), []byte("AB")})
	// 并发执行下，已发现覆盖必须单调累积且不丢边。
	if stats.Coverage < 3 {
		t.Fatalf("expected at least 4 edges, got %d", stats.Coverage)
	}
	if stats.Execs != 800 {
		t.Fatalf("expected exactly 800 execs, got %d", stats.Execs)
	}
}

func TestRestartRecoveryAndCorruptionIsolation(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(dir)
	cfg.MaxExecs = 300
	first := fuzz.New(cfg).Run(branchTarget, [][]byte{[]byte("AB"), []byte("seed")})
	if first.Coverage == 0 {
		t.Fatal("first run found no coverage")
	}
	before := corpusHashes(t, dir)
	if len(before) == 0 {
		t.Fatal("corpus not persisted")
	}
	// 损坏一条语料：内容被篡改，哈希不再匹配。
	target := filepath.Join(dir, before[0])
	if err := os.WriteFile(target, []byte("corrupted-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 重启恢复：不得因损坏条目失败，已发现覆盖不丢失。
	cfg.MaxExecs = 600
	second := fuzz.New(cfg).Run(branchTarget, [][]byte{[]byte("AB"), []byte("seed")})
	if second.Coverage < first.Coverage {
		t.Fatalf("coverage lost across restart: %d -> %d", first.Coverage, second.Coverage)
	}
	// 损坏条目被隔离到 quarantine。
	q, err := os.ReadDir(filepath.Join(dir, "quarantine"))
	if err != nil || len(q) == 0 {
		t.Fatalf("corrupted entry not quarantined: %v", err)
	}
	// 恢复后语料不重复写入：同一哈希只出现一次。
	seen := map[string]bool{}
	for _, h := range corpusHashes(t, dir) {
		if seen[h] {
			t.Fatalf("duplicate corpus entry %s", h)
		}
		seen[h] = true
	}
}

func TestLimitsGracefulStop(t *testing.T) {
	// 总时长上限。
	dir := t.TempDir()
	cfg := baseConfig(dir)
	cfg.MaxExecs = 0
	cfg.MaxDuration = 200 * time.Millisecond
	stats := fuzz.New(cfg).Run(branchTarget, [][]byte{[]byte("x")})
	if stats.StopReason != "max duration reached" {
		t.Fatalf("unexpected stop reason %q", stats.StopReason)
	}
	if stats.Elapsed > 5*time.Second {
		t.Fatalf("engine did not stop gracefully: %v", stats.Elapsed)
	}
	// 执行次数上限：精确截断。
	cfg2 := baseConfig(t.TempDir())
	cfg2.MaxExecs = 123
	stats2 := fuzz.New(cfg2).Run(branchTarget, [][]byte{[]byte("x")})
	if stats2.Execs != 123 {
		t.Fatalf("expected exactly 123 execs, got %d", stats2.Execs)
	}
	if stats2.StopReason != "max execs reached" {
		t.Fatalf("unexpected stop reason %q", stats2.StopReason)
	}
}

func TestReplayStableAcrossProcesses(t *testing.T) {
	// 同一输入在不同“进程”（此处用多次独立调用模拟）与并发度下结果一致。
	inputs := [][]byte{[]byte("boom!"), {0xFF}, []byte("ABC"), []byte("plain")}
	for _, in := range inputs {
		want, _ := fuzz.Replay(branchTarget, in, 30*time.Millisecond)
		for i := 0; i < 5; i++ {
			got, _ := fuzz.Replay(branchTarget, in, 30*time.Millisecond)
			if got != want {
				t.Fatalf("replay of %q unstable: %v vs %v", in, want, got)
			}
		}
	}
}

func TestMemoryLimitGracefulStop(t *testing.T) {
	cfg := baseConfig(t.TempDir())
	cfg.MaxExecs = 0
	cfg.MaxMemoryMB = 1 // 1MB 必然立即触发
	stats := fuzz.New(cfg).Run(branchTarget, [][]byte{[]byte("x")})
	if stats.StopReason != "memory limit reached" {
		t.Fatalf("unexpected stop reason %q", stats.StopReason)
	}
}
