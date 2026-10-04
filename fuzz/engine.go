package fuzz

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"time"
)

// Engine runs coverage-guided fuzzing campaigns against a Target.
type Engine struct {
	cfg    Config
	target Target
}

// New builds an Engine. cfg zero fields are replaced by defaults.
func New(cfg Config, target Target) *Engine {
	d := DefaultConfig()
	if cfg.Workers <= 0 {
		cfg.Workers = d.Workers
	}
	if cfg.ExecTimeout <= 0 {
		cfg.ExecTimeout = d.ExecTimeout
	}
	if cfg.MaxDuration <= 0 {
		cfg.MaxDuration = d.MaxDuration
	}
	if cfg.MaxInputLen <= 0 {
		cfg.MaxInputLen = d.MaxInputLen
	}
	if cfg.MaxMemoryMB == 0 {
		cfg.MaxMemoryMB = d.MaxMemoryMB
	}
	return &Engine{cfg: cfg, target: target}
}

// execute runs the target once with panic recovery and a timeout, and
// returns the outcome plus the coverage edges the target reported.
// A crash or hang never propagates: the engine always gets a Result.
// On timeout the worker slot is released immediately; the stuck target
// goroutine (if any) holds no engine state.
func (e *Engine) execute(input []byte) (Result, map[uint32]struct{}) {
	set := newCoverageSet()
	cov := &Coverage{set: set}
	done := make(chan string, 1) // panic message, "" on success
	start := time.Now()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Sprintf("%v", r)
				return
			}
			done <- ""
		}()
		e.target(input, cov)
	}()
	timer := time.NewTimer(e.cfg.ExecTimeout)
	defer timer.Stop()
	var res Result
	select {
	case msg := <-done:
		if msg != "" {
			res = Result{Input: input, Outcome: OutcomeCrash, Err: msg, Duration: time.Since(start)}
		} else {
			res = Result{Input: input, Outcome: OutcomeOK, Duration: time.Since(start)}
		}
	case <-timer.C:
		res = Result{Input: input, Outcome: OutcomeTimeout, Duration: e.cfg.ExecTimeout}
	}
	return res, set.snapshot()
}

type seqJob struct {
	seq   uint64
	input []byte
}

type seqResult struct {
	seq   uint64
	res   Result
	edges map[uint32]struct{}
}

