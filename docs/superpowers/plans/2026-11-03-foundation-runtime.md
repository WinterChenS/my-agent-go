# Foundation and Pi Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立可验证的工程基础、宿主 Pi 集成、权限、安装和持久化。

**Architecture:** Go 域服务独立于 Wails；真实 Pi 子进程经 LF JSONL adapter接入；自有 MCP 和 policy bridge绑定同一会话。

**Tech Stack:** 版本、依赖与平台矩阵严格来自执行基线。

**Spec:** `docs/superpowers/specs/2026-11-03-v1-execution-baseline.md`、master plan、`docs/acceptance/v1-gates.md`。

## Global Constraints

基线 R01–R10 全部适用；未通过 G0 的 Pi 能力不能向产品宣称可用；不修改全局宿主 Pi 与 auth。文件为建议新建路径，现场有同职责代码时先核查后映射并记录，不另写一套。

## Review Focus

Node/Pi PATH混用→F00/F02；RPC多字节/超大record→F03；项目脚本偷偷启动→F04；崩溃重复写入→F05；压缩包越界→F06。下面各任务均有对应命名用例。

## F00：工具链、版本与构建环境冻结

**依赖：** 无。**Files：** `.nvmrc`、`go.mod`、`go.sum`、`frontend/package.json`、`frontend/package-lock.json`、`build/versions.lock.json`、`build/platforms.json`、`docs/evidence/environment.md`；测试 `scripts/check-toolchain.ps1` / `.sh`。

**Interfaces：** 输出版本锁 JSON：schemaVersion、go、node、npm、wails、piValidated、directDependencies（name/version/source/hash/license）、nativeToolchains、targetMatrix。命令检查退出0成功/2配置缺失。包名/模块初始 `my-agent-go`，产品名 `MyAgent`；公开产品名称与module仓库路径以后统一更名，不自行猜远程组织或发布账户。

- [ ] 收集现场状态、验证六目标资源，写失败检查 `ToolchainRejectsWechatNode`：PATH首个node=16时拒绝，输出实际路径而不是自行调用任意npm。
- [ ] 运行脚本确认旧环境或缺依赖会非零，记录前置缺口；不要为了测试破坏系统 PATH，用子进程环境构造。
- [ ] 固定版本和稳定直接依赖；验证 Wails shell 可在本机编译，frontend静态页作为需要的最小界面。包管理安装不执行未审查项目脚本；新项目依赖安装行为明确列出。
- [ ] 查询并锁定平台SDK、数据库FTS5、MCP SDK版本；每项依赖有来源。若 Wails/Pi 基线不成立写 ADR；禁止自行选择最新版本掩盖问题。
- [ ] 检查 `node -v`、`npm -v`、`go version`、`go env`、Wails doctor、各目标资源记录；输出锁文件和环境证据后提交。缺平台资源记录 BLOCKED，不伪造主机信息。

**验收：** ENV01–ENV03。仅Go/Node可执行不表示 native SDK 或六平台环境齐备。

## F01：共享契约与真实检查编排器

**依赖：** F00。**Files：** `internal/contracts/{ids,event,errors,config,run}.go`、`schemas/{event,config,gate-evidence}.schema.json`、`cmd/devtools/main.go`、`internal/devtools/{runner,evidence,gate}.go`、`tests/acceptance/cases.json`；测试 `internal/devtools/gate_test.go`、`internal/contracts/contracts_test.go`。

**Interfaces：** ID aliases WorkspaceID/SessionID/RunID/TabID/ProfileID 为string；`Event{SchemaVersion int, EventID string, WorkspaceID WorkspaceID, SessionID SessionID, RunID RunID, Seq uint64, Type string, OccurredAt time.Time, Payload json.RawMessage}`；`AppError{Code string, Message string, Retryable bool}`。统一错误码由此定义，禁止前端靠翻译文案解析错误。

`RunSnapshot{RunID,SessionID,WorkspaceID,State,LastSeq,ConfigRevision,PiVersion,Error}`；`ConfigDocument{SchemaVersion int, Revision uint64, Body json.RawMessage}`；`ExecutionContext{WorkspaceID,SessionID,RunID,ConfigRevision,ControlGeneration}`；工具参数为schema校验后的json.RawMessage。

