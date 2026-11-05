# Resources and Workbench Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付真实连接Go/Pi的工作台、资源管理、记忆和运行详情。

**Architecture:** React调用Go应用API；JSON配置经CAS/编译产生会话快照，Pi负责实际加载；UI不复制执行循环。

**Tech Stack:** 执行基线固定版本；按需shadcn组件，未需要时不引入完整聊天框架/Monaco/终端。

**Spec:** V1 执行基线、Anvil V1 产品/UI/生命周期设计、master plan、验收矩阵。

## Global Constraints

所有页面必须有真实 Go 数据源或明确未连接状态；禁止把硬编码 model/tools/MCP 列表、统计、连接状态或成功 toast 当实现。所有页面覆盖 zh-CN/en-US × Light/Dark，错误码和稳定 test id 不依赖翻译文案。持久配置归属与 F07 一致。

**2026-11-05 增量责任：** C01–C07 的历史任务、勾选状态与证据保持不变。生产页面迁移由 [Anvil UI 计划](2026-11-05-anvil-ui.md) 负责；C08/C09 由 [连接器与调度计划](2026-11-05-connectors-scheduler.md) 负责。旧任务证据不能证明新增页面、连接器或调度已完成。

## Review Focus

主界面token频繁刷新→C01；旧auth被隐式读入→C02；保存但实际仍启用资源→C03/C04；跨项目中文记忆泄漏→C05；重放事件重复显示→C06。

## C01：工作台骨架、工作区和 Profile

**依赖：** F05/F07、G0。**Files：** `internal/workspace/service.go`、`internal/profile/service.go`、`internal/app/workspace_api.go`、`frontend/src/app/{App,Layout,Router}.tsx`、`frontend/src/features/{workspaces,profiles}/`；测试 `workspace_test.go`、`ProfileEditor.test.tsx`。

**Interfaces：** `CreateWorkspace(ctx,path string)(Workspace,error)`、`SetTrust(ctx,WorkspaceID,bool) error`；Workspace{ID,Name,CanonicalPath,Trusted,CreatedAt}；`SaveProfile(ctx,id ProfileID,expectedRevision uint64,body json.RawMessage)(ConfigDocument,error)`；profile schema字段modelRef/skillIDs/mcpIDs/toolPolicy/memoryPolicy，不另存秘密。

- [x] 写 `TestWorkspaceCanonicalizationAndTrust`：同一解析路径重复添加返回已有ID，junction重复可识别，不信任不启动可执行资源；目录不可访问显示错误。
- [x] 写 `ProfileEditorConflict`：外部修改后保存显示冲突和差异，不能覆盖；资源引用不存在保存拒绝，Profile可保存但禁止启动缺失模型的run。
- [x] 实现五入口：工作台/资源库/记忆/运行记录/设置；左项目会话、中消息、右可折叠浏览器/详情。无Pi/凭证仍能浏览设置和历史，发起任务显示修复路径。
- [x] 组件测试空/加载/失败/无权限状态，键盘Tab可达主要操作，最小1024×720无主按钮遮挡；真实Wails shell验证bindings，不只React网页截图。
- [x] unit/frontend suite通过后提交；此任务不宣称浏览器已完成。

> 证据：`docs/evidence/c01-workbench.md`（含真实模型一轮、1024×720、配置档冲突、
> 真机抓到的 4 个界面缺陷 + 1 个验证工具缺陷）。
> 键盘可达性为 DOM 级验证（`Layout.test.tsx` 的真实 Tab 序列）：本机 agent-desktop
> 送物理按键被拒，不能声称"真机按键已验"。

**验收：** UI01、CFG01/CFG02。

## C02：模型定义、连接测试与凭证

**依赖：** C01/F04/F07。**Files：** `internal/resources/{models,model_probe}.go`、`internal/app/model_api.go`、`frontend/src/features/models/`；测试 `models_test.go`、`model_probe_integration_test.go`、`ModelEditor.test.tsx`。

**Interfaces：** `SaveModel(ctx,expectedRevision uint64,ModelDefinition)(ConfigDocument,error)`；ModelDefinition{ID,ProviderID,ModelID,API,BaseURL,InputKinds,ContextWindow,MaxOutput,CredentialRef,ThinkingLevels}；`TestModel(ctx,id string)(ModelProbeResult,error)`，Result{Reachable,ToolCalling,Vision,LatencyMS,Error}，没测的能力=unknown而非true。

