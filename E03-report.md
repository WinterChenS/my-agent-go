# E03 报告：MOD02/03 与 MCP01/02/03/05 的外部观察装置

- 工作树：`D:/project/anvil-wt/e03`（分支 `feat/e03`）；主仓库 `D:/project/my-agent-go` 未改动
  （仅只读复制了 gitignore 的 `.tools/pi`，见 §5）
- 证据根：`docs/evidence/runs/<本报告所在提交>/G2/win-x64/<case>`（gitignore，按 revision 分目录）
- 结论：6 条全部 PASS，oracle 均为 `external-observation`，artifact 非空，
  `sourceRevision` = 本报告所在提交、`worktreeDirty=false`。
  G2 门禁整体仍未通过（§4），因为同一 revision 下其余 63 条 case 没有证据。

> 说明：证据目录按 revision 分，因此本报告不写死某一次运行的绝对哈希/端口/pid
> （重跑必然变化）。核对方式见 §2 的命令；`oracle.json` 与 `result.json` 里才有本次运行的值。

## 1. 做了什么

### MOD02 / MOD03（source: `http-server`）

`providerSim` 本身就是实现边界之外的网络对端（进程内 httptest，但它是被测 Pi 真正去打的
provider）。它自己读下的请求体与 `Prober.Probe` 的结论先落盘，事实再从盘上读回：

| artifact | 由谁产生 | 内容 |
|---|---|---|
| `probe-requests.json` | `providerSim.ServeHTTP` 实际读到的请求体 | 每次探测的 `requestCount` + 原始 `requests[]`（含 401/429/超时三类） |
| `probe-result.json` | `Prober.Probe` 的返回 | `reachable` / `toolCalling` / `vision` / `latencyMs` / `error.code` / `error.retryable` |

`observation.json` 的每一条事实都由这两份文件重新解析得到
（`internal/resources/model_mcp_observation_test.go`）：

- `probeRequests`（跨快照累计：MOD02 = 11，MOD03 = 4）、`probeRequestSnapshots`、`probeLabels`
- `probeErrorCodes`（MOD02 = 401 / RATE_LIMITED / RPC_TIMEOUT 三类）
- `byTest:<test>/<label>`：`requestCount`、`reachable`、`toolCalling`、`vision`、`latencyMs`、
  `errorCode`、`errorRetryable`，以及从落盘请求体里数出来的 `firstRequestHasTools`、
  `firstRequestToolCount`、`lastRequestDeclaresRead`

### MCP01 / MCP02 / MCP03 / MCP05（source: `filesystem`）

fixture MCP server 是独立进程，它自己往文件里追加记录；测试只读这些文件：

| artifact | 由谁产生 | 内容 |
|---|---|---|
| `mcp-server-log.json` | `tests/fixtures/mcp` 进程的 `-calls` / `-log` / `-counter` | 每次真实工具调用一行、收到的 JSON-RPC、启动/退出与 `ppid` |
| `mcp-diagnose.json` | `Service.DiagnoseMCP`（`pi mcp list --json` 的真实输出映射） | `state` / `tools` / `errorCode` / `retryable` / `oauthSupported` / `detail` |

事实同样从磁盘读回：`mcpFixtureCallRows`、`mcpFixtureLabels`、`mcpDiagnoseStates`、
`mcpDiagnoseErrorCodes`，以及 `byTest:<test>/<label>` 里的 `callLines` / `requestLines` /
`processStarts` / `processExits` / `parentPIDs` / `state` / `tools` / `toolCount`。

## 2. 逐条取证（真实命令与退出码）

对每条执行（工作区干净、HEAD = 本报告所在提交）：

```
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2 --case=<ID>
```

| case | 退出码 | 状态 | oracle | artifacts（oracle.json 声明的相对路径） |
|---|---|---|---|---|
| MOD02 | 0 | PASS | external-observation / http-server | probe-requests.json、probe-result.json |
| MOD03 | 0 | PASS | external-observation / http-server | probe-requests.json、probe-result.json |
| MCP01 | 0 | PASS | external-observation / filesystem | mcp-server-log.json |
| MCP02 | 0 | PASS | external-observation / filesystem | mcp-diagnose.json、mcp-server-log.json |
| MCP03 | 0 | PASS | external-observation / filesystem | mcp-server-log.json、mcp-diagnose.json |
| MCP05 | 0 | PASS | external-observation / filesystem | mcp-server-log.json、mcp-diagnose.json |

