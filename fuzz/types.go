// Package fuzz implements a coverage-guided fuzzing engine.
//
// The engine executes registered targets against mutated inputs, collects
// coverage feedback reported by the target, and evolves a corpus of
// interesting inputs. All randomness derives from a single seed so runs
// are reproducible.
package fuzz

import "time"

// Outcome classifies the result of a single target execution.
type Outcome int

const (
	// OutcomeOK means the target returned normally.
	OutcomeOK Outcome = iota
	// OutcomeCrash means the target panicked (recovered by the engine).
	OutcomeCrash
	// OutcomeTimeout means the target did not finish within ExecTimeout.
	OutcomeTimeout
)

// String returns a human-readable name for the outcome.
func (o Outcome) String() string {
	switch o {
	case OutcomeOK:
		return "ok"
	case OutcomeCrash:
		return "crash"
	case OutcomeTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

// Coverage is passed to the target so instrumented code can report which
// edges it hit. The zero value is not usable; the engine provides one.
type Coverage struct {
	set *coverageSet
}

// Hit records that the edge with the given id was executed. It is safe
// for concurrent use. ids should be small dense integers (e.g. branch
// indices assigned by instrumentation).
func (c *Coverage) Hit(id uint32) {
	if c.set != nil {
		c.set.hit(id)
	}
}

// Target is the function under test. It receives the input bytes and a
// Coverage recorder. A panic inside Target is recovered by the engine and
// reported as OutcomeCrash.
type Target func(input []byte, cov *Coverage)

// Result describes one execution of the target.
type Result struct {
	Input    []byte
	Outcome  Outcome
	Err      string // panic message when Outcome == OutcomeCrash
	NewEdges int    // number of previously unseen edges this input hit
	Duration time.Duration
}

// Config controls the engine. All fields are optional; zero values are
// replaced by defaults documented in DefaultConfig.
type Config struct {
	// Seed makes the whole run deterministic: same seed + same initial
	// corpus => identical mutation and execution sequence.
	Seed int64
	// Workers is the number of parallel execution units.
	Workers int
	// ExecTimeout is the per-execution timeout. Inputs exceeding it are
	// recorded as OutcomeTimeout and the worker slot is released.
	ExecTimeout time.Duration
	// MaxDuration bounds the total wall-clock time of a run.
	MaxDuration time.Duration
	// MaxExecutions bounds the total number of target executions.
	MaxExecutions uint64
	// MaxInputLen bounds mutated input length.
	MaxInputLen int
	// MaxMemoryMB bounds process heap usage; the engine stops gracefully
	// once the limit is exceeded (checked between executions).
	MaxMemoryMB uint64
	// CorpusDir persists the corpus, crashes and a manifest across runs.
	// Empty means in-memory only.
	CorpusDir string
}

// DefaultConfig returns a Config with sane defaults.
func DefaultConfig() Config {
	return Config{
		Seed:          1,
		Workers:       4,
		ExecTimeout:   100 * time.Millisecond,
		MaxDuration:   30 * time.Second,
		MaxExecutions: 0, // unlimited
		MaxInputLen:   4096,
		MaxMemoryMB:   1024,
	}
}

// StopReason explains why a run ended.
type StopReason string

const (
	StopMaxDuration   StopReason = "max_duration_reached"
	StopMaxExecutions StopReason = "max_executions_reached"
	StopMemoryLimit   StopReason = "memory_limit_exceeded"
	StopCompleted     StopReason = "completed"
)

// Stats summarizes a finished run.
type Stats struct {
	Executions uint64
	CorpusSize int
	EdgesTotal int
	Crashes    uint64
	Timeouts   uint64
	Duration   time.Duration
	StopReason StopReason
}
