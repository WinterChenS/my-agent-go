# Anvil V1 UI-to-Implementation Traceability

日期：2026-11-05。状态：执行映射，不是完成证据。

本表把 `docs/ui/Agent-客户端产品原型/` 的视觉意图映射到生产 React、Go API、权威数据、Task 和原子 case。原型 HTML、截图、固定数字、toast 和 `assets/studio.js` 不进入生产 bundle，也不能单独使任何 case 通过。

## 页面追踪矩阵

| 原型页面/区域 | 生产 React | Go API / 服务 | 数据权威与外部 oracle | 必备状态 | 外部依赖 / BLOCKED 条件 | Task | 必需 case |
|---|---|---|---|---|---|---|---|
| `index.html` 壳层、导航、工作台卡片 | `app/Layout.tsx`、`features/workbench/WorkbenchPage.tsx` | `WorkbenchAPI`、workspace/run/approval/Pi summaries | SQLite + RunService + PiLifecycle；API/DB 对照 | loading/empty/error/blocked | Pi 缺失只阻塞运行卡，不阻塞壳层 | U00/U01 | UI05、I18N01–04、THEME01–05 |
| `index.html` 历史与全局搜索 | `features/history/HistorySidebar.tsx`、`features/search/GlobalSearch.tsx` | C06 会话目录 API；U01 `Search`（`internal/search`） | Pi transcript + SQLite 索引；重启复查 | loading/empty/error/index-unavailable/QueryTooShort | 索引未建立或损坏为 unavailable/修复态 | U01/C06 | UI05、SRCH01、SES01、RUN02、RUN07 |
| `chat.html` 消息流、composer、工具、审批与澄清 | `features/chat/{ChatPage,Composer,MessageList,ToolCallCard,ApprovalCard,ClarificationCard}.tsx` | C06 `StartSessionRun/SendMessage/AbortRun/ResolveApproval/AnswerClarification/ReplayRun/ResolveAttachments/ExportSession` | Pi event stream + RunService；fixture 副作用计数 | loading/empty/error/permission/waiting_approval | Pi/模型凭证缺失时不能启动 run | U02/C06 | UI02、UI06、SES02、SES03、RUN01–07、SEC01–06 |
| `chat.html` 浏览器/产物/引用/执行右栏 | `features/chat/ContextPanel.tsx`、`features/browser/*` | B07 浏览器 API；C07 `ListRunArtifacts/RunDetails` | NativeBrowserHost + 文件 hash + SQLite | empty/error/unsupported/control-uncertain | 缺目标 native engine 或 display 为 BLOCKED | U02/B07/C07 | UI03–04、UI06、ART01–03、E2E03 |
| `agents.html` 单 Agent/Profile 编辑 | `features/agents/{AgentsPage,AgentEditor}.tsx` | `workspace_api.go` 的 `GetProfile/SaveProfile/ProfileConflict`；model/skill/MCP/memory 目录 | versioned JSON + CAS + credential refs；fixture provider 记录的请求 | loading/empty/error/conflict/permission | 模型或资源缺失允许保存但阻塞运行 | U03/C01–C05 | UI07、CFG01–03、MOD01–04、RES02/04 |
| `skills.html` Skill 管理、链接导入和隔离试运行 | `features/skills/{SkillsPage,SkillEditor,SkillImportDialog}.tsx` | `SkillAPI` + U04 `CreateSkill/InspectSkillLink/DryRunSkill` | 文件系统 + config revision；marker 文件；用户 agentDir hash | loading/empty/error/untrusted/dependency-missing | 文件权限或外部依赖缺失为 BLOCKED；试运行需模型凭证 | U04/C03 | UI08、RES01–05、SEC08 |
| `mcp.html` MCP server 与工具清单 | `features/mcp/{McpPage,McpServerEditor}.tsx` | `McpAPI` | Pi MCP supervisor + fixture server logs | connecting/connected/error/unsupported/disabled | OAuth/协议不支持不得显示 connected | U04/C04 | UI08、MCP01–05、SEC03–04 |
| `connectors.html` 定义、健康、权限与动作 | `features/connectors/{ConnectorsPage,ConnectorEditor}.tsx` | `ConnectorAPI`、app-owned MCP | OS credential store + adapter fixture/专用账户 counters | loading/empty/error/permission/outcome-unknown | 缺 OAuth client/测试账户逐 connector BLOCKED | C08/U01 | CONN01–08、SEC01–07、I18N02–04、THEME02–05 |
| `schedules.html` 创建、编辑、暂停、立即运行和状态 | `features/schedules/{SchedulesPage,ScheduleEditor}.tsx` | `ScheduleAPI`、RunAPI | SQLite occurrence + deterministic clock + linked run/session | loading/empty/error/paused/waiting_approval | Profile/workspace/Pi 不可用时任务 BLOCKED | C09/U01 | SCH01–07、RUN03–07、I18N02–04、THEME02–05 |
| `library.html` 产物库、预览、导入导出 | `features/library/{LibraryPage,ArtifactPreview}.tsx` | C07 `ListArtifacts/GetArtifact/ImportArtifact/ExportArtifact/PreviewArtifact`；C06 `ResolveAttachments`（加入会话） | 文件系统为内容权威，SQLite 为索引；path/hash/size | loading/empty/error/missing/changed | 路径权限或文件消失显示 blocked/missing | U05/C07 | UI09、ART01–03 |
| `library.html` 记忆分区 | `features/library/MemorySection.tsx` | `MemoryAPI` | SQLite memory 表 + next-run injection | loading/empty/error/candidate/expired | DB/工作区 scope 不可用为 BLOCKED | U05/C05 | UI09、MEM01–05 |
| `settings.html` 通用/模型/权限/安全 | `features/settings/{SettingsPage,GeneralSettings,ModelSettings,PermissionSettings,SecuritySettings}.tsx` | settings/model/policy APIs | versioned JSON + CAS + OS credential store | loading/error/conflict/credential-store-unavailable | OS 凭证库不可用时拒绝持久秘密 | U06/C02/F04/F07 | UI10、CFG01–04、MOD01–04、SEC01–08 |
| `settings.html` Pi 运行时 | `features/settings/PiSettings.tsx` | PiLifecycle/PiInstall APIs | 实际可执行路径/version/hash + probe + active pointer | discovering/missing/incompatible/installing/rollback_available/failed | 网络、受信包或目标包缺失为 BLOCKED | U06/F06/F08 | UI10、PI_DISC01–02、PI_COMP01–04、PI_INS01–03、PI_UPG01–03 |
| `settings.html` Anvil 更新 | `features/settings/UpdateSettings.tsx` | UpdateAPI | manifest/package signatures + updater transaction + process version/hash | checking/downloading/verifying/ready/rollback/failed/blocked | 签名、旧/新包、目标权限或 helper 缺失为 BLOCKED | U06/F09/X04 | UI10、UPD01–14 |
| 设置/工作台中的观测 | `features/observability/ObservabilityPage.tsx` | Diagnostics/Usage APIs | run/tool/approval records；缺失 usage=unavailable | loading/empty/error/unavailable/redacted | provider 不提供 usage/价格时为 unavailable | U06/C07 | UI11、ART03、RUN06 |

