# C04 MCP 配置、诊断与统一工具目录 — 执行证据

- 日期：2026-11-04（本机 Windows 10 x64）
- 源码状态：分支 `feat/v1-foundation`，HEAD `fe603e0` + **未提交的 C04 工作区改动**
  （另有 B00 的未提交改动；本任务没有写 commit，按纪律留给复核者）
- 验收目标：MCP01–MCP05、RES04（仅 win-x64；其余五目标未验证）
- 结论：**win-x64 上 MCP01–MCP05 的功能路径有真实 Pi 证据，RES04 的"工具启停落到
  runtime"有真实 Pi 负例**。六目标 ALL 范围未满足，不宣称全矩阵 PASS。
- 本文件没有改写 `docs/evidence/raw/**`；测试产物全部落在 `t.TempDir()`。

## 1. 实际命令与退出码

| 命令 | 退出码 | 结果 |
|---|---|---|
| `go build ./...` | 0 | 全仓可构建（含 B00 工作区改动） |
| `go vet ./internal/... ./tests/fixtures/mcp/` | 0 | 无输出 |
| `go test ./internal/... -count=1` | 0 | 17 个包全绿 |
| `go test ./internal/resources -count=1` | 0 | 0.6s（跳过真实 Pi） |
| `go test ./internal/app -count=1` | 0 | 0.5s |
| `MYAGENT_PI_WORKER_INTEGRATION=1 go test ./internal/resources -count=1 -timeout 900s` | 0 | 83.9s（C03 技能 + C04 MCP 真实 Pi） |
| `MYAGENT_PI_WORKER_INTEGRATION=1 go test ./internal/resources -run 'TestRealPiStdioMCPDiscoveryAndCall\|TestRealPiHTTPMCPDiscoveryAndCall\|TestDisabledMCPToolCannotExecute\|TestRealPiDiagnose' -count=1 -v -timeout 600s` | 0 | 37.8s，见 §3–§6 |
| `go test ./tests/fixtures/mcp -count=1` | 0 | fixture 协议测试仍绿 |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend run typecheck` | 0 | 无错误 |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend test` | 0 | 15 文件 / **109** 用例通过（C04 新增 17 条） |

真实 Pi：`C:\Users\Administrator\AppData\Local\pi-node\current\node.exe`（v22.23.2，PATH 解析）
+ `D:\project\my-agent-go\.tools\pi\v1.5.0\node_modules\@earendil-works\pi-coding-agent\dist\bundle\cli.js`
（Pi 1.5.0）。**注意**：这是 C02/C03 测试沿用的 PATH node（v22），不是基线的
`D:\Program Files\nodejs\node.exe`（v24）；产品运行时用设置里的 Pi/Node，不受此影响。
真实 Pi 会话的实际 argv（从测试打印，非推断）：

```text
node.exe <pi-1.5.0>\dist\bundle\cli.js --mode rpc -na -nc -ne -np --no-themes \
  -e builtin:mcp --no-session --provider probe --model probe-model
```

## 2. 配置拒绝负例（实际错误码）

`go test ./internal/resources -run TestSaveMCP -v` 打印的实际错误（节选）：

