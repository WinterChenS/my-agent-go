# F04 工具通道策略对照（SEC03.runtime 的五条通道）

本文记录"每一个能让真实副作用发生的工具通道，是否都在**执行前**经过授权"的实测结论。
对应验收项：`docs/acceptance/v1-gates.md` 里的 SEC03 ——

> 自有和外部 MCP、扩展、嵌套 tool 路径均执行前授权；未覆盖通道禁用且实际不能调用。

## 1. 为什么这件事必须逐条测

策略拦截落在 Pi 的 `tool_call` 事件钩子上（`pi-extensions/harness-policy.ts`）。
钩子只有一个，但**执行路径不止一条**：

- 内置工具（`read`/`write`/`edit`/`bash`）走 Pi 自己的工具实现；
- MCP 工具由 MCP 客户端的 handler 执行，参数结构由远端 server 定义；
- 扩展工具由扩展自己注册的 `execute()` 执行，参数结构由扩展自定义；
- `codemode` 在 QuickJS 沙箱里用 `tools.<name>(...)` 再调一次工具——**内层调用不经过模型**。

"内置工具被拦住了"不能推出"扩展工具也被拦住了"；"顶层工具调用被拦住了"更不能推出
"沙箱里的嵌套调用也被拦住了"。任何一条没测过的通道，都等于一条未经检验的安全边界。

因此本文的判据一律落在**副作用**上（文件是否存在 / 内容是否改变 / MCP handler 是否收到
`tools/call`），而不是界面文案或日志里出现的字符串——工具参数本身就含文件名，
"被拦"和"跑了"在日志里长得一模一样。

## 2. 被测物与运行条件

| 项 | 值 |
|---|---|
| Pi | v1.5.0（`.tools/pi/v1.5.0`），`--mode rpc` |
| 模型 | 受控 fixture provider（`internal/fixtures/openaisse`），脚本化回放，不访问真实模型 |
| 策略扩展 | `pi-extensions/harness-policy.ts`，显式 `-e` 加载 |
| 策略端点 | `internal/policybridge`，每用例独立启动，127.0.0.1 随机端口 + 随机令牌 |
| 隔离参数 | `-na -nc -ne -ns -np --no-themes` + `--no-mcp`（或 `-e builtin:mcp`） |
| 平台 | Windows x64（`go test` 直接跑在宿主上） |

审批结论由用例在收到 `OnPending` 回调后经端点提交，模拟人工点击"允许一次 / 拒绝"。
每条用例同时断言 **审批请求确实发生过**（`pending > 0`）——
否则"拒绝后没产生副作用"可能只是"工具根本没被调到"，那就不是证据。

## 3. 通道矩阵（实测结果）

| 通道 | 用例 | 拒绝后 | 允许后 | 判据 |
|---|---|---|---|---|
| 内置 `bash` | `TestRealPiPolicyBlocksToolAndProjectResources` | `tool-ran.txt` 不存在 | 存在 | 文件系统 |
| 内置 `write` | `TestRealPiPolicyBlocksWriteAndEditChannels` | `write-ran.txt` 不存在（待审批 1 次） | 存在（待审批 1 次） | 文件系统 |
| 内置 `edit` | 同上 | 内容保持 `before\n` | 内容变 `after\n` | 文件内容 |
| 外部 MCP | `TestRealPiPolicyBlocksToolAndProjectResources` | handler **未收到** `tools/call`（待审批 1 次） | handler 收到 `tools/call` | fixture MCP 请求日志 |
| 扩展工具 | `TestRealPiPolicyBlocksExtensionChannel` | `extension-ran.txt` 不存在（待审批 1 次） | 存在，内容 `extension-tool-ran` | 文件系统 |
| `codemode` 嵌套 | `TestRealPiPolicyBlocksCodemodeNestedChannel` | 见下表 | 见下表 | 文件系统 + 按工具拆分审批数 |
| 自有 MCP（未支持） | `TestRealPiPolicyBlocksToolAndProjectResources` | 见 §4 | — | 审批数 + handler 日志 |

### codemode 嵌套：外两层必须分开给结论

`codemode` 的外层调用与脚本内的 `tools.bash(...)` 是**两个**独立的审批请求。
如果两层用同一个结论，"外层被拒"会让脚本根本没跑起来，嵌套通道等于没被测到——
会得到一个"看起来通过、实际没覆盖"的假绿。

