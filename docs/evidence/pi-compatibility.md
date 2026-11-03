# F02 证据：Pi 定位、探测与真实协议基线

日期：2026-11-03。状态：**PI01 / PI02 PASS（宿主范围）；PI03 PARTIAL；PI04 BLOCKED**。

一句话结论：真实 Pi **1.5.0** 已完成 RPC / MCP / extension 三项探测，原始流全部可追溯；
但**成功的上下文压缩未能诱发**，且实测发现三处与官方文档不一致的行为（见 §4），
因此 PI03 不标 PASS。

## 1. 产物

| 文件 | 内容 |
|---|---|
| `internal/runtime/pi/compatibility.go` | 类型（PiInstallation/ProbeState/Origin/ProbeResult/Capability）、版本策略 `DefaultPolicy`（仅认证 1.5.0）、`BuildLaunch`（按扩展名推导 argv，不拼 shell 字符串）、协议 fixture 目录哈希 |
| `internal/runtime/pi/locator.go` | `Locate`：用户指定 → PATH → 常见位置 → 托管目录；中文/空格路径；`pi.exe/.cmd/.bat` 优先、仅 `.ps1` 给诊断且标不可执行；按真实路径去重；失效的已保存路径返回 NotFound 但保留其它结果 |
| `internal/runtime/pi/probe.go` | `Probe`：隔离 `HOME/USERPROFILE/APPDATA/PI_CODING_AGENT_DIR` 后只跑 `--version`，不进 agent loop、不请求模型、不启动用户 MCP |
| `internal/runtime/pi/protocol_capture_test.go` | 真实 Pi 捕获装置（`MYAGENT_PI_CAPTURE=1` 才跑）：9 个场景、分批下发、关闭宽限期 |
| `internal/runtime/pi/protocol_golden_test.go` | 只读 testdata 的结构与序列断言，**不依赖本机装 Pi** |
| `internal/runtime/pi/testdata/v1.5.0/*.jsonl` | 9 份真实捕获记录（265 条，共 1567 KB） |
| `internal/fixtures/openaisse/` | 受控 OpenAI 兼容 SSE 端点（可复用：CLI 与测试共用） |
| `tests/fixtures/provider/main.go` | 上述端点的命令行壳 |
| `tests/fixtures/mcp/main.go` | 受控 MCP server（stdio，JSON-RPC；initialize / tools/list / tools/call） |
| `tests/fixtures/mcp/main_test.go` | MCP 黑盒测试（真的起进程走 stdio，测 flush 时序与通知无响应） |
| `tests/fixtures/extension/fixture.ts` | 薄 extension，只注册一个命令，用于验证加载链路 |
| `internal/runtime/pi/locator_test.go` / `probe_integration_test.go` / `realhost_test.go` | PI01 / PI02 用例 |

## 2. PI01 / PI02：真实宿主实测

原始日志：`docs/evidence/raw/f02-realhost.log`。

```text
MYAGENT_PI_REAL=1 go test ./internal/runtime/pi -run TestRealHostPi -count=1 -v
exit=0

发现 Pi: version="1.4.0" origin=external source=path arch=any runnable=true
        path=C:\Users\Administrator\AppData\Local\pi-node\current\pi.cmd
探测 ...\pi.cmd => state=UnsupportedVersion version="1.4.0"
        diagnostics=[版本 1.4.0 未经认证（基线仅认证 [1.5.0]）；标 unverified，不自动运行]
```

宿主 PATH 上的 Pi 是 **1.4.0**（npm `.cmd` shim），探测成功但**判为未认证**，
符合基线 §3「未知版本 unverified，不静默承诺可用」。没有把它标 Ready。

PI02 的 oracle 是 marker 文件哈希：隔离 agentDir 下 probe 前后用户配置哈希不变，
且不生成 `mcp.json`/`auth.json` 之类副作用文件。

## 3. PI03：真实 Pi 1.5.0 协议基线

### 3.1 环境与方法