- [x] 写 `TestNoImplicitAuthImport`、`TestModelSaveNoSecretsInJSON`：旧Pi auth marker不被读取/修改；secret仅OS存储；导出和错误日志中sentinel命中0。
- [x] 写配置schema测试：ID唯一、URL/端口/API类型合法、context/output数值正值且output≤context；API类型白名单来自认证Pi版本，不猜所有OpenAI兼容完全一致。
- [x] 实现本地配置→Pi models配置编译；连接测试通过临时Pi session实际发最小请求，用户点击才发生调用，展示可能计费；不在Go重写provider适配。
- [x] fixture provider分别返回401/429/超时/有效文本/工具调用，分类准确；真实模型能力在X01验证，fixture不能认证外部模型。
- [x] 运行资源unit/integration/frontend，提交。

**验收：** MOD01–MOD04、SEC07。

> 证据：`docs/evidence/c02-models.md`（真实 deepseek 连接测试 2494ms、真实 run 走凭证库
> 路径、运行记录带 provider/model/revision）。
> 真机抓到并修掉的缺陷：旧路径把明文密钥写进 `models.json`（SEC07 实测漏洞）+
> 切模型后旧明文残留；旧形状条目会被编译成空 provider 键；设置诊断"实际运行模型"
> 永远显示"尚未启动"；运行记录不显示 provider/revision。
> **未覆盖：** 视觉能力恒为 unknown（本轮不发图，X01 验）；只跑 Windows；
> `pendingRuntimeKey`（等空闲生效）只有单测覆盖。

## C03：Skills 和自有扩展管理

**依赖：** C01/F04/F07。**Files：** `internal/resources/{skills,extensions,import}.go`、`internal/app/skill_api.go`、`frontend/src/features/skills/`；测试 `skills_test.go`、`skills_loading_integration_test.go`。

**Interfaces：** `ImportSkill(ctx,sourcePath string,scope ResourceScope)(SkillResource,error)`；`SetSkillEnabled(ctx,id string,expectedRevision uint64,enabled bool) error`；SkillResource{ID,Name,Description,Path,Scope,Enabled,Origin,Revision,Hash,Dependencies}；ResourceScope user/workspace。支持本地导入/编辑/启停/删除，Git/npm下载后置。

- [x] 写 frontmatter/name/description缺失、重复ID、损坏UTF8、symlink逃逸测试；Import默认复制到产品管理目录，不修改源skill；覆盖须CAS/明确动作。→ `internal/resources/skills_test.go` 13 条；真机 §3 #1/#2（跨盘 rename 缺陷已修）。
- [x] 写 `TestDisabledSkillAbsentInNextRun`：真实Pi加载资源快照，禁用后新run不能列出/读取自动发现同名副本；运行中沿用旧revision且UI显示下次生效。→ **2026-11-04 方向调整 + 实测收口**（ADR 0003）：不再用 `-ns` 抑制本地技能。真实 Pi 1.5.0 正反两向实测：用户级 `~/.agents/skills` 同步进来；项目 `.pi/skills` 与祖先 `.agents/skills` 仍被 `-na` 切断；同名时自动发现副本胜出（禁用不消失，启用也不覆盖托管版）。测试：`TestDisabledManagedSkillAbsentInNextRun`（纯托管禁用后不加载）、`TestSkillChangeIsDeferredDuringRun`（运行中沿用旧集合、pending）、`TestSkillDiscoveryLayersAgainstRealPi`（4 子测试，项目/祖先隔离 + 用户级同步 + 同名限制）。证据见 `docs/evidence/c03-skills.md` §7。
- [x] 运行记录不把托管清单冒充实际加载清单（RES02）：`RunSnapshot` 拆成 `managedSkills` + `skillAutoDiscovery`，UI 两段式显示（托管：… ｜ Pi 本地自动发现：已开启（清单未知））。—— 见 `docs/evidence/c03-skills.md` §8。
- [x] 管理自有policy extension为不可关闭的系统资源；第三方可执行扩展默认不自动启用，显示来源与信任边界。用户信任安装不等于工具免审批。→ `internal/resources/extensions.go` + `ExtensionsSection.tsx`；真机 §3 #7/#8/#9。
- [x] 对依赖缺失显示不可用和原因，不静默安装Python/命令；导入数据不执行skill脚本。→ `TestMissingDependencyMarksUnavailable`、`TestImportCopiesWithoutExecutingSource`（marker 文件不存在）。
- [x] 跑资源unit、真实Pi技能加载integration和UI测试，提交。→ 见 `docs/evidence/c03-skills.md` §5/§7；commit 见仓库历史（`C03:` 开头）。