| 输入 | 错误码 |
|---|---|
| `command="npx -y @modelcontextprotocol/server-filesystem ."` | `UNSUPPORTED`：command 只能是单个可执行文件，请拆成 command 与 args |
| `command="cmd", args=["/c", ...]` | `UNSUPPORTED`：不要把整条命令交给 shell |
| `bash -lc ...` / `sh -c ...` | `UNSUPPORTED`：同上 |
| `powershell -Command ...` / `pwsh -EncodedCommand ...` | `UNSUPPORTED`：同上 |
| `command="npx\|tee"` | `UNSUPPORTED`：command 里出现 shell 语义字符 |
| `transport="sse"` | `UNSUPPORTED`：旧的 SSE transport 已废弃，Pi 1.5.0 不再接受；请改用 http |
| `transport="ws"` | `UNSUPPORTED`：未知 transport（只允许 stdio / http） |
| stdio 缺 command | `UNSUPPORTED`：stdio 服务必须给出 command |
| http 缺 url | `UNSUPPORTED`：http 服务必须给出 url |
| command 与 url 同时存在 | `UNSUPPORTED`：不能同时给出 command 与 url |
| `url="ftp://..."` | `UNSUPPORTED`：url 只支持 http/https |
| `url="https://user:pass@example.test/mcp"` | `CodeUnauthorized（UNAUTHORIZED）`：url 不能内嵌用户名或密码；改用 headerRefs |
| `exposure="codemode"` | `UNSUPPORTED`：未知 exposure（只允许 direct / deferred / hidden） |
| `id="../escape"` | `UNSUPPORTED`：MCP 服务 ID 非法 |
| `headerRefs.Authorization="Bearer abc123"` | `CodeUnauthorized（UNAUTHORIZED）`：看起来是明文凭证；只接受凭证引用名 |
| `envRefs.API_KEY="sk-..."` / `ghp_...` / JWT | `CodeUnauthorized（UNAUTHORIZED）`：同上 |
| `workingDir="sub/dir"` | `UNSUPPORTED`：workingDir 必须是绝对路径 |
| `workingDir` = 工作区外绝对路径 | `CodeUnauthorized（UNAUTHORIZED）`：必须在工作区或产品托管目录内 |
| `workingDir` = `工作区/../escape` | `CodeUnauthorized（UNAUTHORIZED）`：同上（Clean 后判租界） |
| 引用了不存在的凭证 ref | `DEPENDENCY_MISSING`：请先保存该凭证 |
| 无 OS 凭证库 + 带 ref 的配置 | `CREDENTIAL_STORE_UNAVAILABLE`：拒绝保存（不回退明文） |

保存不执行未知脚本：`TestSaveMCPDoesNotExecuteAnything` 用"会写 marker 的 .cmd"
作为 command，`SaveMCP` 成功但 marker 不存在（`os.Stat` 为 `IsNotExist`）；
`TestRealPiStdioMCPDiscoveryAndCall` 还断言配置/编译/写盘阶段 fixture counter 为空。

## 3. stdio 真实调用链（MCP01 的一半）

测试：`TestRealPiStdioMCPDiscoveryAndCall`（退出码 0，4.93s）。fixture 是
`tests/fixtures/mcp` 的真实进程；模型端点用可控的 OpenAI 兼容 SSE fixture。

实际写出的 `agentDir/mcp.json`（来自测试打印，未美化）：

```json
{"mcpServers":{"fixture":{
  "args":["-counter","<tmp>\\counter.txt","-calls","<tmp>\\calls.txt","-log","<tmp>\\requests.jsonl"],
  "command":"<tmp>\\fixture-mcp.exe",
  "exposure":"direct"}}}
```

证据链（全部来自测试断言/打印，不是界面推断）：

- Pi 首份模型请求里声明了工具：`first-request MCP tool declarations: [mcp__fixture__fixture_echo]`
- Pi 真的执行了调用，结果回到模型：第二份请求含 `"role":"tool"` 与 `echo: ping`
- fixture 服务端请求日志含 `"method":"tools/call"` 与 `fixture_echo`
- 副作用计数（fixture 自己写）：`fixture calls: [call fixture_echo]`（恰好 1 次）
- 进程归属：`fixture counter: [start transport=stdio pid=21052 ppid=24808 tools=fixture_echo exit transport=stdio pid=21052]`
  - `ppid=24808` 是 Pi worker，**不是** Go 测试进程（测试进程 PID 不在其中）；
  - worker `Close` 后 fixture 记录 `exit`，即 Pi 清理其 owned stdio 进程（MCP05）。

## 4. Streamable HTTP 真实调用链（MCP01 的另一半）

测试：`TestRealPiHTTPMCPDiscoveryAndCall`（退出码 0，7.74s）。fixture 的
`-http 127.0.0.1:0` 模式 + `-auth <token>`；Pi 用 header 引用访问。