| 项 | 实际值 |
|---|---|
| Pi 版本 | **1.5.0**（`--version` 实测输出 `1.5.0`） |
| 安装位置 | `.tools/pi/v1.5.0/`（项目内隔离副本，未动全局 npm 前缀与 `pi-node/current`） |
| 启动方式 | `node dist/bundle/cli.js --mode rpc --session-dir <tmp> --provider fixture --model fixture-model` |
| provider | 本地 Go 端点，实现 OpenAI Chat Completions 流式协议；**完全离线**，由 prompt 里的 `[[scenario:x]]` 标记选回放脚本 |
| agentDir | 每场景独立临时目录（`PI_CODING_AGENT_DIR`），不触碰用户 `~/.pi/agent` |
| session-dir | 每场景独立临时目录（不用 `--no-session`，原因见 §3.5） |

选择"真实 Pi + 受控 provider"而不是 fake-pi：走的是 Pi 内置 `openai-completions` 实现的
完整请求转换、工具拼装与 usage 结算链路，满足"mock 不得替代"的要求；同时 provider 侧
不请求任何真实模型，可重复执行。

### 3.2 捕获结果

`MYAGENT_PI_CAPTURE=1 go test ./internal/runtime/pi -run TestCaptureRealPiProtocol -count=1 -v`
→ **9/9 场景 PASS，总耗时 80.46s**（原始日志：`docs/evidence/raw/f02-capture.log`）。

| 场景 | 记录数 | 体积 | 诱发目标 |
|---|---|---|---|
| `text` | 17 | 94 KB | 基础轮次、`disposition=started` |
| `unicode` | 19 | 94 KB | 多字节 / emoji / **U+2028** / CRLF 分帧 |
| `tool` | 32 | 99 KB | 工具调用 → 执行 → 收尾的完整序列 |
| `empty` | 12 | 92 KB | 空响应仍进入 idle |
| `provider-error` | 37 | 98 KB | 500 触发的自动重试 |
| `long` | 44 | 434 KB | 超长单轮响应 |
| `mcp` | 16 | 94 KB | MCP 工具暴露（需查请求侧，见 §4.2） |
| `extension` | 17 | 121 KB | 薄 extension 的命令注册 |
| `compaction` | 71 | 441 KB | 压缩事件字段（实测失败，见 §3.5） |

记录只做了一件事：把本机绝对路径替换为 `<REPO_ROOT>`。原始未脱敏版本见
`docs/evidence/raw/`。校验过：9 个文件均含占位符、均不含本机仓库路径。

### 3.3 实测事件序列（`tool` 场景，单一轮次内含文本 + 工具）

```text
response(prompt, disposition=started)
→ agent_start → turn_start
→ message_update/text_start → text_delta
→ message_update/toolcall_start → toolcall_delta ×2 → text_end → toolcall_end
→ tool_execution_start → tool_execution_update ×2 → tool_execution_end
→ turn_end
→ turn_start → text_start → text_delta → text_end → turn_end
→ agent_end → agent_settled{aborted:false}
```

三个对 F03 直接有用的事实：

1. **同一 assistant 消息里文本与工具调用并存**，且 `text_end` 出现在 `toolcall_end` **之前**。
2. **工具结果回传后会开启第二个 `turn_start`**，不是同一轮内继续。
3. `agent_settled` 才是"不再自动继续"的信号；`agent_end` 之后仍可能有重试/压缩。

### 3.4 关键字段（全部来自实测，非文档抄录）

| 记录 | 字段 |
|---|---|
| `response` | `id` / `type=response` / `command` / `success` / `data` / `error` |
| `prompt` 响应 | `data.disposition = "started"` |
| `message_update` | `assistantMessageEvent.type` ∈ {`text_start`,`text_delta`,`text_end`,`toolcall_start`,`toolcall_delta`,`toolcall_end`}，带 `contentIndex`、`delta`、`content` |
| usage | `input` / `output` / `cacheRead` / `cacheWrite` / `reasoning` / `totalTokens` / `cost{...}` |
| `agent_settled` | `aborted: bool` |
| `compaction_start` | `reason`（实测 `"manual"`） |
| `compaction_end` | `reason` / `aborted` / `willRetry` / `errorMessage` |
| 重试 | `auto_retry_start` / `entry_appended` / `auto_retry_end` |
| 工具执行 | `tool_execution_start` / `tool_execution_update` / `tool_execution_end` |