证据类型 `GateEvidence{SchemaVersion,GateID,TaskID,SourceRevision,VersionsLockHash,TargetID,StartedAt,EndedAt,Command,ExitCode,CaseResults,Artifacts,Limitations,Reviewer}`，完整JSON schema枚举状态，不允许null/缺字段作为通过。

- [ ] 写 `TestGateRejectsMissingRequiredCase`、`TestGateRejectsStaleRevision`、`TestGateRejectsEmptyOrSkippedNativeSuite`：缺一必需case、旧revision、仅skip时返回非零；unknown target/suite返回2。
- [ ] 先运行 `go test ./internal/devtools -run 'TestGateRejects' -count=1` 记录失败，再实现 runner 和 schema；子进程原始exit code和stdout/stderr持久化。
- [ ] 为当前任务创建acceptance索引，同时包含所有计划case ID；未实现的case保持required并使依赖gate失败，而非删掉case。suite按master约定分组，不允许在run时自动发现空目录然后判成功。
- [ ] 对命令故意失败、假报告哈希、人工证据缺审查人分别测试；人工review字段不是可机械证明的真实性，gate只验证结构完整，独立审查验真。
- [ ] 运行contracts/devtools测试、type/schema校验并提交。之后才可使用 `go run ./cmd/devtools verify` 作为门禁命令。

**验收：** GOV01–GOV04。

## F02：本地 Pi 定位、能力探测与真实协议基线

**依赖：** F01。**Files：** `internal/runtime/pi/{locator,probe,compatibility}.go`、`internal/runtime/pi/testdata/v1.5.0/`、`tests/fixtures/provider/main.go`、`tests/fixtures/mcp/main.go`、`docs/evidence/pi-compatibility.md`；测试 `locator_test.go`、`probe_integration_test.go`。

**Interfaces：** `Locate(ctx context.Context, preferred string) ([]PiInstallation,error)`；`Probe(ctx context.Context, installation PiInstallation) (ProbeResult,error)`；`PiInstallation{ID,Executable,Args,Version,Origin,Architecture}`；`ProbeResult{State,Capabilities,Version,ProtocolFixtureHash,Diagnostics}`。Origin=external/managed，State沿用基线。

- [ ] 写 `TestLocateWindowsShimWithSpaces`：中文/空格目录、npm .cmd/.ps1、多个版本、无Pi、失效保存路径；不把用户字符串拼接成通用shell命令，shell alias返回不可直接执行。
- [ ] 写并运行 `TestProbeHasNoUserConfigSideEffects`：临时HOME/agentDir，用户marker文件hash不变；probe不得请求真实模型或启动用户MCP。
- [ ] 从精确tag收集RPC/extension定义，启动真实Pi v1.5.0；接本地脚本化provider/MCP，捕获accepted、delta、tool、compaction/错误/最终idle等实际记录。记录无法诱发的事件，不编造fixture；用合成记录单独标 synthetic。
- [ ] 在无Node目标环境验证独立发行包加载本项目薄extension与MCP。独立包/宿主npm两种形式分别记录；确实支持才列Ready。未知版本默认unverified且禁止自动运行。
- [ ] 运行 `go test ./internal/runtime/pi -run 'TestLocate|TestProbe' -count=1` 及integration suite；提交版本/安装形式矩阵与原始协议。不能仅用fake-pi通过此任务。

**验收：** PI01–PI04；G0前置。该阶段provider是受控fixture，不冒称真实云模型通过。

## F03：RPC 分帧与 worker 进程监督

**依赖：** F02。**Files：** `internal/runtime/pi/{framing,rpc,worker,process_windows,process_unix}.go`、`internal/runtime/pi/fakeworker/main.go`；测试 `framing_test.go`、`worker_test.go`、`worker_integration_test.go`。

**Interfaces：** `StartWorker(ctx context.Context, spec WorkerSpec) (Worker,error)`；`Worker.Call(ctx, command json.RawMessage) (json.RawMessage,error)`、`Worker.Events() <-chan RawEvent`、`Worker.Close(ctx) error`；`WorkerSpec{Installation,WorkingDir,AgentDir,Env,SessionPath}`。`RawEvent{Type string,Record json.RawMessage}`仅adapter消费，前端不直接使用。

