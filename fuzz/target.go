package fuzz

import (
	"fmt"
	"time"
)

// Target 是被测目标函数，data 为引擎生成的输入，
// 目标应在执行过程中通过 ctx.Track 上报覆盖边。
type Target func(ctx *Context, data []byte)

// Context 向目标暴露覆盖率上报能力。
// 每次执行使用独立的 Context，超时被淘汰的执行不会污染共享状态。
type Context struct {
	cov *Coverage
}

// Track 上报一条覆盖边，id 通常由被测代码插桩点唯一确定。
func (c *Context) Track(id uint64) { c.cov.add(id) }

// FailReason 区分单次执行的失败原因。
type FailReason int

const (
	FailNone    FailReason = iota // 正常结束
	FailPanic                     // 目标 panic
	FailTimeout                   // 执行超时
)

func (r FailReason) String() string {
	switch r {
	case FailNone:
		return "ok"
	case FailPanic:
		return "panic"
	case FailTimeout:
		return "timeout"
	}
	return "unknown"
}

// runResult 是单次执行的产出。
type runResult struct {
	reason  FailReason
	panicV  any
	cov     *Coverage
	elapsed time.Duration
}

// execute 运行一次目标，panic 与超时都被隔离，不会传播到引擎。
// 超时后目标 goroutine 被遗弃，但其只写本次执行私有的 Coverage，
// 被淘汰后不会留下影响引擎的悬挂状态。
func execute(t Target, data []byte, timeout time.Duration) runResult {
	cov := newCoverage()
	type outcome struct{ panicV any }
	done := make(chan outcome, 1)
	go func() {
		var o outcome
		defer func() {
			if r := recover(); r != nil {
				o.panicV = r
			}
			done <- o
		}()
		t(&Context{cov: cov}, data)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	start := time.Now()
	select {
	case o := <-done:
		res := runResult{reason: FailNone, cov: cov, elapsed: time.Since(start)}
		if o.panicV != nil {
			res.reason = FailPanic
			res.panicV = o.panicV
		}
		return res
	case <-timer.C:
		return runResult{reason: FailTimeout, cov: nil, elapsed: timeout}
	}
}

// CrashInfo 描述一次可复现的失败。
type CrashInfo struct {
	Reason FailReason
	Panic  string
	Input  []byte // 最小可复现输入
}

func (c CrashInfo) String() string {
	if c.Reason == FailPanic {
		return fmt.Sprintf("panic: %s (input %d bytes)", c.Panic, len(c.Input))
	}
	return fmt.Sprintf("%s (input %d bytes)", c.Reason, len(c.Input))
}