### 3.5 未能诱发：成功的上下文压缩（如实记录）

`compaction` 场景稳定产出：

```json
{"type":"compaction_start","reason":"manual"}
{"type":"compaction_end","reason":"manual","aborted":false,"willRetry":false,
 "errorMessage":"Compaction failed: Nothing to compact (session too small)"}
```

**五次尝试全部失败**（原始日志：`docs/evidence/raw/f02-compaction-attempts.log`）：

| # | 配置 | 结果 |
|---|---|---|
| 1 | `--no-session`，1 轮常规 | `session too small` |
| 2 | 隔离 `--session-dir`，1 轮常规 | `session too small` |
| 3 | 1 轮大 usage（prompt=7600） | `session too small` |
| 4 | 4 轮大 usage | `session too small` |
| 5 | 3 轮长正文（会话 1.1 MB） | `session too small` |
| 6 | 3 轮 bulk（长正文 + 大 usage，451 KB） | `session too small` |

已排除的解释：不是轮次不够、不是会话体积不够、不是 usage 报得太小、不是 `--no-session` 缺失。
`compaction.md` 给出的阈值是 `keepRecentTokens=20000` / `reserveTokens=16384`，
但按这两个口径上述条件都应满足。**根因未定位**，不做推测性结论。

因此：`compaction_start` / `compaction_end` 的**字段结构**已捕获可用于实现，
但"成功压缩"的完整流（含 `summary`、`firstKeptEntryId`、`tokensBefore`）**没有证据**。
`protocol_golden_test.go` 对这条断言的是"失败字段存在"，不是伪造成功。

## 4. 与官方文档不一致的三处实测行为

这三条是本任务最有价值的产出，直接约束 F03/F04 的实现方式。

### 4.1 `--no-extensions` 会连带不加载 MCP

文档只说它禁用扩展发现与内置扩展。实测：加上该开关后，Pi 完全不读 `mcp.json`，
MCP 工具不会出现在请求的 `tools` 数组里；去掉后立刻出现 `mcp__fixture__fixture_echo`。

复现证据：`docs/evidence/raw/f02-mcp-not-exposed-before-fix.log`（加开关，工具只有
`[read bash edit write]`）对比 `f02-capture.log` 中 `mcp` 场景 PASS。

影响：F04 的 Harness MCP 不能被 `--no-extensions` 误伤。捕获装置因此按场景决定是否带该开关。

### 4.2 MCP 工具默认不声明给模型（`exposure` 默认 `codemode`）

`mcp.json` 不写 `exposure` 时，工具既不声明给模型、也不写进 codemode 描述，
只能被脚本 `searchTools()` 找到。要验证"工具是否暴露给模型"必须显式 `"exposure": "direct"`。

配置本身可先用 `pi mcp list` 独立确认（原始日志 `docs/evidence/raw/f02-mcp-list.log`）：

```text
fixture: connected, 1 tool (direct, global)
  tools: fixture_echo
```

### 4.3 `agent_settled` 后立即关 stdin 会让 Pi 在退出阶段崩溃

对照实验（`MYAGENT_PI_CLOSE_GRACE` 控制 `agent_settled` 到关 stdin 的间隔）：

| 宽限期 | `tool` / `long` 各跑次数 | 结果 |
|---|---|---|
| 0 s | 2 + 2 | **全部崩溃**，退出码 `3221226505`（0xC0000409） |
| 800 ms | 2 + 2 | 全部正常退出 |
| 3000 ms | 2 + 2 | 全部正常退出 |

