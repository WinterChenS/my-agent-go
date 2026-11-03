# F03 — RPC 分帧与 worker 进程监督

日期：2026-11-03。状态：**RPC01–RPC04 PASS（Windows/amd64 宿主实测）；RPC05 PARTIAL**（Windows 实测通过并验证了测试区分力，Unix 进程组路径未实机验证）。

一句话结论：LF 分帧、按 id 归属、背压隔离、进程树回收四条路径都在真实进程上跑通了；
剩下的不确定性集中在平台覆盖面（只有 Windows）与工具缺口（本机无法跑 `-race`）。

## 1. 产物

| 文件 | 作用 |
|---|---|
| `internal/runtime/pi/framing.go` | LF 分帧器；16 MiB 上限；poisoned 语义 |
| `internal/runtime/pi/rpc.go` | `RawEvent`、命令 id 分配、完成语义判定、诊断 payload |
| `internal/runtime/pi/worker.go` | `StartWorker` / `Call` / `Events` / `Close`；stderr 环形缓冲与脱敏；有界事件队列 |
| `internal/runtime/pi/process_windows.go` | Job Object：`KILL_ON_JOB_CLOSE`、`ActiveProcesses` 存活计数 |
| `internal/runtime/pi/process_unix.go` | 进程组 + `kill(-pgid)`（未实机验证） |
| `internal/runtime/pi/fakeworker/main.go` | 可脚本化假 Pi：乱序/重复/洪泛/畸形/孙进程/挂起 |
| `framing_test.go` / `worker_test.go` / `worker_integration_test.go` | 用例（详见 §3） |

## 2. 设计要点（为什么这样写）

**分帧只认 LF。** `U+2028`（E2 80 A8）是合法的 JSON 字符串内容，很多"按行读"的实现会把它当换行，
从而把一条记录拆成两条、产生两个解析失败的假事件。分帧器按字节处理，多字节 UTF-8 被 pipe 切断也不影响。
CRLF 的尾部 CR 在返回前剥离。

**超限即失败，不截断。** 累积到 16 MiB + 1 字节立即返回 `PROTOCOL_RECORD_TOO_LARGE`，并把分帧器置为 poisoned：
一旦失去记录边界，继续读只会产生错位记录，所以后续调用持续返回同一错误。

**响应走 pending map，事件走队列。** 这是背压能成立的关键：
`Call` 的响应直接投递给等待者，不经过事件队列；事件队列（上限 4096）满了就丢最旧的并计诊断。
因此消费者（adapter/UI）变慢或完全不读，也不会把 stdout 读循环堵死——否则子进程写满管道后会整体卡死。

**命令 id 由 worker 分配。** 调用方传入的 `id` 会被覆盖成 `myagent-<n>`，单调递增、永不复用。
复用 id 会让迟到的上个响应被误归属到新命令，这是最难排查的一类串话。

**未知记录一律保留 + 诊断。** 畸形 JSON、缺 `type`、无主 `response.id` 都产出 `protocol_diagnostic` 事件；
未知 `type` 的记录原样透传给上层。`ReadSettledOutcome` 只认 `agent_settled`，未知 type 一律 `Known=false`，
`IsCompleted()` 因此不可能返回 true——这条把"未知完成语义不得瞎猜 completed"落成了代码而不是口号。

**Close 的顺序有实测依据。** F02 发现：`agent_settled` 后立刻关 stdin 会让 Pi 在退出阶段触发
`Assertion failed: !(handle->flags & UV_HANDLE_CLOSING)` 并崩溃（exit 3221226505），宽限 800 ms 后 8/8 正常。
所以顺序固定为：等 stdout 静默 → 关 stdin → 等退出 → 超时才强杀进程树 → 校验 owned 进程清空。

**进程树按"own"回收，不按名字杀。** Windows 用 Job Object（只有被 assign 的进程及其子孙在 job 内），
`ActiveProcesses` 就是精确的 owned 计数；Unix 用独立进程组。按可执行文件名杀会误伤用户自己的 Pi 和其他会话。

## 3. 验收逐条

| 用例 | 覆盖 | 结果 | 证据 |
|---|---|---|---|
| RPC01 | LF 随机分块 / CRLF / U+2028 / UTF-8 / 128 KiB / 16 MiB 边界 / 不完整尾记录 / 空行 | **PASS** | `raw/f03-framing.log` |
| RPC02 | 并发乱序响应按 id 归属；重复 id 不误投；取消/超时后无悬挂 Call | **PASS** | `raw/f03-worker-unit.log` |
| RPC03 | stdout 4 MiB 洪泛期间命令可达；stderr 8 MiB 洪泛不阻塞 stdout；缓冲不超过 1 MiB 且标记截断 | **PASS** | `raw/f03-worker-unit.log` |
| RPC04 | 畸形 JSON / 缺 type / 未知 type / 无主 response 均有诊断且不崩溃；未知完成语义不判 completed | **PASS** | `raw/f03-worker-unit.log` |
| RPC05 | Close 后 owned 孙进程被回收，无关同类进程仍存活 | **PARTIAL** | `raw/f03-worker-unit.log`、`raw/f03-jobobject-discrimination.log` |
| 真实 Pi 端到端 | 真实 Pi 1.5.0 + fixture provider：`get_state` → `prompt` → `agent_settled` → Close exit 0 | **PASS** | `raw/f03-realpi-worker.log` |