> 证据：`docs/evidence/c03-skills.md`（真机操作、真实 Pi 测到的项目/祖先隔离、同名冲突、
> 运行快照口径）+ `docs/adr/0003-skills-discovery-and-res02.md`。
> 限制：只有 win-x64；其余五目标 BLOCKED。RES02 的 `ALL` 范围因此**未**满足，不宣称 C03 全矩阵 PASS。

**验收：** RES01–RES04。

## C04：MCP 配置、诊断与统一工具目录

**依赖：** C01/F02/F04/F07。**Files：** `internal/resources/{mcp,tools,diagnostics}.go`、`internal/app/mcp_api.go`、`frontend/src/features/{mcp,tools}/`；测试 `mcp_test.go`、`mcp_runtime_integration_test.go`。

**Interfaces：** `SaveMCP(ctx,expectedRevision uint64,MCPDefinition)(ConfigDocument,error)`；MCPDefinition{ID,Transport,Command,Args,EnvRefs,WorkingDir,URL,HeaderRefs,Enabled,Exposure}；Transport stdio/http，Exposure direct/deferred/hidden；`DiagnoseMCP(ctx,id string)(MCPStatus,error)`；`ListTools(ctx,SessionID)([]ToolDescriptor,error)`；Descriptor{Name,Source,InputSchema,Enabled,Policy,CapabilityState}。

- [ ] 测试shell字符串误当executable、无效schema、旧SSE、秘密header直接保存被拒绝；command+args分开，workingDir解析固定workspace。
- [ ] 接真实Pi的stdio和HTTP fixture MCP：发现、调用、断开、鉴权错误、超时、重连、服务工具列表变化都可见。确认Pi管理连接，Go不再启动第二个MCP client抢同一stdio进程。
- [ ] 测试 `TestDisabledMCPToolCannotExecute`：禁用后安全生效点不再可调用；撤销权限立即生效；旧run配置不静默变化，若重启worker需空闲时明确执行。
- [ ] UI显示built-in/extension/MCP来源和实际有效状态，禁用/hidden必须同时影响runtime，不能只隐藏菜单。OAuth需要授权时显示未支持/需外部配置，不伪装已连接。
- [ ] 跑unit、真实Pi+MCP integration、frontend测试并提交。

**验收：** MCP01–MCP05、RES04。

## C05：长期记忆、中文检索与注入

**依赖：** F07/F04/C01。**Files：** `internal/memory/{service,search,inject}.go`、`internal/storage/migrations/002_memory.sql`、`internal/app/memory_api.go`、`internal/harnessmcp/memory_tools.go`、`frontend/src/features/memory/`；测试 `memory_test.go`、`search_test.go`、`memory_integration_test.go`。

**Interfaces：** `Save(ctx,MemoryDraft)(Memory,error)`、`Confirm(ctx,id string) error`、`Delete(ctx,id string) error`、`Search(ctx,MemoryQuery)([]Memory,error)`、`BuildContext(ctx,MemoryQuery)(MemoryContext,error)`；Memory{ID,Scope,WorkspaceID,Kind,Content,SourceSessionID,Status,CreatedAt,UpdatedAt,ExpiresAt}；Query{WorkspaceID,Text,Limit}，Limit≤5；Context{Entries,Bytes,Provenance}。