- [ ] 写 `TestLFFramingUnicodeAndLargeRecord`：随机chunk、多字节、U+2028、CRLF、128KiB记录均还原；16MiB边界允许，+1字节报ProtocolRecordTooLarge；malformed JSON不误接受。
- [ ] 写 `TestOutOfOrderResponsesAndStderrFlood`、`TestStdoutBackpressureDoesNotDeadlock`：响应按id归属，stderr洪泛不阻塞stdout，取消后没有悬挂Call；先运行失败用例。
- [ ] 实现持续读写、记录限额、超时和重试分类；command id不复用。未知事件保留不崩溃，执行命令写失败不自动重放。
- [ ] 写/运行 `TestWorkerTerminatesGrandchild`，Windows Job Object/Unix进程组关闭后子孙marker进程不再存活；误杀用户Pi/其他会话为失败。进程树方案若对子程序breakaway无效，要报告限制并阻止受影响模式认证。
- [ ] 跑 unit + target integration，真实Pi重新跑basic fixture，提交证据和代码。

**验收：** RPC01–RPC05。

## F04：执行前授权、项目资源信任与 Harness MCP

**依赖：** F03/F07。**Files：** `internal/policy/{service,approval,pathscope}.go`、`internal/harnessmcp/{server,auth,tools}.go`、`internal/policybridge/server.go`、`pi-extensions/harness-policy.ts`、`internal/runtime/pi/config_isolation.go`；测试 `policy_test.go`、`auth_test.go`、`tests/integration/tool_policy_test.go`。

**Interfaces：** `Authorize(ctx context.Context, exec ExecutionContext, call ToolCall) (Decision,error)`；`ResolveApproval(ctx,id string, choice ApprovalChoice) error`；`ToolCall{ID,Name,Source,Arguments,Origin,ResourceScope}`；Decision allow/deny/ask；ApprovalChoice once/session_scope/deny。`StartHarnessServer(ctx, binding SessionBinding) (Endpoint,error)`；Endpoint{URL,TokenRef}，Token不进入前端DTO。policy bridge与MCP路由/令牌分开。

- [ ] 写 `TestApprovalBindsArgumentsAndExpires`：参数变更、不同session、60秒后、二次消费均拒绝；拒绝/断连/服务超时后fixture副作用计数=0。
- [ ] 写 `TestUntrustedProjectDoesNotExecuteMarker`，涵盖.pi/.agents/祖先发现、!command、项目MCP/扩展；测试marker由fixture真实执行路径设置，不只检查配置布尔值。不能禁用的项目可执行通道让run拒绝启动。
- [ ] 写 `TestAllToolPathsUsePolicy`：真实Pi+受控provider依次调用内置write/edit/bash、自有MCP、外部MCP、扩展工具、codemode嵌套工具；拒绝后文件/HTTP计数不变。每条通道有独立case，未支持的通道从工具列表禁用并验证不可调用。
- [ ] 实现loopback随机端口、每worker随机令牌、session绑定、Host/Origin检查、请求schema、超时/大小限制；错token/伪造session/网页Origin调用不得到达工具。路径校验包含junction/symlink/UNC和实际解析后路径；明确不是OS沙箱。
- [ ] 跑policy/Harness unit与真实Pi integration，记录hook覆盖表。全部可用执行通道满足执行前检查才允许G0；不得仅在提示词里要求模型征求批准。

**验收：** SEC01–SEC08、CFG03；G0关键阻断项。

## F05：任务状态、事件、停止与恢复

**依赖：** F04/F07。**Files：** `internal/run/{service,state,events,scheduler,recovery}.go`、`internal/app/run_api.go`；测试 `state_test.go`、`recovery_test.go`、`scheduler_test.go`。

**Interfaces：** `RunService.Start(ctx, StartRunRequest) (RunSnapshot,error)`、`Send(ctx, RunID, UserMessage) error`、`Abort(ctx,RunID) error`、`Get(ctx,RunID)(RunSnapshot,error)`、`Replay(ctx,RunID,afterSeq uint64)([]Event,error)`。`StartRunRequest{WorkspaceID,SessionID,ProfileID,Message}`；`UserMessage{Text,Attachments}`；Attachment引用产物ID，不接受模型任意读取本机路径。

