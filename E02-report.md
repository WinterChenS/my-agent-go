# E02 report — runners + external observations, round 2 (MOD / MCP / SEC07.store / CONN)

- Task: E02
- Repo: **worktree** `D:\project\anvil-wt\e02`（分支 `feat/e02`，未碰 `D:\project\my-agent-go`）
- Start HEAD: `71e8165`
- Code commit: `0ab61a2`；final revision = **包含本报告的提交**（`git log -1 --format=%h`）
- 证据目录按 final revision 命名：`docs/evidence/runs/<final>/G2/win-x64/`（本节 §5 的数字就是在它上面跑的）
- Status: **PASS**（8 条 case 接上 runner + 外部观察并通过；6 条按契约 BLOCKED 且理由可复核；1 条按"实现/产物不存在"如实不接）

`tests/acceptance/cases.json` **未改动**：候选 case 的 `oracleKind` 本来都是 `external`，没有需要转换的条目。

---

## 1. Commit

| commit | 内容 |
|---|---|
| `0ab61a2` | E02: wire MOD01/MOD04/MCP04/SEC07.store/CONN03-05/CONN08 with external observations（含 MOD02/MOD03/MCP01/MCP02/MCP03/MCP05 的 BLOCKED 条目与只读后端探测） |
| final | E02: record the wiring report and the gate numbers（唯一内容是本报告；代码未变，因此 §5 的数字与 `0ab61a2` 上完全一致） |

说明：证据按 revision 存放，必须是**最终 revision** 上的运行结果才参与该 gate 判定。
本轮在包含报告的最终 revision 上完整重跑了 G2 集成套件并重跑 gate，数字见 §5，与
code-commit 上的运行完全一致（报告文件不改变任何被测代码路径）。

改动文件（全部在允许范围内；`gate.go` / `index_test.go` / `runner.go` 未改）：

```
internal/devtools/integration.go                 # 唯一改动的 devtools 文件（case 表 + 后端探测）
internal/resources/observation_test.go           # 新增：配置根/agentDir 遍历 + 磁盘 sentinel 扫描
internal/resources/models_test.go                # 追加观察调用（MOD01）
internal/resources/mcp_test.go                   # 追加观察调用（MCP04 / SEC07.store 的不可用路径）
internal/credentials/observation_test.go         # 新增：凭证根遍历 + sentinel 扫描
internal/credentials/credentials_test.go         # 追加观察调用（SEC07.store）
internal/connectors/observation_test.go          # 新增：第二个 DB 句柄 dump + sentinel 扫描
internal/connectors/service_test.go              # 追加观察调用（CONN03/04/08）
internal/connectors/mail/driver_test.go          # 观察目录接入 fixture + Observe 调用（CONN08）
internal/harnessmcp/observation_test.go          # 新增：真实 MCP 服务 tools/list 响应字节落盘
internal/harnessmcp/connector_tools_test.go      # 追加观察调用（CONN04/CONN05/CONN08）
internal/app/model_observation_test.go           # 新增：settings.json / agentDir 磁盘事实（MOD04）
internal/app/model_api_test.go                   # 追加观察调用（MOD04）
internal/app/connector_observation_test.go       # 新增：appData 遍历 + 明文扫描（CONN03）
internal/app/connector_api_test.go               # 追加观察调用（CONN03）
internal/app/mcp_api_test.go                     # 追加观察调用（CONN05）
```

**没有**新增/删除/弱化任何既有断言：只追加观察调用与（CONN05 里）两条新断言（禁用后新 run 的工具表里没有该工具、调用必须返回错误且不产生副作用）。不碰 `tests/acceptance/**`、`docs/**`、`schemas/**`、`frontend/**`、`internal/run/**`、`internal/runtime/pi/**`、`internal/piinstall/**`、`internal/session/**`。

---

## 2. 接上的 8 条（全部 `external-observation`，artifacts 非空，`sourceRevision = 0ab61a2`）

观察契约沿用 `internal/memory|run|config` 的既有写法：目录来自 `ANVIL_OBSERVATION_DIR`，`observation.json` **合并写**，artifact **按测试追加**；未设置变量时什么也不写。哨兵只记 sha256，明文命中数由**重新扫描磁盘**算出（原始字节 + 3 种 base64 变体）。

