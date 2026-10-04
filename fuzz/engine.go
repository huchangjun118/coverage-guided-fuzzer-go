package fuzz

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// commitWindow 是“生成”领先“提交”的最大逻辑距离。
// 生成第 n 个任务时，只允许看到序号 <= n-commitWindow 的提交结果，
// 因此任务内容只取决于种子与确定性的提交序列，
// 与 worker 数量、调度抖动无关。该值为固定常数，不随并行度变化。
const commitWindow = 16

// maxCrashRecords 记录的崩溃数量上限，防止异常目标刷爆磁盘。
const maxCrashRecords = 256

// Engine 是覆盖率引导模糊测试引擎。
type Engine struct {
	cfg Config
}

// New 创建引擎。
func New(cfg Config) *Engine { return &Engine{cfg: cfg.withDefaults()} }

// Stats 是引擎运行结束后的统计结果。
type Stats struct {
	Execs      uint64      // 总执行次数
	CorpusSize int         // 最终语料条数
	Coverage   int         // 全局覆盖边数
	Crashes    []CrashInfo // 去重后的崩溃记录（含最小可复现输入）
	Timeouts   uint64      // 超时执行次数
	Panics     uint64      // panic 执行次数
	Elapsed    time.Duration
	StopReason string // 优雅停止的原因
}

type task struct {
	seq   uint64
	input []byte
}

type taskResult struct {
	seq   uint64
	input []byte
	res   runResult
}