// Run executes the fuzzing campaign and returns summary statistics.
//
// Determinism: mutation for sequence number i derives only from
// (cfg.Seed, i) and the corpus state, and coverage merges are applied in
// strictly increasing sequence order by a single coordinator goroutine.
// Workers may finish out of order, but their results are buffered and
// applied in order, so the corpus evolution is identical across runs and
// across worker counts.
func (e *Engine) Run(seeds [][]byte) Stats {
	start := time.Now()
	stats := Stats{}
	global := newCoverageSet()
	corpus := newCorpus()

	if len(seeds) == 0 {
		seeds = [][]byte{{0}}
	}

	// apply merges one result into global state, in sequence order.
	apply := func(res Result, edges map[uint32]struct{}) {
		stats.Executions++
		res.NewEdges = global.countNewInto(edges)
		switch res.Outcome {
		case OutcomeCrash:
			stats.Crashes++
			if e.cfg.CorpusDir != "" {
				min := minimize(res.Input, func(b []byte) bool {
					r, _ := e.execute(b)
					return r.Outcome == OutcomeCrash
				})
				_ = saveCrash(e.cfg.CorpusDir, min, res.Err)
			}
		case OutcomeTimeout:
			stats.Timeouts++
		}
		if res.NewEdges > 0 {
			corpus.add(corpusEntry{input: res.Input, edges: edges})
			if e.cfg.CorpusDir != "" {
				_ = saveCorpusEntry(e.cfg.CorpusDir, res.Input)
			}
		}
	}

	// Phase 1: run seed inputs inline, in order, so the corpus is
	// populated deterministically before any mutation is generated.
	for _, s := range seeds {
		res, edges := e.execute(s)
		apply(res, edges)
	}
	// Guarantee the corpus is non-empty even if no seed hit any edge,
	// so mutation always has a base input.
	if corpus.len() == 0 {
		corpus.add(corpusEntry{input: seeds[0]})
	}

	ctx, cancel := context.WithDeadline(context.Background(), start.Add(e.cfg.MaxDuration))
	defer cancel()

	// The dispatch-ahead window is a fixed constant, NOT derived from
	// Workers: dispatch of sequence k always happens right after the
	// apply of sequence k-window, so the corpus state seen by every
	// mutation is identical for any worker count.
	const window = 32

	jobs := make(chan seqJob)
	// The results buffer must exceed the maximum number of in-flight
	// jobs (window) so workers can never block on send while the
	// coordinator is blocked offering a job — that would deadlock.
	results := make(chan seqResult, window+2*e.cfg.Workers)
	var wg sync.WaitGroup
	for w := 0; w < e.cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				res, edges := e.execute(j.input)
				select {
				case results <- seqResult{seq: j.seq, res: res, edges: edges}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	var seq, nextApply uint64
	pending := make(map[uint64]seqResult)
	stop := false

	// dispatch generates the input for the next sequence number and
	// queues it; returns false when no more jobs should be generated.
	dispatch := func() bool {
		if stop {
			return false
		}
		if e.cfg.MaxExecutions > 0 && seq >= e.cfg.MaxExecutions {
			return false
		}
		// Derive this iteration's randomness solely from (Seed, seq).
		rng := rand.New(rand.NewPCG(uint64(e.cfg.Seed), seq))
		base := corpus.get(rng.IntN(corpus.len()))
		m := newMutator(rng.Uint64(), e.cfg.MaxInputLen)
		select {
		case jobs <- seqJob{seq: seq, input: m.mutate(base)}:
			seq++
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Fill the dispatch window.
	for i := 0; i < window; i++ {
		if !dispatch() {
			break
		}
	}

loop:
	for nextApply < seq {
		select {
		case sr := <-results:
			pending[sr.seq] = sr
			for {
				r, ok := pending[nextApply]
				if !ok {
					break
				}
				delete(pending, nextApply)
				apply(r.res, r.edges)
				nextApply++
				// Check resource limits between executions.
				if e.cfg.MaxMemoryMB > 0 && memUsageMB() > e.cfg.MaxMemoryMB {
					stats.StopReason = StopMemoryLimit
					stop = true
				}
				if !stop {
					dispatch()
				}
			}
		case <-ctx.Done():
			stop = true
			break loop
		}
		if stop {
			break loop
		}
	}

	// Drain: stop accepting new jobs and wait for in-flight results so
	// no worker is left blocked on send; no dangling state remains.
	close(jobs)
	drainTimeout := time.After(e.cfg.ExecTimeout + 100*time.Millisecond)
	for nextApply < seq {
		select {
		case sr := <-results:
			pending[sr.seq] = sr
			for {
				r, ok := pending[nextApply]
				if !ok {
					break
				}
				delete(pending, nextApply)
				apply(r.res, r.edges)
				nextApply++
			}
		case <-drainTimeout:
			// Give up on stragglers; workers exit on ctx cancellation.
			// Unapplied results are dropped from the record.
			nextApply = seq
		}
	}
	wg.Wait()

	stats.CorpusSize = corpus.len()
	stats.EdgesTotal = global.len()
	stats.Duration = time.Since(start)
	if stats.StopReason == "" {
		switch {
		case e.cfg.MaxExecutions > 0 && stats.Executions >= e.cfg.MaxExecutions:
			stats.StopReason = StopMaxExecutions
		case ctx.Err() != nil:
			stats.StopReason = StopMaxDuration
		default:
			stats.StopReason = StopCompleted
		}
	}
	return stats
}

// memUsageMB reports current heap allocation in MiB.
func memUsageMB() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc / (1024 * 1024)
}