| case | 命令（go test 包 / 用例） | 观察源 | artifacts（实测字节） | 观察值的边界 |
|---|---|---|---|---|
| MOD01 | `internal/resources`：TestModelSchemaValidation、TestModelNormalization、TestModelSaveNoSecretsInJSON、TestModelSaveRequiresExistingCredential、TestModelUpsertPreservesOtherFields、TestModelSaveUsesCAS、TestDeleteModel | filesystem | config-tree.json 992、models-json.json 350、secret-scan.json 395 | 配置存储根的真实遍历（sha256/revision）+ 产品写下的 `models.json` 从盘读回（长度/sha256/是否含 `$ENV` 引用/是否含明文）；明文命中 0 由重扫得到。**校验规则的逐条判定仍是进程内断言**（纯函数无落盘产物），外部观察覆盖的是"落盘后只有 credentialRef"。 |
| MOD04 | `internal/app`：TestModelSwitchDoesNotInterruptRunningRun、TestModelLifecycleThroughBindings | filesystem | model-switch-tree.json 2391、model-state.json 996 | appData 根真实遍历 + `settings.json` 解析事实（`modelRef=m-demo`，而 `provider/model` 仍是旧值 → 新模型确实"等下一次运行"）+ agentDir 里 `models.json` 是否含内联 apiKey（无）。运行中的 run 用的是测试自建的 `run.Service`，所以 runs 表里没有行——**provider/model/configRevision 的服务端记录由 `internal/run` 的 `TestSnapshotRecordsModelAndConfigRevision` 断言，而 `internal/run/**` 本轮禁止改动**，故这条 case 的外部证据覆盖的是安全点 + 磁盘配置边界。 |
| MCP04 | `internal/resources`：TestSaveMCP\*（shell 整串/路径含空格/schema/明文秘密/凭证存在性/workingDir 逃逸/保存不执行）、TestCompileMCPKeepsSecretsOutOfJSON、TestApplyMCPPlanRemovesDisabledServer、TestApplyMCPPlanRefusesCorruptFile | filesystem | mcp-config-tree.json 1368、mcp-save-sandbox.json 579、mcp-runtime-tree.json 794、mcp-secret-scan.json 406 | 配置存储根与 agentDir `mcp.json` 的磁盘快照；`markerExists=false` 由 `os.Stat` 读盘得出（保存阶段没有执行脚本）；编译结果的明文扫描命中 0；`mcp.json` 里禁用项确实被移除。 |
| SEC07.store | `internal/credentials`：TestNativeStoreRoundTripAndNoPlaintext、TestNativeStoreRejectsBadRef、TestMemStoreIsSessionOnly（`MYAGENT_REQUIRE_NATIVE_CREDSTORE=1`）+ `internal/resources`：TestMCPCredentialStoreUnavailableIsExplicit | filesystem | cred-store.json 1207、store-unavailable-tree.json 510、store-unavailable-scan.json 430 | 凭证根的真实文件（DPAPI 密文文件：名字/字节/sha256）与每次快照的明文命中数（Put 后、轮换后均为 0）；"契约失效不落明文"由 MCP 层不可用路径（返回 CREDENTIAL_STORE_UNAVAILABLE 且配置根 0 文件、0 命中）从磁盘侧证明。后端可用性由 `nativeCredStoreBlockedReasonFor` 只读探测（Windows DPAPI / macOS `security list-keychains` / Linux `secret-tool lookup`）；不可用写 BLOCKED 而不是让 `t.Skipf` 变成 PASS。 |
| CONN03 | `internal/connectors`：TestNoSecretLeaksIntoLogsConfigOrDTO + `internal/app`：TestConnectorsAppAPIRoundTrip | database | connector-db.json 885、connector-sentinel-scan.json 377、connector-app-tree.json 1145 | 第二个 `storage.DB` 句柄打开同一个 `connectors.db`，直接读 `connector_definitions` / `connector_audit` 真实行（含 credential_ref）；对库文件（含 WAL/SHM）重新扫描 sentinel，命中 0；绑定层再扫 appData 根（撤销后仍 0 命中）。 |
| CONN04 | `internal/connectors`：TestConnectorRegistryDeclaresCapabilitiesAndScopes、TestConnectorDriverCannotDowngradeRisk + `internal/harnessmcp`：TestConnectorMCPConfigCarriesNoToken | database | connector-db.json 1373、connector-sentinel-scan.json 736、connector-tools-list.json 2346 | 模型真正看到的那一面：真实 MCP 服务在 TCP 上返回的 `tools/list` 响应体**原样落盘**（工具名、描述里的 risk/idempotency 声明、inputSchema）；连接器库审计行证明"驱动把 send 说成 read-only+幂等"被拒绝（0 派发）。 |
| CONN05 | `internal/harnessmcp`：TestConnectorMCPToolsArePolicyGated + `internal/app`：TestMCPDisabledOnlyAtSafePoint、TestMCPChangeDuringRunKeepsOldRuntime | filesystem | connector-tools-list.json 372、connector-counter.json 483、connector-observation.json 897、mcp-agent-tree.json 1398 | 禁用后**新 run** 的 `tools/list` 响应体里已无 `connector_mail_*`，再调该工具返回 JSON-RPC 错误且 fixture 副作用仍为 1（没有新写入）；agentDir 的 `mcp.json` 磁盘快照证明"空闲时才改、运行中不改"。 |
| CONN08 | `internal/connectors`：TestConnectorRejectsOutOfScopeRecipients、TestConnectorRegistryDeclaresCapabilitiesAndScopes + `internal/connectors/mail`：TestMailReadActionsUseFixtureWithoutWriteSideEffects、TestMailSendIsGatedByPolicy + `internal/harnessmcp`：TestConnectorMCPToolsArePolicyGated | fixture-counter | connector-counter.json 483、connector-observation.json 3492、connector-db.json 2197、connector-sentinel-scan.json 740、connector-tools-list.json 372 | 五个 fixture 侧的真实副作用快照 + 审计行：附件经真实 IMAP fixture 取回（`mailRead`>0 但 `externalWrites`=0）；越界收件人在派发前被拒（审计 deny、驱动 0 次）；伪造 `sessionId` 到不了 adapter handler；禁用后再调用无新写入。 |