- [ ] 构造中文检索集：A项目“数据库连接池上限为二十”、B项目同关键词不同约定、user偏好、过期和candidate条目；`TestChineseTrigramScopeAndExpiry`只返回授权active项，短查询返回QueryTooShort。
- [ ] 写 `TestDeleteNotInjectedNextRun`、`TestMemoryContextBounded`：删除后下一run无该内容，≤5条/6000bytes且UTF8完整，来源记录存在；已发出的历史不伪称删除。
- [ ] 实现显式保存/候选确认，自动后台抽取默认关闭；通过Harness MCP和prompt构建受控注入，检索结果标数据来源，不升级为系统指令。
- [ ] UI可编辑/确认/删除/按scope关闭、导出；含秘密fixture文本拒绝作为凭证存储方式，防止简单把整个工具日志当记忆。
- [ ] unit/SQLite真实FTS5/integration/frontend通过并提交。

**验收：** MEM01–MEM05。

## C06：聊天、审批、停止和历史 UI

> 2026-11-05 增量：C06 只拥有会话/运行后端域和前端事件归约；生产 Chat 组件归 U02，历史侧栏归 U01。下方原 Files 清单里的 `frontend/src/features/chat/*`、`frontend/src/features/history/` 由 U01/U02 取代。C06 通过不自动使 U02 通过。
>
> **增量范围（供 U01/U02/U05 使用）：**
> - **Acceptance cases：** UI02、RUN01/RUN04、E2E01（原有）；SES01–SES03（新增）。
> - **Files：** `internal/session/{catalog,attachments,export}.go`、`internal/session/{catalog,attachments,export}_test.go`、`internal/storage/migrations/005_session_catalog.sql`（为 `sessions` 增加 title/pinned/archived/updated_at/revision，删除为软删除标记）、`internal/app/session_api.go`、`internal/app/session_api_test.go`、Modify `internal/app/desktop.go`（按会话启动 run）、Modify `frontend/src/runState.ts`。
> - **Interfaces：** `ListSessions(SessionQuery) SessionPageResult`（按工作区分组、普通会话、稳定分页、置顶与归档过滤）、`CreateSession(workspaceID string)`、`RenameSession/PinSession/ArchiveSession(id, expectedRevision, …)`、`DeleteSession(id, expectedRevision)`（只移除索引与 transcript 定位，不删产物）、`StartSessionRun(sessionID, UserMessage)`、`SendMessage`、`AbortRun`、`ReplayRun`（现有 `Start/Abort/Replay` 保留为兼容入口）、`ResolveAttachments(sessionID, []AttachmentRequest) AttachmentResult`（Go 解析真实路径，校验工作区、symlink/junction 与 policy，逐项返回拒绝原因，生成不可变引用）、`ExportSession(sessionID, format html|markdown|json, target string)`（内容来自 Pi transcript，HTML 转义且不含脚本，秘密脱敏）。
> - **普通会话：** 使用应用托管的 scratch 工作区，不授予任何用户目录访问，满足 `sessions.workspace_id` 非空约束。
> - [ ] 先写失败测试：目录 CAS 并发写只有一个成功；分页在插入下稳定；重启后目录一致；删除不删产物；附件越界与 symlink 逃逸逐项拒绝；导出内容与 transcript 一致且 HTML 无脚本。
> - [ ] 写迁移 005，实现 catalog/attachments/export 与 `session_api.go`；`desktop.go` 只加按会话启动的装配。
> - [ ] `go test ./internal/session ./internal/app -run 'TestSession|TestAttachment|TestExport' -count=1` 通过后提交。

**依赖：** C01/F05/F04/C02，G2。**Files：** `internal/app/session_api.go`、`frontend/src/features/chat/{Transcript,Composer,ToolCard,ApprovalCard,useRunEvents}.tsx`、`frontend/src/features/history/`；测试 `RunEvents.test.tsx`、`ApprovalCard.test.tsx`。

**Interfaces：** 直接使用F05 RunService和Event DTO；前端通过Wails bindings订阅规范事件；Reconnect使用Get+Replay(afterSeq)，相同eventId/seq不重复。

