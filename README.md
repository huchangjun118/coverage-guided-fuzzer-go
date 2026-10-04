# coverage-guided-fuzzer-go

一个覆盖率引导的模糊测试引擎：注册被测目标后，引擎自动变异输入、收集覆盖率反馈、
进化语料库，并把崩溃最小化为可稳定复现的输入。给定相同种子与初始语料，运行结果
（语料演化顺序、执行序列、崩溃与统计）可逐条复现。

## 快速开始

```go
eng := fuzz.New(fuzz.Config{
    Seed:          42,
    Workers:       4,
    ExecTimeout:   100 * time.Millisecond,
    MaxDuration:   30 * time.Second,
    MaxExecutions: 100000,
    MaxInputLen:   4096,
    MaxMemoryMB:   1024,
    CorpusDir:     "fuzzdata",
}, func(input []byte, cov *fuzz.Coverage) {
    cov.Hit(0)                 // 在分支处上报覆盖
    if len(input) > 0 {
        cov.Hit(1)
        parse(input, cov)      // 被测目标
    }
})
stats := eng.Run([][]byte{[]byte("seed")})
fmt.Println(stats)
```

也可运行自带示例：`go run ./cmd/fuzzdemo -max-exec 200000`

## 覆盖率反馈语义

- 目标通过 `cov.Hit(id)` 上报覆盖边（edge）。`id` 应为由插桩分配的稠密小整数，
  每个分支/基本块一个 id。
- 每次执行在独立的覆盖集合中记录本次命中的边；执行结束后由**唯一的协调者
  goroutine 按序列号顺序**把结果原子合并进全局覆盖集（`countNewInto` 在锁内完成
  判重与合并），因此并发执行下"新边"统计既不遗漏也不重复计数。
- **只有命中至少一条新边的输入才进入语料库**；重复覆盖的输入被丢弃，语料规模
  以可达边数为上界，不会无限膨胀。
- 崩溃（panic，被引擎 recover）与超时（超过 `ExecTimeout`）不会进入语料；
  崩溃会被确定性最小化（删块 + 字节归零，保持崩溃谓词）后写入 `crashes/`。

## 确定性模型

- 第 `i` 次变异的随机性只取决于 `(Seed, i)` 与当时的语料状态。
- 结果按序列号顺序合并；工作协程可以乱序完成，但乱序结果会被缓冲、按序应用。
- 派发窗口是与 Worker 数无关的固定常数，因此**不同并发度下语料演化序列逐条一致**。
- `Replay(input)` / `ReplayCorpusEntry(path)` 在任意进程、任意并发度下对同一输入
  给出相同的 `Outcome`（崩溃含 panic 信息，超时由 `ExecTimeout` 判定）。

## 执行隔离与超时淘汰

- 每次执行都有 panic recover 与超时定时器；崩溃、超时、异常都不会带崩引擎，
  且以可区分的 `Outcome`（`ok` / `crash` / `timeout`）上报。
- 超时后工作槽位立即释放，卡死输入不拖垮整体吞吐；引擎簿记（槽位、统计、
  派发状态）不留下悬挂状态。注意：Go 无法强杀 goroutine，真正死循环的目标
  代码会遗留一个不再被引擎引用的 goroutine——引擎自身状态始终一致。
- 停止时关闭任务通道并排空在途结果，所有 worker 正常退出后才返回。

## 持久化与断点恢复

`CorpusDir` 下的布局：

```
corpus/<sha256>      语料条目，按内容哈希命名（写入幂等，重启不会重复）
crashes/<sha256>     最小化崩溃复现输入
crashes/<sha256>.txt 崩溃信息
quarantine/<name>    损坏条目隔离区
```

- 所有写入先写临时文件再 rename，中断不会留下半写文件（遗留的 `.tmp`
  会在下次加载时被隔离）。
- `LoadCorpusInputs(dir)` 加载语料：校验内容哈希，损坏/错名条目移入
  `quarantine/` 而不是导致启动失败；把返回值作为 `Run` 的种子即可从
  中断处恢复，已发现的覆盖不会丢失，也不会重复写入。

## 配置项（Config）

| 字段 | 默认 | 含义 |
|---|---|---|
| `Seed` | `1` | 随机种子；种子 + 初始语料相同则整个运行逐条可复现 |
| `Workers` | `4` | 并行执行单元数（共享语料与覆盖率；不影响演化序列） |
| `ExecTimeout` | `100ms` | 单次执行超时，超时记为 `timeout` 并淘汰该输入 |
| `MaxDuration` | `30s` | 总时长上限，达到后优雅停止（`max_duration_reached`） |
| `MaxExecutions` | 不限 | 变异执行次数上限（`max_executions_reached`） |
| `MaxInputLen` | `4096` | 变异输入长度上限 |
| `MaxMemoryMB` | `1024` | 进程堆内存上限（MiB），执行间检查，超限优雅停止（`memory_limit_exceeded`） |
| `CorpusDir` | 空 | 语料/崩溃持久化目录；空表示仅内存 |

## 统计输出（Stats）

`Executions`（总执行数）、`CorpusSize`（语料条目数）、`EdgesTotal`（覆盖边总数）、
`Crashes`、`Timeouts`、`Duration`、`StopReason`（可解释的停止原因）。

## 测试

```
go test ./fuzz/... -race
```

覆盖场景：崩溃隔离与最小化、超时淘汰、跨并发度的确定性演化与重放、
语料去重上界、并发覆盖统计准确性、损坏条目隔离与重启恢复、
执行次数/内存/总时长三类上限的优雅停止。