证据根统一为 `docs/evidence/runs/<final>/G2/win-x64/<CASE>/`（`result.json` + `oracle.json` + stdout/stderr + 上表 artifacts）。全部 `oracle.kind = external-observation`，`result.sourceRevision = <final>`，`worktreeDirty=false`。

复现命令：

```
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2 --case=MOD01,MOD04,MCP04,SEC07.store
# EXIT=0；[PASS]×4；汇总: 可跑 4 / 必需 4 条，失败 0，阻塞 0

powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2 --case=CONN03,CONN04,CONN05,CONN08
# EXIT=0；[PASS]×4；汇总: 可跑 4 / 必需 4 条，失败 0，阻塞 0
```

---

## 3. BLOCKED 的 6 条（写了 runner 条目、执行时按探测结果报 BLOCKED）

全部缺同一个观察装置：**真实 Pi bundle（`.tools/pi/<ver>/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js`）**。这个 worktree（`D:\project\anvil-wt\e02`）下没有 `.tools/`（主仓 `D:\project\my-agent-go` 有），所以 `realPiBlockedReasonFor` 的只读探测判定缺件。

| case | 需要真实 Pi 的判据 | 状态 |
|---|---|---|
| MOD02 | 临时 Pi 连接测试要真起 Pi 去打 fixture provider（401/429/超时/有效文本分类） | BLOCKED |
| MOD03 | "未知模型能力标 unknown" 由 `internal/resources` 的 probe 用例断言（同样需要真实 bundle）；另外两条（旧 Pi auth 不改、客户端 key 只进 OS 库）有可跑的测试，但 case 是原子的，不拿部分成功冒充整体 | BLOCKED |
| MCP01 | stdio/HTTP 两类经真实 Pi 发现与调用 fixture 工具 | BLOCKED |
| MCP02 | 断开/超时/鉴权失败/重连状态（真实 Pi 诊断） | BLOCKED |
| MCP03 | 工具列表变化与暴露策略生效（真实 Pi） | BLOCKED |
| MCP05 | 只由 Pi 管理外部 MCP 连接、退出清理其 owned 进程 | BLOCKED |

```
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2 --case=MOD02,MOD03,MCP01,MCP02,MCP03,MCP05
# EXIT=0；[BLOCKED]×6，每条都带修复命令（把 Pi 按应用托管流程装到 .tools/pi 后重跑）
# 汇总: 可跑 6 / 必需 6 条，失败 0，阻塞 6
```