- [ ] 写 `TranscriptDeduplicatesReplay`、`AcceptedDoesNotRenderCompleted`、`DisconnectDeniesApproval`；模拟delta/tool/approval/compaction重排与断线，不靠整页刷新消除重复。
- [ ] 实现纯文本/Markdown/代码块和工具卡片；Markdown默认禁任意HTML与脚本，不执行网页/工具结果中的命令；附件使用artifactId。
- [ ] 审批显示来源/工具/目标/参数摘要/范围/到期；允许一次、会话范围、拒绝行为与F04相同；停止时不等模型下次token才响应。
- [ ] 真Wails + Pi实例完成发送、排队、授权、停止、重开历史、继续；UI等待状态与后端最终状态一致，失败没有假成功气泡。
- [ ] 跑frontend、integration/E2E，提交。

**验收：** UI02、RUN01/RUN04、E2E01。

## C07：上下文、产物、预算和诊断

> 2026-11-05 增量：C07 只拥有产物、usage/预算和诊断的后端域与统一 API；页面分别归 U02（右栏）、U05（资源库）、U06（观测）。下方原 Files 清单里的 `internal/app/details_api.go` 与 `frontend/src/features/{details,artifacts,diagnostics}/` 由以下文件取代。C07 证据只能覆盖其原子 case，不覆盖新增 UI/I18N/THEME case。
>
> **增量范围（供 U02/U05/U06 使用）：**
> - **Acceptance cases：** ART01–ART03、SEC07、E2E02（原有）。
> - **Files：** `internal/artifact/{service,import,preview}.go`、`internal/artifact/artifact_test.go`、`internal/diagnostics/{export,redact,usage}.go`、`internal/diagnostics/diagnostics_test.go`、`internal/storage/migrations/007_artifacts.sql`（产物索引与 run usage 记录）、`internal/app/artifact_api.go`、`internal/app/diagnostics_api.go` 及各自 `_test.go`。
> - **Interfaces：** `ListArtifacts(ArtifactQuery)`、`GetArtifact(id)`（返回前重新核对磁盘 hash/大小，变化或丢失如实标记）、`ImportArtifact(path, workspaceID)`、`ExportArtifact(id, target)`、`PreviewArtifact(id)`（MIME 白名单）、`ListRunArtifacts(runID)`、`RunDetails(runID)`（引用来源、工具执行、审批、usage、压缩事件）、`UsageSummary(range)`（无 usage 或价格时为 `unavailable`）、`RunChain(runID)`、`ExportDiagnostics(ExportRequest)`。
> - [ ] 先写失败测试：越界与 symlink 拒绝、hash 变化被检出、缺 usage 返回 `unavailable` 而不是 0、诊断导出 sentinel 命中 0。
> - [ ] 写迁移 007，实现服务与 API，`go test ./internal/artifact ./internal/diagnostics ./internal/app -count=1` 通过后提交。

**依赖：** C03–C06/F05/F07。**Files：** `internal/artifact/service.go`、`internal/diagnostics/{export,redact}.go`、`internal/app/details_api.go`、`frontend/src/features/{details,artifacts,diagnostics}/`；测试 `artifacts_test.go`、`redact_test.go`、`RunDetails.test.tsx`。

**Interfaces：** `RegisterArtifact(ctx,ExecutionContext,path string)(Artifact,error)`、`ExportDiagnostics(ctx,ExportRequest)(ExportResult,error)`；Artifact{ID,WorkspaceID,RunID,CanonicalPath,Hash,MediaType,Size}；ExportRequest含run范围和目标路径，不默认上传云端。

- [ ] 写 `TestArtifactOutsideScopeDenied`，symlink/junction越界及消失文件不可注册为可访问产物；错误不能fallback打开任意本机路径。
- [ ] 写 `TestDiagnosticExportRedactsSentinelSecrets`：key/header/token/URL query/错误堆栈里注入sentinel，导出命中0；保留错误码、版本、时间和事件ID以可定位故障。
- [ ] 展示有效模型/skills/MCP/记忆来源、配置revision和压缩事件；无法从Pi确定精确上下文时明确estimated/unknown，不编造token图。
- [ ] 展示工具次数/时间/usage与费用来源；无价格显示unavailable，100工具/30分钟阈值前拒绝下一动作，已发模型请求费用不宣称可硬撤销。
- [ ] 运行artifact/diagnostic tests和工作台E2E并提交。

**验收：** ART01–ART03、SEC07、E2E02。