## 跨页约束

| 约束 | 实现入口 | Task | case |
|---|---|---|---|
| Anvil 品牌、`anvil://`、`ANVIL_*` 与旧数据迁移 | `cmd/myagent/main.go`、`internal/app/{env,brandmigrate}.go`；打包元数据归 X03 | U00/X03/X04 | UI05、BRAND01–03、PKG06–07、UPD09–10 |
| zh-CN/en-US key、formatter 和即时切换 | `frontend/src/i18n` | U00/U07 | I18N01–04 |
| Light/Dark、跟随系统和 semantic tokens | `frontend/src/theme`、`index.css` | U00/U07 | THEME01–05 |
| loading/empty/error/blocked/permission/retry | `components/states/AsyncState.tsx` + 各 API 状态 | U00–U07/C08/C09 | UI05–13、CONN07、SCH06 |
| 880×600 至 1920×1080 与平台缩放 | built Wails native suite | U07/B07 | UI12–13、I18N04、THEME03/05 |
| 原生 child view 层级与状态不丢失 | BrowserOcclusionCoordinator + NativeBrowserHost | B07/U02/U07 | UI03–04、THEME04、BR04–06/11–14 |
| 禁止生产 mock/演示统计/点击即成功 | static scanner + API/DB/process oracle | U07/F01 | GOV01–04、UI05–13 |

## 原型内容处理规则

- 可以迁移：信息架构、空间关系、导航层级、组件视觉语汇和已批准交互意图。
- 必须替换：固定统计、固定会话、假连接状态、假进度、静态 loading、直接成功 toast、“完全控制”默认值和演示账号。
- 不进入 V1：子 Agent、自动委派、多运行时、Skill 市场和云分享入口。
- 外部依赖缺失：对应 connector/OAuth/签名/目标机器 case 为 BLOCKED；页面必须显示真实 blocker，不得以 disabled mock card 冒充集成完成。