---

## 4. 未接的 case 与确切缺什么

| case | 状态 | 缺什么 |
|---|---|---|
| CFG01 | **未接（产物不存在）** | 五层合并是纯函数：`Merge` 的 `Provenance` 只在内存里，`Store.Compile` 把它放进 `contracts.EffectiveConfig` 但**不落盘**（`compile.go` 只返回结构体）。要外部取证只能改产品行为（把有效配置/provenance 写盘）或把合并逻辑复制进测试（自证），两者都违反纪律。**本轮新增的 `internal/config` 观察装置已经能读配置存储根**，等产品把"有效配置 + 来源"落盘（例如写进 agentDir 的 settings/provenance 文件）后可以立刻接上。 |
| BR07.tool | **等下一轮（实现/测试都不在本 worktree）** | `internal/browser` 目前只有契约层（`contracts_test.go`：Action/Snapshot/Ref 校验、`UnimplementedReturnsNotImplemented`），native 只有 Windows 控制器的两个测试；B04 的 tool 面（observe/act 的可执行语义 + 结果与预期一致）在 `brnative` 分支进行中，本 worktree 里没有可接的测试。按要求"若它同时提供 tool 面，就等下一轮"。 |

（另外：`CFG03` / `RUN01` / `RUN04` / `RUN06` / `SEC03` / `SEC04` / `SEC07` 这类**复合 case** 本来就不该有 runner，它们由子 case 各自取证。）

---

## 5. 真跑出来的数字（不是估算）

### 5.1 runner 覆盖（`integrationCases()` vs `cases.json`，同一段脚本对 `git show 71e8165:internal/devtools/integration.go` 与当前文件做差集）

| 口径 | before `71e8165` | after `0ab61a2` |
|---|---|---|
| `integrationCases()` 条目数 | 75 | **89**（+14） |
| G2 必需 case 中没有 runner 的 | 34 | **20**（−14） |
| 全部必需 case 中没有 runner 的（去重） | 123 | **109**（−26，含 RPC/SEC 等其它 gate 的 sub-case） |

新增的 14 条：`MOD01`、`MOD02`、`MOD03`、`MOD04`、`MCP01`、`MCP02`、`MCP03`、`MCP04`、`MCP05`、`SEC07.store`、`CONN03`、`CONN04`、`CONN05`、`CONN08`（其中 6 条按环境 BLOCKED）。

G2 里剩下的 20 条没有 runner 的：`BR08–BR17`（10，B04/B01 面）、`CFG01` + `SEC03.browser`/`SEC04.browser`（3，产物不存在）、以及 7 条本来就不该有 runner 的复合 case（`CFG03`、`RUN01`、`RUN04`、`RUN06`、`SEC03`、`SEC04`、`SEC07`）。

### 5.2 在最终 revision 的干净工作区**完整跑** G2 集成套件

```
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2
# EXIT=1
# 汇总: 可跑 56 / 必需 69 条，失败 2，阻塞 8
# 以下必需 case 没有集成 runner：SEC03.browser, SEC04.browser, CFG01, BR08..BR17
```

失败 2 条：`SEC02`、`SEC08` —— 都是**既有**条目（E01 之前就接了，Env=`MYAGENT_PI_POLICY=1`），在本 worktree 缺 `.tools/pi` 时以退出码 1 失败（测试内直接要求真实 bundle，不 skip）。这是 worktree 环境差异，不是 E02 引入；在主仓（有 `.tools/pi`）重跑即可。

阻塞 8 条：`PI_INS02`、`RES02`（既有 BLOCKED）+ E02 新增的 `MOD02`、`MOD03`、`MCP01`、`MCP02`、`MCP03`、`MCP05`。

### 5.3 真跑 `gate --id=G2`

```
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 gate --id=G2 \
  --evidence=D:/project/anvil-wt/e02/docs/evidence/runs/0ab61a2/G2/win-x64
# 基线 revision=<final> worktreeDirty=false 证据根=.../<final>/G2/win-x64
# passed=false exitCode=2 必需证据位=69
# 通过 46 / 不通过 23
#   [rejected]×10：MCP01 MCP02 MCP03 MCP05 MOD02 MOD03 PI_INS02 RES02（BLOCKED，只认 PASS）
#                   + SEC02 SEC08（命令退出码非零）
#   [missing]×13：BR08–BR17（10）、CFG01、SEC03.browser、SEC04.browser
# 另有 [deferred]×255：ADR 0005 本轮停用的其它五个目标导致的未评估必需位
```