实际写出的 `agentDir/mcp.json`（测试打印，秘密已替换为占位）：

```json
{"mcpServers":{"web":{
  "exposure":"direct",
  "headers":{"Authorization":"${MYAGENT_CRED_E5E6D80FD7C8}"},
  "url":"http://127.0.0.1:57157/mcp"}}}
```

- 文件里没有明文令牌，只有 `${MYAGENT_CRED_*}`；真实值经 worker 环境变量注入
- `first-request MCP tool declarations: [mcp__web__fixture_echo]`
- 工具结果回到模型（`echo: ping`）
- `fixture calls: [call fixture_echo]`（HTTP 服务端副作用计数 1，鉴权正确）

## 5. 禁用后"实际不可调用"（MCP03 / RES04）

测试：`TestDisabledMCPToolCannotExecute`（退出码 0，6.47s）。同一配置先启用跑一轮，
再禁用 → 重新 `CompileMCP` → `ApplyMCPPlan`，然后**新起一轮真实 Pi**：

```text
first run calls: [call fixture_echo]
after disabling, MCP tool declarations: []
after disabling, mcp.json servers: []
after disabling, fixture calls (unchanged): [call fixture_echo]
counter before second run: [start transport=stdio pid=20500 ppid=7372 tools=fixture_echo exit ...]
counter after second run:  [start transport=stdio pid=20500 ppid=7372 tools=fixture_echo exit ...]
```

判据是三条**运行时**事实，而不是菜单文案：

1. 禁用后 `mcp.json` 里没有该服务（不是 `enabled:false` 残条）；
2. 新 Pi 会话的工具声明里没有 `mcp__fixture__fixture_echo`；
3. 模型仍然尝试调用同名工具 → `tool_execution_end.isError=true`，而 fixture 的
   `tools/call` 计数与进程启动计数**完全不变**（没有第二个 MCP 进程被拉起）。

旧 run 不静默变化（绑定层，stub worker）：
`TestMCPChangeDuringRunKeepsOldRuntime` 断言运行中禁用后：
`mcp.json` 字节不变、`ensureRuntime` 返回同一个 `*run.Service`、`ActiveCount` 不变、
`ListMCP().Pending=true`、`RuntimeServers` 仍是旧集合；`Abort` 之后的下一次
`ensureRuntime` 才移除服务。`Status().MCPPending` 同步为 true。
`TestMCPDisabledOnlyAtSafePoint` 覆盖空闲时"下一次 ensureRuntime 生效"。

## 6. 诊断状态（MCP02 / MCP05）

诊断通道是 `pi mcp list --json`（短命 Pi 进程，连接由 Pi 拥有）。诊断把当前配置
编译到**临时 agentDir**，不改写运行中的 `mcp.json`（`TestDiagnoseMCP...` 断言
运行目录不出现 `mcp.json`）——"诊断的是当前配置"与"旧 run 用旧集合"同时成立。

| 场景 | 实际结果（测试打印） |
|---|---|
| 不存在的 stdio command | `state=failed code=PROVIDER_UNREACHABLE`，detail `MCP connection closed ... 系统找不到...` |
| HTTP 错误凭证 | `state=auth_required error=CodeUnauthorized（UNAUTHORIZED）: ... V1 不支持 MCP OAuth ... （Pi: MCP server requires authentication）`；`calls with wrong token: []` |
| HTTP 正确凭证 | `state=connected tools=[fixture_echo]` |
| 服务停止后 | `state=failed detail=fetch failed`（不保留 connected） |
| 原地址重启 | `state=connected tools=[fixture_echo]`（无需改配置） |
| 工具列表变化（`-tools fixture_echo,fixture_extra`） | 诊断 `tools=[fixture_echo fixture_extra]`；`ListTools` 同步出现 `mcp__fixture__fixture_extra` |
| 禁用服务再诊断 | `state=disabled`，且**不启动**诊断进程（runner 调用 0 次） |
| 凭证缺失（启用但 ref 不可用） | `state=unavailable` + `DEPENDENCY_MISSING`，不启动进程 |
| 诊断进程启动失败/无合法 JSON | 返回 `INTERNAL_ERROR`，不会退化成 connected |