- [ ] 写状态转换表测试：accepted后仍running；自动压缩/重试期间不completed；最终条件依F02协议；未知完成语义报兼容错误；序号重放不重复消息。
- [ ] 写 `TestInterruptedRunDoesNotReplayUnknownWrite`：工具已产生计数后进程崩溃，恢复时计数保持1且状态interrupted；明确继续会话不等于重试工具。
- [ ] 写 `TestQueueLimitTwoAndCancelQueued`：并发上限2，第三个queued；取消第三个不启动worker；停止2/5/10秒边界用可控clock验证，native集成另测真实时间。
- [ ] 实现持久关键状态、50ms UI合并、默认预算100次/30分钟、usage未知显示unavailable；断开UI不丢transcript，审批请求断开即拒绝。
- [ ] 运行run unit与integration，崩溃进程树/事件重放证据归档后提交。

**验收：** RUN01–RUN07。

## F06：Pi 一键安装、升级与回滚

**依赖：** F02/F03/F07；可先定义测试，不得在G0前认证安装渠道。**Files：** `internal/piinstall/{manifest,download,extract,activate,service}.go`、`build/pi-manifest.json`、`internal/app/pi_api.go`；测试 `extract_test.go`、`activate_test.go`、`install_integration_test.go`。

**Interfaces：** `Install(ctx, InstallRequest)(InstallID,error)`、`Cancel(ctx,InstallID) error`、`Rollback(ctx,InstallationID) error`；InstallRequest只含已认证version/target，URL不由前端任意指定；Progress事件stage=download/verify/extract/probe/activate，明确bytesDownloaded/total与错误。

- [ ] 构造本地下载fixture，写 `TestInstallCancellationPreservesActive`、`TestHashMismatchNeverActivates`、`TestArchiveTraversalRejected`（../、绝对路径、symlink/hardlink逃逸）、`TestUnsupportedArchitectureRejected`；先观察失败。
- [ ] 实现manifest固定源/版本/hash、下载校验与staging；下载/解压/探测任一步失败，active及已有Pi hash不变。重定向不可跳出source allowlist；无网络不自动跑网页脚本。
- [ ] 写 `TestAtomicActivationAndRollback`：激活前崩溃后仍指向旧版，激活成功保留1个旧版；外部Pi不可卸载；更新等待活跃worker退出，现有worker路径固定。
- [ ] 在干净目标机真实安装官方Pi包，完成probe和最小MCP/extension回归；无Node也必须运行认证独立包。基础客户端包不增加runtime。
- [ ] 跑piinstall unit/integration、归档源/hash/目标版本/日志并提交。

**验收：** INS01–INS07。

## F07：配置 CAS、SQLite 与凭证存储

**依赖：** F01/F00。**Files：** `internal/config/{store,merge,compile}.go`、`internal/storage/{db,migrate}.go`、`internal/storage/migrations/001_initial.sql`、`internal/credentials/{store,native}.go`、`schemas/{profile,model,mcp,skill}.schema.json`；测试 `config_test.go`、`migration_test.go`、`credentials_test.go`。

**Interfaces：** `ConfigStore.Load(ctx,key string)(ConfigDocument,error)`、`CompareAndSwap(ctx,key string,expected uint64,body json.RawMessage)(ConfigDocument,error)`；`Compile(ctx,CompileRequest)(EffectiveConfig,error)`；CompileRequest{WorkspaceID,ProfileID,SessionOverrides}；EffectiveConfig{Revision,Hash,Values,Provenance,AgentDir}；`CredentialStore.Put/Get/Delete(ctx,ref string,secret []byte)`返回error。

- [ ] 写 `TestCASRejectsExternalEdit`：两writer同revision仅一个成功；外部编辑也可检测，ConfigConflict不覆盖；断电/staging恢复不产生半JSON。
- [ ] 写 `TestMergePrecedenceAndDisableByID`：五层固定值、数组同ID整项覆盖、enabled=false；权限不可放宽、credentialRef不能被项目替换。
- [ ] 写SQLite迁移事务/重复运行/失败回滚；configJSON和DB索引不构成两个配置源。验证FTS5 trigram原生支持，不满足停止。
- [ ] 凭证写入真实OS存储做target集成；不可用返回CredentialStoreUnavailable，不落明文；跨错误日志/导出扫描fixture sentinel secret，结果0命中。
- [ ] 跑config/storage/credentials suites，提交schema、迁移和证据。此任务可在G0之前独立推进。

**验收：** CFG01–CFG04、DB01–DB03、SEC07。