// Run 运行引擎直到达到任一上限。seeds 为初始种子输入。
// 相同的 Config.Seed、相同的目标与种子输入，产生的执行序列与
// 语料演化顺序逐条一致，与 Workers 数量无关。
func (e *Engine) Run(t Target, seeds [][]byte) Stats {
	cfg := e.cfg
	start := time.Now()
	stats := Stats{}

	crashDir := cfg.CrashDir
	if crashDir == "" && cfg.CorpusDir != "" {
		crashDir = filepath.Join(cfg.CorpusDir, "crashes")
	}

	store := newCorpusStore(cfg.CorpusDir)
	if err := store.load(); err != nil {
		fmt.Fprintf(os.Stderr, "fuzz: load corpus: %v\n", err)
	}
	// 初始任务：先重放磁盘恢复的语料（重建覆盖率，保证中断恢复
	// 不丢已发现覆盖），再执行新种子。
	var initial [][]byte
	for _, en := range store.Snapshot() {
		initial = append(initial, en.Data)
	}
	for _, s := range seeds {
		if _, added := store.Add(s); added {
			initial = append(initial, s)
		}
	}
	if len(initial) == 0 {
		initial = [][]byte{{0}}
	}

	tracker := newCoverageTracker()
	mut := newMutator(cfg.Seed, cfg.MaxInputLen)

	tasks := make(chan task, cfg.Workers)
	results := make(chan taskResult, cfg.Workers*2)
	done := make(chan struct{})
	defer close(done)
	for i := 0; i < cfg.Workers; i++ {
		go worker(t, cfg.ExecTimeout, tasks, results, done)
	}

	// 确定性状态：仅由本 goroutine 访问。
	var (
		nextSeq   uint64 // 下一个待生成任务序号
		commitSeq uint64 // 下一个待提交序号
		inFlight  int
	)
	pending := make(map[uint64]taskResult)
	var view []Entry     // 生成用语料视图：只含序号 <= n-window 的提交
	var viewAdds []Entry // viewAdds[i] 为第 i 次提交新增的条目（nil 表示无新增）
	viewApplied := 0     // 已应用到 view 的提交数
	seenCrash := make(map[string]bool)
	stop := ""

	memExceeded := func() bool {
		if cfg.MaxMemoryMB == 0 {
			return false
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc>>20 >= cfg.MaxMemoryMB
	}
	limitsHit := func() bool {
		if cfg.MaxExecs > 0 && nextSeq >= cfg.MaxExecs {
			stop = "max execs reached"
			return true
		}
		if cfg.MaxDuration > 0 && time.Since(start) >= cfg.MaxDuration {
			stop = "max duration reached"
			return true
		}
		if memExceeded() {
			stop = "memory limit reached"
			return true
		}
		return false
	}

	// generate 生成第 n 个任务的输入。调用前需保证序号 <= n-window
	// 的提交已全部完成，从而视图内容确定。
	generate := func(n uint64) []byte {
		need := 0
		if n >= commitWindow {
			need = int(n - commitWindow + 1)
		}
		for viewApplied < need {
			view = appendView(view, viewAdds[viewApplied])
			viewApplied++
		}
		if uint64(len(initial)) > n {
			return initial[n]
		}
		if len(view) == 0 {
			return []byte{byte(mut.rng.Intn(256))}
		}
		base := view[mut.rng.Intn(len(view))]
		other := view[mut.rng.Intn(len(view))]
		return mut.Mutate(base.Data, other.Data)
	}
	canGenerate := func(n uint64) bool {
		if n < commitWindow {
			return true
		}
		return commitSeq >= n-commitWindow+1
	}

	commit := func(tr taskResult) {
		stats.Execs++
		var added Entry
		switch tr.res.reason {
		case FailPanic, FailTimeout:
			if tr.res.reason == FailPanic {
				stats.Panics++
			} else {
				stats.Timeouts++
			}
			key := tr.res.reason.String() + "|" + fmt.Sprint(tr.res.panicV)
			if !seenCrash[key] && len(stats.Crashes) < maxCrashRecords {
				seenCrash[key] = true
				minInput := minimize(t, tr.input, cfg.ExecTimeout, tr.res.reason)
				ci := CrashInfo{Reason: tr.res.reason, Panic: fmt.Sprint(tr.res.panicV), Input: minInput}
				stats.Crashes = append(stats.Crashes, ci)
				if crashDir != "" {
					persistCrash(crashDir, ci)
				}
			}
		default:
			if tracker.merge(tr.res.cov) > 0 {
				en, ok := store.Add(tr.input)
				if !ok {
					// 已在语料库中（如初始/恢复语料），仍应成为变异基座。
					en = Entry{Data: tr.input, Hash: hashOf(tr.input)}
				}
				added = en
			}
		}
		viewAdds = append(viewAdds, added)
		commitSeq++
	}

	var staged *task
	generating := true
	for {
		// 1. 按逻辑序提交所有已就绪的结果。
		for {
			p, ok := pending[commitSeq]
			if !ok {
				break
			}
			delete(pending, commitSeq)
			commit(p)
		}
		// 2. 停止判定：达到上限后先停止生成，再排空在飞任务，
		//    保证已派发的执行都被有序提交，不留悬挂状态。
		if generating && limitsHit() {
			generating = false
		}
		if !generating && inFlight == 0 {
			break
		}
		// 3. 派发新任务。MaxExecs 按生成序号截断，因此总执行序列
		//    与 worker 数量无关，逐条一致。
		//    注意：generate 消耗 RNG 状态，必须每个序号恰好调用一次，
		//    因此先暂存任务，发送成功后才推进序号。
		for generating && canGenerate(nextSeq) && !limitsHit() {
			if staged == nil {
				tk := task{seq: nextSeq, input: generate(nextSeq)}
				staged = &tk
			}
			select {
			case tasks <- *staged:
				nextSeq++
				inFlight++
				staged = nil
			default:
				goto wait
			}
		}
		if generating && limitsHit() {
			generating = false
		}
		if !generating && inFlight == 0 {
			break
		}
	wait:
		// 此处必有在飞任务（否则上面已退出），阻塞等待结果。
		tr := <-results
		inFlight--
		pending[tr.seq] = tr
	}
	if stop == "" {
		stop = "completed"
	}
	stats.StopReason = stop
	stats.CorpusSize = store.Len()
	stats.Coverage = tracker.totalEdges()
	stats.Elapsed = time.Since(start)
	return stats
}

func appendView(view []Entry, e Entry) []Entry {
	if e.Hash == "" {
		return view
	}
	return append(view, e)
}

func worker(t Target, timeout time.Duration, tasks <-chan task, results chan<- taskResult, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case tk := <-tasks:
			res := execute(t, tk.input, timeout)
			select {
			case results <- taskResult{seq: tk.seq, input: tk.input, res: res}:
			case <-done:
				return
			}
		}
	}
}

func persistCrash(dir string, ci CrashInfo) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := fmt.Sprintf("%s-%s", ci.Reason, hashOf(ci.Input))
	_ = writeAtomic(filepath.Join(dir, name), ci.Input)
}

// Replay 确定性重放单个输入：相同目标与输入在任何进程、
// 任何并发度下都返回相同的失败原因，不受调度影响。
func Replay(t Target, input []byte, timeout time.Duration) (FailReason, any) {
	res := execute(t, input, timeout)
	return res.reason, res.panicV
}