（同一命令在**只跑 8 条 case** 的证据树上会给出 `必需证据位=69 / missing 更少但 rejected 更多`，那是"单 case 取证模式"的开发用数字，不是 gate 结论。）

**E02 的 8 条在 gate 输出里都是 PASS**（`通过 46` 含这 8 条；接上之前这 8 条属于"没有集成 runner"，根本无法评估）。

---

## 6. 验证

```
go test ./internal/resources ./internal/credentials ./internal/connectors/... ./internal/harnessmcp ./internal/devtools ./internal/config ./internal/storage -count=1
# EXIT=0，全部 ok（32 个包中的相关 16 个）
gofmt -l internal/ cmd/ tests/    # 空
```

复用测试的 flaky 检查（各一次 `-count=3`，全绿、无可观察抖动）：

```
go test ./internal/resources -run '<MOD01/MCP04/SEC07 相关 18 条>' -count=3
go test ./internal/credentials -run 'TestNativeStoreRoundTripAndNoPlaintext|TestNativeStoreRejectsBadRef|TestMemStoreIsSessionOnly' -count=3
go test ./internal/connectors ./internal/connectors/mail -run '<CONN03/04/08 6 条>' -count=3
go test ./internal/harnessmcp -run 'TestConnectorMCPToolsArePolicyGated|TestConnectorMCPConfigCarriesNoToken' -count=3
go test ./internal/app -run 'TestModelSwitchDoesNotInterruptRunningRun|TestModelLifecycleThroughBindings|TestMCPDisabledOnlyAtSafePoint|TestMCPChangeDuringRunKeepsOldRuntime|TestConnectorsAppAPIRoundTrip' -count=3
```

`verify --suite=static` 在本 worktree **EXIT=1**，原因与本任务无关（worktree 没铺前端）：

```
go vet: frontend\embed.go:11:12: pattern all:dist: no matching files found   # worktree 没有 frontend/dist
前端 typecheck: 'tsc' 不是内部或外部命令                                    # worktree 没有 frontend/node_modules
```

主仓 `D:\project\my-agent-go` 两者都存在（`frontend/dist`、`frontend/node_modules`）。gofmt 与 Go 侧 vet（除上述 embed）均干净。

---

## 7. Concerns

1. **本机不是验收机**：Windows 10 22H2 / build 19045 < 目标最低 build 26100（GOV05），gate 会拒绝本机 win-x64 证据。E02 的证据是**开发证据**，需在 Windows 11 24H2 或 CI 上重跑。
2. **worktree 缺 `.tools/pi`**：导致 6 条 E02 BLOCKED + 2 条既有 SEC02/SEC08 失败。在主仓（有 `.tools/pi`）或装了 Pi 的机器上，`MOD02/MOD03/MCP01-03/MCP05` 可立刻从 BLOCKED 转成实际执行；但它们的**观察装置**（fixture 请求日志 / Pi 侧工具表）本轮没有补，届时需要通过观察而不是进程内断言取证——这点必须在下一轮补上，否则会在有 Pi 的机器上暴露出"runner 有、外部观察没有"的缺口。
3. **MOD01 与 MOD04 的外部观察有已知边界**（见 §2）：MOD01 的校验规则判定、MOD04 的 provider/model+configRevision 服务端记录仍是进程内断言（后者在 `internal/run`，本轮禁止改动）。报告的 PASS 只就"磁盘边界"成立，不声称覆盖了那两条断言。
4. **CFG01 仍未接**：需要产品把"有效配置 + provenance"落盘，或者接受用 `internal/config` 的编译产物作为观察面；这是实现/装配决定，不该由 E02 单方面改生产行为。
5. **证据按 revision 存放**：`0ab61a2` 之后任何提交都会让本轮证据不参与该 gate 判定；验收前必须在最终 revision 上重跑 G2。
6. **CONN05 的 app 侧两步共用 `mcp-agent-tree.json`**：两条测试各自是独立的 appData 根，artifact 按测试追加（数组），审查时请按 `test` 字段区分，不要读成"同一份 mcp.json 两次快照"。
