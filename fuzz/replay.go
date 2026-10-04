package fuzz

// Replay executes input once against the target and returns the outcome.
//
// Replay is deterministic: the same input always produces the same
// Outcome regardless of process, worker count, or scheduling jitter,
// because it performs a single isolated execution with the same panic
// recovery and timeout semantics used during fuzzing. Use it to confirm
// crashes and timeouts found by a campaign.
func (e *Engine) Replay(input []byte) Result {
	res, _ := e.execute(input)
	return res
}

// ReplayCorpusEntry loads a persisted corpus or crash entry by path and
// replays it. This is the stable way to reproduce a historical finding
// in a fresh process.
func (e *Engine) ReplayCorpusEntry(path string) (Result, error) {
	data, err := readFile(path)
	if err != nil {
		return Result{}, err
	}
	return e.Replay(data), nil
}
