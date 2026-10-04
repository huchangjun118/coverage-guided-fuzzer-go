package fuzz

import "time"

// Config 控制模糊测试引擎的全部可调参数。
type Config struct {
	// Seed 随机种子，固定后整个演化序列可复现。
	Seed int64
	// Workers 并行执行的工作单元数量，<=0 时取 1。
	Workers int
	// ExecTimeout 单次目标执行的超时时间。
	ExecTimeout time.Duration
	// MaxDuration 引擎总运行时长上限，0 表示不限。
	MaxDuration time.Duration
	// MaxExecs 总执行次数上限，0 表示不限。
	MaxExecs uint64
	// MaxMemoryMB 引擎进程内存上限（MB），0 表示不限。
	MaxMemoryMB uint64
	// MaxInputLen 变异生成输入的最大长度。
	MaxInputLen int
	// CorpusDir 语料库持久化目录，空表示不持久化。
	CorpusDir string
	// CrashDir 崩溃输入持久化目录，空时写入 CorpusDir/crashes。
	CrashDir string
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.Workers <= 0 {
		out.Workers = 1
	}
	if out.ExecTimeout <= 0 {
		out.ExecTimeout = time.Second
	}
	if out.MaxInputLen <= 0 {
		out.MaxInputLen = 4096
	}
	return out
}