各条 `result.json`：`gateId=G2`、`targetId=win-x64`、`exitCode=0`、`sourceRevision` = 本报告
所在提交、`worktreeDirty` 缺省（false）；`oracle.json`：`kind=external-observation`、
`schemaVersion=1`、`artifacts` 非空且都是证据目录内的常规非空文件。
runner 的 `writeExternalObservationOracle` 保证：case 没写 `observation.json` 就直接 FAIL，
不会退回 test-process 自证。

## 3. 观察值示例（都来自证据目录里的文件）

- MOD02 `TestProbeClassifiesFailures/401`：`reachable=false`、`errorCode` = 未授权码、
  `errorRetryable=false`、`requestCount=1`；429 → `RATE_LIMITED`/retryable=true；
  slow → `RPC_TIMEOUT`/retryable=true。三类失败的分类互不相同，且 fixture 各自确实收到 1 份请求。
- MOD02 `TestProbeDetectsToolCalling`：4 份请求，`toolCalling=supported`，
  落盘的最后一请求里出现 `"read"`；`TestProbeReachableText`：2 份请求、`toolCalling=unsupported`，
  首份请求 `tools` 为空（文本探测不声明工具）。
- MCP01 stdio：`processStarts=1`、`processExits=1`、`requestLines=4`、`callLines=1`，
  `parentPIDs=[<Pi 的 pid>]`（不等于测试进程），即"fixture 进程由 Pi 拥有并被清理"；
  HTTP 侧 `callLines=1`。
- MCP02：`auth_required`（错误令牌）→ `connected`（正确令牌）→ `failed/PROVIDER_UNREACHABLE`
  （服务停）→ `connected`（同地址重启），鉴权失败期间 fixture 调用行数 = 0。
- MCP03：诊断工具表 `[fixture_echo]` → `[fixture_echo, fixture_extra]`；禁用后 fixture
  调用行数与进程启动次数都不变（`1` / `1`），即禁用后没有再起 MCP 进程。
- MCP05：stdio 发现 + 诊断两条测试的观察值同域累计（`mcpFixtureCallRows=1`，诊断 2 份快照）。

## 4. G2 门禁真实数字

```
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 gate --id=G2 --evidence="D:\project\anvil-wt\e03\docs\evidence\runs"
```

- 基线 revision = 当前 HEAD，`worktreeDirty=false`，必需证据位 = 69
- **通过 6 / 不通过 63**（断言失败 0 条；63 条是"缺必需证据"——同一 revision 下其余 case
  还没有在本 worktree 重新取证）
- 本轮停用目标（ADR 0005）导致未评估必需位 255 条
- 退出码 2（未通过，符合预期；本任务只补这 6 条，不宣称 G2 通过）
- 6 条 case 都在通过的那 6 个里；`缺必需证据` 列表里不含 MOD02/MOD03/MCP01/MCP02/MCP03/MCP05

## 5. 环境与限制（诚实记录）

- 本机是 Windows 10 22H2 build 19045，低于基线最低验收系统 Windows 11 24H2：
  这份 win-x64 证据是**开发证据**，不能作为 win-x64 验收结论（GOV05 会在门禁侧拒绝不合规宿主）。
- 只有 win-x64：其余五个目标（win-arm64/mac-x64/mac-arm64/linux-x64/linux-arm64）
  这 6 条均为 NOT_RUN（门禁侧按 ADR 0005 记 deferred）。
- worktree 里原本没有 `.tools/pi`（gitignore），本次从 `D:/project/my-agent-go/.tools/pi`
  只读复制了一份（`v1.5.0` + `standalone`，约 330M）；两边文件清单与逐文件 sha256 的聚合哈希
  一致（`75294034f00e0cb2…`）。这是环境准备，不是产品改动，也没有碰主仓库的工作树。
- `Blocked` 字段没有删除，而是改成 `realPiBlockedReasonFor("G2", <id>)` 的只读探测：
  真实 Pi 装置缺失时如实 BLOCKED（skip 在 runner 里是 exit 0，会被读成 PASS），
  装置齐备时为空串——本机即为空串，6 条都真的执行了。这与 RES02 的写法一致。
- `tests/acceptance/**`、`docs/**`、`schemas/**`、`frontend/**`、`internal/run/**`、
  `internal/runtime/pi/**`、`internal/app/**` 未改动。没有新增 `t.Skip`，没有弱化既有断言。

## 6. 测试摘要

`go test ./internal/... -count=1` → 退出码 0（33 个包 ok，含 `internal/resources` 与
`internal/devtools`）；`gofmt -l internal/resources internal/devtools` 无输出；
`go vet ./internal/resources ./internal/devtools` 通过。