OAuth：`MCPStatus.oauthSupported` 恒为 `false`；`auth_required` 时 UI 显示
"需要鉴权 + V1 未支持 MCP OAuth，请用 headerRefs"，不显示已连接。

## 7. 工具目录（RES04）

`internal/resources/tools.go` 的来源与状态：

- `builtin`：`read/write/edit/bash`（worker 不传 `--tools` 时的 Pi 默认集）；
  deferred 暴露时追加 Pi 自动激活的 `tool_search`；
- `harness`：自有 MCP 注册表（`harnessTools` 与 ensureRuntime 共用同一构造）；
- `mcp`：来自诊断输出的真实工具名，命名 `mcp__<server>__<tool>`；
  诊断失败/未连接时逐服务一行 `mcp__<server>__*`，标 `unknown`/`unavailable`/`disabled`，
  `Enabled=false`，不伪造工具名；
- `extension`：只列已加载的第三方扩展本身，工具清单标 `unknown`（产品无法静态枚举）。
- `policy`：统一 `approval`（F04 对每次调用都要审批；不把 `readOnlyHint` 当免审理由）。
- `hidden`：服务 connected 但工具 `Enabled=false`、`capabilityState=hidden`。

## 8. 未覆盖 / 限制

1. **六目标**：只有 win-x64。macOS/Linux 未跑真实 Pi，MCP01–MCP05 的 `ALL` 范围未满足。
2. **超时**：配置模型没有 `timeout` 字段（Pi 默认 60s/请求）；"超时"只在解析层
   用注入的 Pi 输出覆盖（`TestDiagnoseMCPMapsTimeoutAndUnreachable`），没有一条
   "真的等 60s 超时"的真实 Pi 用例。服务不可达（ECONNREFUSED/不存在命令）是真实覆盖的。
3. **诊断的连接代价**：诊断会启动短命 Pi，因此也会短暂启动 stdio MCP 的第二个进程。
   这是 Pi 拥有的连接，Go 没有自建 MCP client（§3 的 ppid 证据）；对有状态服务
   可能触发重复初始化，V1 作为已知限制记录。诊断超时（默认 90s，大于 Pi 单请求
   60s）时 Go 会终止诊断用的 Pi 主进程；由 Pi 启动的 stdio 子进程正常路径下会随
   连接关闭退出，但超时强杀的极端情况没有额外进程树收割（worker 路径才有）。
4. **诊断输出不含 inputSchema**：Pi 只列工具名，目录里 MCP 工具的 schema 为空（未编造）。
5. **OAuth**：V1 未实现 `pi mcp login`；需要 OAuth 的服务只能显示 auth_required。
6. **前端**未做真实 Wails 渲染/交互验证（Vitest + jsdom 通过）；未做 E2E。
7. **凭证轮换**：摘要包含值的哈希，因此轮换会触发下一次 ensureRuntime 重建；
   没有单独的真实 Pi 用例（只有单测级摘要稳定性检查）。
8. `writeMCPConfig`（F04，自有 MCP）仍负责 `harness` 条目；外部服务由
   `resources.ApplyMCPPlan` 用 `mcp-managed.json` 记账后合并/移除，两者都不触碰对方的名字。
   两者在 `ensureRuntime` 内由 `rtMu` 串行，诊断不再写运行中的文件。

## 9. 需要复核者处理

- `tests/acceptance/cases.json` 与 `docs/acceptance/v1-gates.md` 未改（不在本任务所有权内）：
  MCP01–MCP05 / RES04 的 case→命令映射请复核后补；本次没有生成 G0 式证据目录。
- B00 的 `go.mod`/`go.sum`/`build/versions.lock.json` 改动与本任务无关，未触碰。
- 未提交文件清单见 `git status --short` 中 C04 相关路径（见最终报告）。