| 场景 | 审批请求（按工具） | `codemode-ran.txt` |
|---|---|---|
| 外层拒绝 | `{codemode: 1}` | 不存在 |
| 外层允许 + 内层 `bash` 拒绝 | `{codemode: 1, bash: 1}`（共 2 次） | **不存在** |
| 两层都允许 | `{codemode: 1, bash: 1}`（共 2 次） | 存在，内容 `codemode\n` |

中间一行是关键：脚本确实跑起来了（否则不会有第二次审批），而它调起的 `bash`
被单独拦下，副作用没有发生。

原始证据：`docs/evidence/raw/f04-policy-codemode-nested.json`。

## 4. 未支持的通道：禁用 + 验证不可调用

`internal/harnessmcp`（自有 MCP server）本期没有实现，因此"自有 MCP 通道"当前不存在。
验收计划对这类通道的要求不是"策略会拦住它"，而是**从工具列表里禁用并验证不可调用**
（`docs/superpowers/plans/2026-11-03-foundation-runtime.md` §F04）。

两者的差别很实际：策略拦截说明工具还在，安全依赖"钩子实现永远正确"；
工具根本不在工具表里，模型连发起的机会都没有。

实测（`agentDir/mcp.json` 保持存在，只是不启用 MCP）：

| 场景 | 审批请求 | handler 是否收到 `tools/call` | mcp.json |
|---|---|---|---|
| `-e builtin:mcp` + 拒绝 | 1 | 否 | 存在 |
| `-e builtin:mcp` + 允许 | 1 | 是 | 存在 |
| `--no-mcp` | **0** | 否 | 存在 |

`--no-mcp` 下审批次数为 **0**，说明工具不在工具表里（不是"在、但被拦"）。
原始证据：`docs/evidence/raw/f04-policy-integration.json` 的 `mcp.disabled` 段。

## 5. 过程中发现并修掉的两个真实缺陷

1. **相对路径解析错位（已修）**。策略扩展把 `write`/`edit` 的 `path` 原样发给 Go 侧，
   Go 用**策略进程**的 cwd 做 `filepath.Abs`，与工具实际的 cwd（用户工作区）不是同一个目录。
   症状是首次跑 `TestRealPiPolicyBlocksWriteAndEditChannels` 时 `write` 待审批 0 次：
   路径被判越界、直接 deny，**根本没进审批**。修复方式是在 TS 侧用
   `path.resolve(process.cwd(), raw)` 定死绝对路径。
   这个 bug 的方向是先误拒，但如果两个 cwd 恰好有交集就会变成误放——属于必须修的那类。

2. **证据采集时机错（已修）**。首版证据在跑完全部场景后才读文件，
   `deny` 记录显示 `fileCreated: true`、内容 `after`——读到的是最后一轮的最终状态。
   改为每个场景结束立刻取快照。这不是实现缺陷，是**证据方法**缺陷：
   它会让结论往"更安全"的反方向错，同样不可接受。

## 6. 限制（如实记录）

- **自有 MCP server 未实现**：§4 覆盖的是"禁用后不可调用"，不是"自有 MCP 通道的执行前授权"。
  `internal/harnessmcp` 落地后，本节必须补一条真实对照，届时本表才完整。
- **扩展工具 fixture 是自己写的**：`tests/fixtures/extension/fixture-tool.ts`。
  它证明的是"扩展注册的工具同样过钩子"，不等于"第三方扩展的所有写法都安全"
  （例如扩展在 `session_start` 里直接写文件，不经过工具调用——那是扩展自身的权限问题，
  不在本项范围内）。
- **`codemode` 需显式启用**：`-ne` 会关掉内置扩展，必须 `-e builtin:codemode`；
  该工具 `defaultActive: false`，还要 `--tools +codemode`。两句缺一不可。
  只写 `--tools` 会得到"工具不存在"的假失败——首跑就是这样失败的。
- **单平台**：本文全部结论来自 Windows x64。mac/linux/arm 未在本轮验证。
- **fixture 模型固定文案**：模型输出是脚本化的，"模型主动要求执行某个工具"这件事是
  构造出来的，不代表真实模型的行为分布。

## 7. 复现

```bash
# 需要 node 与 .tools/pi/v1.5.0
export PATH="/d/Program Files/nodejs:$PWD/.tools/bin:$PATH"
MYAGENT_PI_POLICY=1 go test ./internal/runtime/pi -run 'TestRealPiPolicy' -v -count=1

# 全套（24 条必需 case + 证据树）
./scripts/devtools.sh verify --suite=integration --target=win-x64
```

证据树落到 `docs/evidence/runs/<rev>/win-x64/<CASEID>/`，按 revision 分目录，
属于现场生成的产物，不入库（见 `.gitignore`）。