实际命令与退出码：

```text
# 首个失败用例（实现前，7 个子用例全红；原始输出 raw/f03-first-failure.log）
go test ./internal/runtime/pi -run 'TestLFFramingUnicodeAndLargeRecord' -count=1  → exit 1
# 实现后
go test ./internal/runtime/pi -run 'TestLFFramingUnicodeAndLargeRecord' -count=1  → exit 0
go test ./internal/runtime/pi -run 'TestOutOfOrder|TestDuplicate|TestCancelled|TestStdoutBackpressure|TestStderrFlood|TestUnknownAndMalformed|TestWorkerTerminatesGrandchild|TestCloseRejects|TestCallRejects' -count=1  → exit 0（16.8s）
MYAGENT_PI_WORKER_INTEGRATION=1 go test ./internal/runtime/pi -run TestWorkerWithRealPi -count=1  → exit 0（3.1s，15 条事件，exit 0）
go test ./... -count=1  → exit 0
gofmt -l .  → 空
go vet ./...  → exit 0
```

真实 Pi 用例的关键断言（不是"跑起来了"就过）：

- `get_state` 响应的 `id` 是 `myagent-1`，**不是**调用方传入的 `caller-supplied` → 证明 id 由 worker 分配；
- `prompt` 的 `data.disposition == "started"`，但**没有**据此判完成；
- 必须等到 `agent_settled` 且 `aborted=false` 才 `IsCompleted()`；
- `Close` 返回 nil（没有 `CLEANUP_FAILED`），进程退出码 0；
- 事件流里确实出现 `text_delta`（排除"空跑但事件齐全"的假通过）。

## 4. 复核：Job Object 测试有没有区分力

全绿最容易骗人的地方是"这条用例就算实现错了也会绿"。所以做了一次对抗验证：
临时让 `attach` 跳过 `AssignProcessToJobObject`，重跑孙进程用例。

```text
worker_test.go:399: owned 孙进程在 Close 后仍存活（心跳 20 → 77 字节）
--- FAIL: TestWorkerTerminatesGrandchild (9.20s)
```

失败信息同时说明两件事：孙进程确实会脱离父进程继续写心跳（测试前提成立），
且 `Close` 的成功路径**依赖** job 真的把子孙收走——不是无条件返回 nil。
验证后已恢复代码，钩子未留在仓库里（`raw/f03-jobobject-discrimination.log`）。

## 5. 限制与未验证项（不要在报告里省略）

1. **只有 Windows/amd64。** `process_unix.go` 未经实机运行；`TestWorkerTerminatesGrandchild` 在非 Windows 上直接 skip。
   RPC05 的 scope 是 ALL，因此只给 PARTIAL，不给 PASS。
2. **`-race` 未执行。** 本机无 gcc/mingw，`go test -race` 报 `requires cgo`（`raw/f03-race-unavailable.log`）。
   并发正确性靠设计与用例保证，**不能声称"已排除数据竞争"**。
3. **Job Object assign 存在窄窗口。** 进程 `Start` 之后、`AssignProcessToJobObject` 之前，理论上可抢先派生逃逸子孙。
   Pi 启动要先读配置与扩展才会拉起 MCP，窗口远小于启动耗时；但这是"概率低"而非"不可能"。
   若 assign 失败（例如宿主本身在不允许嵌套的 Job 里），`Limitations()` 会带出说明，`Close` 不会宣称清理成功。
4. **Unix 进程组抓不到 `setsid` 逃逸。** Pi 与 MCP server 不这么做，但方案本身不保证。
5. **stderr 脱敏是尽力而为。** 只覆盖 `sk-`、`Bearer`、`api_key/token/secret` 赋值、`C:\Users\<name>`、`/home/<name>` 五类规则；
   自定义格式的凭证仍可能落进那 1 MiB 环形缓冲。真正的边界是"不要把密钥写进日志"，不是脱敏规则。
6. **fakeworker 只证明异常时序可复现，不证明 Pi 的行为。** Pi 的真实行为以 F02 的 `testdata/v1.5.0/*.jsonl` 为准。
