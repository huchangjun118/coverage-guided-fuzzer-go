# coverage-guided-fuzzer-go

一个覆盖率引导的模糊测试引擎。被测目标以普通 Go 函数注册，引擎自动变异输入、
依据覆盖率反馈进化语料，并对崩溃/超时输入做确定性最小化，产出可稳定复现的结论。

## 快速开始

```go
eng := fuzz.New(fuzz.Config{
    Seed:        42,
    Workers:     4,
    ExecTimeout: 100 * time.Millisecond,
    MaxExecs:    100000,
    CorpusDir:   "corpus",
})
stats := eng.Run(func(ctx *fuzz.Context, data []byte) {
    // 被测逻辑；在分支点调用 ctx.Track(edgeID) 上报覆盖
    if len(data) > 0 && data[0] == 'A' {
        ctx.Track(1)
        // ...
    }
}, [][]byte{[]byte("seed")})
fmt.Println(stats.StopReason, stats.Coverage, len(stats.Crashes))
```

## 覆盖率反馈语义

- 目标通过 `ctx.Track(id uint64)` 上报覆盖边，`id` 由插桩点唯一确定。
- 每次执行在独立的 `Context` 中收集覆盖；worker 并发执行互不干扰。
- 覆盖合并在**提交点**按确定性逻辑序串行进行，因此并发度不影响统计准确性。
- **只有命中新覆盖边的输入才会进入语料库**；重复覆盖或重复内容（按 SHA-256
  去重）的输入不会使语料膨胀。
- panic / 超时的执行不贡献覆盖，也不进入语料；其输入经最小化后记入
  `Stats.Crashes` 并持久化到崩溃目录。

## 确定性语义

- 固定 `Seed`、相同目标与种子输入时，执行序列与语料演化顺序**逐条一致**，
  且与 `Workers` 数量无关（生成只依赖固定窗口之前的提交结果）。
- `MaxExecs` 按生成序号精确截断，达到上限后先停止生成、再排空在飞任务并有序提交。
- `MaxDuration` / `MaxMemoryMB` 依赖墙钟与堆状态，截断位置随环境自然波动；
  在此之前的演化序列仍然确定。
- `fuzz.Replay(target, input, timeout)` 对同一输入在任何进程、任何并发度下
  返回相同的失败原因，崩溃与卡死均可稳定复现。

## 失败分类与最小化

| FailReason      | 含义                     |
|-----------------|--------------------------|
| `FailNone`      | 正常结束                 |
| `FailPanic`     | 目标 panic（已被隔离）   |
| `FailTimeout`   | 超过 `ExecTimeout`       |

单次执行的 panic / 超时不会传播到引擎。超时目标所在的 goroutine 只写本次执行
私有的覆盖数据，被淘汰后不留悬挂状态。每个失败输入都会经确定性最小化
（块删除 + 字节清零），输出最小可复现输入。

## 持久化与恢复

- `CorpusDir` 非空时，语料以内容哈希命名落盘（临时文件 + 原子 rename，
  已存在不重复写入）。
- 重启时自动重放磁盘语料重建覆盖率，已发现的新覆盖不丢失。
- 哈希与内容不符的损坏条目被移入 `CorpusDir/quarantine/`，不会导致启动失败。
- 崩溃输入写入 `CrashDir`（默认 `CorpusDir/crashes/`）。

## 配置项

| 字段          | 默认值   | 说明                                       |
|---------------|----------|--------------------------------------------|
| `Seed`        | 0        | 随机种子，固定后整个演化序列可复现         |
| `Workers`     | 1        | 并行执行的工作单元数量                     |
| `ExecTimeout` | 1s       | 单次目标执行超时                           |
| `MaxDuration` | 不限     | 引擎总运行时长上限                         |
| `MaxExecs`    | 不限     | 总执行次数上限（按生成序号精确截断）       |
| `MaxMemoryMB` | 不限     | 堆内存上限（MB），达到后优雅停止           |
| `MaxInputLen` | 4096     | 变异生成输入的最大长度                     |
| `CorpusDir`   | 不持久化 | 语料库持久化目录                           |
| `CrashDir`    | 见上     | 崩溃输入目录，默认 `CorpusDir/crashes`     |

达到任一上限时引擎优雅停止，`Stats.StopReason` 给出可解释的原因
（`max execs reached` / `max duration reached` / `memory limit reached`），
并输出执行数、语料规模、覆盖边数、崩溃列表等统计结果。

## 变异策略

比特翻转、字节替换、随机插入、块删除、块替换、语料拼接、边界特征值写入、
字节算术运算，共 8 种，由种子决定的随机源统一调度，调用序列确定。

## 测试

```sh
go test ./fuzz/ -count=1 -race
```

覆盖崩溃检测与最小化、超时隔离、确定性演化（跨并发度一致）、并发覆盖准确性、
语料膨胀抑制、重启恢复与损坏隔离、各类上限的优雅停止、重放稳定性。
