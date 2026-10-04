// Command fuzzdemo runs a small coverage-guided fuzzing campaign against
// a demo target and prints reproducible statistics.
package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/huchangjun118/coverage-guided-fuzzer-go/fuzz"
)

// demoTarget is a tiny parser-like function with instrumented branches.
func demoTarget(input []byte, cov *fuzz.Coverage) {
	cov.Hit(0)
	if len(input) == 0 {
		return
	}
	cov.Hit(1)
	switch input[0] {
	case 'G':
		cov.Hit(2)
		if len(input) > 1 && input[1] == 'O' {
			cov.Hit(3)
			if len(input) > 2 && input[2] == 'P' {
				cov.Hit(4)
				panic("found the magic word") // the bug
			}
		}
	case 'Z':
		cov.Hit(5)
		if len(input) > 3 && input[3] == 0xff {
			cov.Hit(6)
		}
	}
	for i, b := range input {
		cov.Hit(uint32(10 + (int(b)+i)%50))
	}
}

func main() {
	seed := flag.Int64("seed", 1, "random seed (deterministic run)")
	workers := flag.Int("workers", 4, "parallel workers")
	maxExec := flag.Uint64("max-exec", 20000, "max mutated executions")
	maxDur := flag.Duration("max-duration", 30*time.Second, "max total duration")
	execTimeout := flag.Duration("exec-timeout", 100*time.Millisecond, "per-execution timeout")
	maxMem := flag.Uint64("max-memory-mb", 1024, "heap limit in MiB")
	corpusDir := flag.String("corpus-dir", "fuzzdata", "corpus persistence directory")
	flag.Parse()

	eng := fuzz.New(fuzz.Config{
		Seed:          *seed,
		Workers:       *workers,
		ExecTimeout:   *execTimeout,
		MaxDuration:   *maxDur,
		MaxExecutions: *maxExec,
		MaxMemoryMB:   *maxMem,
		CorpusDir:     *corpusDir,
	}, demoTarget)

	// Resume from a previous run's corpus if present.
	seeds, err := fuzz.LoadCorpusInputs(*corpusDir)
	if err != nil {
		fmt.Println("load corpus:", err)
	}
	if len(seeds) == 0 {
		seeds = [][]byte{[]byte("G"), []byte("Z")}
	}

	stats := eng.Run(seeds)
	fmt.Printf("executions=%d corpus=%d edges=%d crashes=%d timeouts=%d duration=%s stop=%s\n",
		stats.Executions, stats.CorpusSize, stats.EdgesTotal,
		stats.Crashes, stats.Timeouts, stats.Duration.Round(time.Millisecond), stats.StopReason)
}
