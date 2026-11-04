# Resources and Workbench Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付真实连接Go/Pi的工作台、资源管理、记忆和运行详情。

**Architecture:** React调用Go应用API；JSON配置经CAS/编译产生会话快照，Pi负责实际加载；UI不复制执行循环。

**Tech Stack:** 执行基线固定版本；按需shadcn组件，未需要时不引入完整聊天框架/Monaco/终端。

**Spec:** V1执行基线、master plan、验收矩阵。

## Global Constraints

所有页面必须有真实Go数据源或明确未连接状态；禁止把硬编码model/tools/MCP列表当实现。UI主题统一明暗色，中文为初始界面语言，错误码/稳定test id不依赖中文文案。持久配置归属与F07一致。

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

- [ ] 写 frontmatter/name/description缺失、重复ID、损坏UTF8、symlink逃逸测试；Import默认复制到产品管理目录，不修改源skill；覆盖须CAS/明确动作。
- [ ] 写 `TestDisabledSkillAbsentInNextRun`：真实Pi加载资源快照，禁用后新run不能列出/读取自动发现同名副本；运行中沿用旧revision且UI显示下次生效。
- [ ] 管理自有policy extension为不可关闭的系统资源；第三方可执行扩展默认不自动启用，显示来源与信任边界。用户信任安装不等于工具免审批。
- [ ] 对依赖缺失显示不可用和原因，不静默安装Python/命令；导入数据不执行skill脚本。
- [ ] 跑资源unit、真实Pi技能加载integration和UI测试，提交。

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

**依赖：** F07/F04/C01。**Files：** `internal/memory/{service,search,inject}.go`、`internal/storage/migrations/003_memory.sql`、`internal/app/memory_api.go`、`internal/harnessmcp/memory_tools.go`、`frontend/src/features/memory/`；测试 `memory_test.go`、`search_test.go`、`memory_integration_test.go`。

**Interfaces：** `Save(ctx,MemoryDraft)(Memory,error)`、`Confirm(ctx,id string) error`、`Delete(ctx,id string) error`、`Search(ctx,MemoryQuery)([]Memory,error)`、`BuildContext(ctx,MemoryQuery)(MemoryContext,error)`；Memory{ID,Scope,WorkspaceID,Kind,Content,SourceSessionID,Status,CreatedAt,UpdatedAt,ExpiresAt}；Query{WorkspaceID,Text,Limit}，Limit≤5；Context{Entries,Bytes,Provenance}。

- [ ] 构造中文检索集：A项目“数据库连接池上限为二十”、B项目同关键词不同约定、user偏好、过期和candidate条目；`TestChineseTrigramScopeAndExpiry`只返回授权active项，短查询返回QueryTooShort。
- [ ] 写 `TestDeleteNotInjectedNextRun`、`TestMemoryContextBounded`：删除后下一run无该内容，≤5条/6000bytes且UTF8完整，来源记录存在；已发出的历史不伪称删除。
- [ ] 实现显式保存/候选确认，自动后台抽取默认关闭；通过Harness MCP和prompt构建受控注入，检索结果标数据来源，不升级为系统指令。
- [ ] UI可编辑/确认/删除/按scope关闭、导出；含秘密fixture文本拒绝作为凭证存储方式，防止简单把整个工具日志当记忆。
- [ ] unit/SQLite真实FTS5/integration/frontend通过并提交。

**验收：** MEM01–MEM05。

## C06：聊天、审批、停止和历史 UI

**依赖：** C01/F05/F04/C02，G2。**Files：** `internal/app/session_api.go`、`frontend/src/features/chat/{Transcript,Composer,ToolCard,ApprovalCard,useRunEvents}.tsx`、`frontend/src/features/history/`；测试 `RunEvents.test.tsx`、`ApprovalCard.test.tsx`。

**Interfaces：** 直接使用F05 RunService和Event DTO；前端通过Wails bindings订阅规范事件；Reconnect使用Get+Replay(afterSeq)，相同eventId/seq不重复。

- [ ] 写 `TranscriptDeduplicatesReplay`、`AcceptedDoesNotRenderCompleted`、`DisconnectDeniesApproval`；模拟delta/tool/approval/compaction重排与断线，不靠整页刷新消除重复。
- [ ] 实现纯文本/Markdown/代码块和工具卡片；Markdown默认禁任意HTML与脚本，不执行网页/工具结果中的命令；附件使用artifactId。
- [ ] 审批显示来源/工具/目标/参数摘要/范围/到期；允许一次、会话范围、拒绝行为与F04相同；停止时不等模型下次token才响应。
- [ ] 真Wails + Pi实例完成发送、排队、授权、停止、重开历史、继续；UI等待状态与后端最终状态一致，失败没有假成功气泡。
- [ ] 跑frontend、integration/E2E，提交。

**验收：** UI02、RUN01/RUN04、E2E01。

## C07：上下文、产物、预算和诊断

**依赖：** C03–C06/F05/F07。**Files：** `internal/artifact/service.go`、`internal/diagnostics/{export,redact}.go`、`internal/app/details_api.go`、`frontend/src/features/{details,artifacts,diagnostics}/`；测试 `artifacts_test.go`、`redact_test.go`、`RunDetails.test.tsx`。

**Interfaces：** `RegisterArtifact(ctx,ExecutionContext,path string)(Artifact,error)`、`ExportDiagnostics(ctx,ExportRequest)(ExportResult,error)`；Artifact{ID,WorkspaceID,RunID,CanonicalPath,Hash,MediaType,Size}；ExportRequest含run范围和目标路径，不默认上传云端。

- [ ] 写 `TestArtifactOutsideScopeDenied`，symlink/junction越界及消失文件不可注册为可访问产物；错误不能fallback打开任意本机路径。
- [ ] 写 `TestDiagnosticExportRedactsSentinelSecrets`：key/header/token/URL query/错误堆栈里注入sentinel，导出命中0；保留错误码、版本、时间和事件ID以可定位故障。
- [ ] 展示有效模型/skills/MCP/记忆来源、配置revision和压缩事件；无法从Pi确定精确上下文时明确estimated/unknown，不编造token图。
- [ ] 展示工具次数/时间/usage与费用来源；无价格显示unavailable，100工具/30分钟阈值前拒绝下一动作，已发模型请求费用不宣称可硬撤销。
- [ ] 运行artifact/diagnostic tests和工作台E2E并提交。

**验收：** ART01–ART03、SEC07、E2E02。