崩溃时 stderr：

```text
Assertion failed: !(handle->flags & UV_HANDLE_CLOSING), file src\win\async.c, line 94
```

这是 libuv 在 Windows 上的异步句柄重复关闭断言。发生在**进程退出阶段**，此时 stdout 记录
已完整（`agent_settled` 已收到），所以**不影响捕获内容，但影响退出码**。

原始日志：`docs/evidence/raw/f02-close-grace-experiment.log`。

影响：F03 的 `Worker.Close` **不能**在 `agent_settled` 后立刻关 stdin，
必须先等 stdout 静默（宽限期），否则会把"正常完成"误判为"异常退出"。
这条必须写进 RPC 退出语义的用例里，否则 G0 的门禁会被一个假故障反复卡住。

## 5. PI04：BLOCKED

| 项 | 缺什么 |
|---|---|
| 无 Node 环境的官方独立 Pi 包 | 本机只有 Windows x64 一台机器，没有"无 Node 目标环境"可验证；也未获取官方独立发行包 |
| npm 安装形式 | 已记录（`.tools/pi/v1.5.0` 即 npm 形式，`pi.cmd` shim 实测可执行） |

独立包能否加载本项目薄 extension 与 MCP，**没有验证**，不标 Ready。

## 6. 执行过程与纪律（如实记录）

- **先红后绿有真实证据**：子 Agent 先写测试并运行，原始失败输出见
  `docs/evidence/raw/f02-first-failure.log`：
  `TestLocateWindowsShimWithSpaces/PreferredTakesPriority`（指定路径未排首位）、
  `TestProbeHasNoUserConfigSideEffects`（state 为空）、`TestProbePs1NotDirectlyExecutable`。
- 子 Agent 被限流中断后由主 Agent 接管，`locator.go` 与测试为复核版。
- 编译期冲突：子 Agent 留下的 `compatibility.go`/`probe.go` 与并行写的 `types.go`
  出现 `Origin`/`ProbeState`/`PiInstallation` 重复声明，统一到 `compatibility.go` 后删除 `types.go`。
- **捕获装置自身出过两个真缺陷，都是先看到异常数据才发现的**：
  1. fixture 每轮都回 `tool_calls` → Pi 无限执行工具，60s 内积累 **2081 条记录 / 122 轮**。
     修正为"收到 `role=tool` 结果后收敛为文本"，降到 32 条。golden 测试新增
     "`toolcall_start` 必须恰好 1 次"来钉死这个回归。
  2. `NewHandler` 没建记录目录，`WriteFile` 静默失败，表现为"请求目录为空"，
     差点误判成 Pi 没发请求。修正后才发现工具列表原本是 `[read bash edit write]`。
- 压缩尝试共 6 次全部失败，未用合成数据代替，见 §3.5。

## 7. 复核要点与遗留风险

- `Locate` 全程 `exec.CommandContext(路径, 参数...)` / `os.Stat`，没有 `cmd /c` 或字符串拼接；
  中文与空格路径用例真实跑通（已单独实测 Go 在 Windows 可直接执行 `.cmd`）。
- `.ps1` 不给"看起来能用"的假象：`Runnable=false` 且 Note 说明执行策略约束。
- `protocol_golden_test.go` 与捕获测试分离：前者任何机器都能跑（CI 可用），
  后者需要本机装 Pi 1.5.0（默认 skip）。这样 golden 断言不会因为环境缺失而静默失效。
- **遗留风险 1**：成功压缩无证据。F05 的压缩恢复路径在设计阶段就缺一份可信流样本。
- **遗留风险 2**：`--no-extensions` 与 MCP 的耦合是实测行为，未确认是否为 Pi 的有意设计；
  升级 Pi 版本时必须重新验证此项。
- **遗留风险 3**：关闭时序崩溃只在 Windows + Node 24.16.0 + Pi 1.5.0 上复现，
  其他平台未验证；F03 需要按"未验证"处理，不能假设 Linux/macOS 同样表现。
