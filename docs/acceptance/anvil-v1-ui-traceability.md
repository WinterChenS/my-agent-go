# Anvil V1 UI-to-Implementation Traceability

日期：2026-11-05。状态：执行映射，不是完成证据。

本表把 `docs/ui/Agent-客户端产品原型/` 的视觉意图映射到生产 React、Go API、权威数据、Task 和原子 case。原型 HTML、截图、固定数字、toast 和 `assets/studio.js` 不进入生产 bundle，也不能单独使任何 case 通过。

## 页面追踪矩阵

| 原型页面/区域 | 生产 React | Go API / 服务 | 数据权威与外部 oracle | 必备状态 | 外部依赖 / BLOCKED 条件 | Task | 必需 case |
|---|---|---|---|---|---|---|---|
| `index.html` 壳层、导航、工作台卡片 | `app/{Layout,App,Router,navigation}.tsx`、`features/workbench/WorkbenchPage.tsx` | `WorkbenchAPI`、workspace/run/approval/Pi summaries | SQLite + RunService + PiLifecycle；API/DB 对照 | loading/empty/error/blocked | Pi 缺失只阻塞运行卡，不阻塞壳层 | U00/U01 | UI05、I18N01–04、THEME01–05 |
| `index.html` 历史与全局搜索 | `features/history/HistorySidebar.tsx`、`features/search/GlobalSearch.tsx` | C06 会话目录 API；U01 `Search`（`internal/search`） | Pi transcript + SQLite 索引；重启复查 | loading/empty/error/index-unavailable/QueryTooShort | 索引未建立或损坏为 unavailable/修复态 | U01/C06 | UI05、SRCH01、SES01、RUN02、RUN07 |
| `chat.html` 消息流、composer、工具、审批与澄清 | `features/chat/{ChatPage,Composer,MessageList,ToolCallCard,ApprovalCard,ClarificationCard}.tsx` | C06 `StartSessionRun/SendMessage/AbortRun/ResolveApproval/AnswerClarification/ReplayRun/ResolveAttachments/ExportSession` | Pi event stream + RunService；fixture 副作用计数 | loading/empty/error/permission/waiting_approval | Pi/模型凭证缺失时不能启动 run | U02/C06 | UI02、UI06、SES02、SES03、RUN01–07、SEC01–06 |
| `chat.html` 浏览器/产物/引用/执行右栏 | `features/chat/ContextPanel.tsx`、`features/browser/*` | B07 浏览器 API；C07 `ListRunArtifacts/RunDetails` | NativeBrowserHost + 文件 hash + SQLite | empty/error/unsupported/control-uncertain | 缺目标 native engine 或 display 为 BLOCKED | U02/B07/C07 | UI03–04、UI06、ART01–03、E2E03 |
| `agents.html` 单 Agent/Profile 编辑 | `features/agents/{AgentsPage,AgentEditor}.tsx` | `workspace_api.go` 的 `GetProfile/SaveProfile/ProfileConflict`；model/skill/MCP/memory 目录 | versioned JSON + CAS + credential refs；fixture provider 记录的请求 | loading/empty/error/conflict/permission | 模型或资源缺失允许保存但阻塞运行 | U03/C01–C05 | UI07、CFG01–03、MOD01–04、RES02/04 |
| `skills.html` Skill 管理、链接导入和隔离试运行 | **现状：`features/skills/{SkillsSection,ExtensionsSection}.tsx`（旧分区）**；U04 计划的 `SkillsPage/SkillEditor/SkillImportDialog` 未落地 | `SkillAPI` + U04 `CreateSkill/InspectSkillLink/DryRunSkill` | 文件系统 + config revision；marker 文件；用户 agentDir hash | loading/empty/error/untrusted/dependency-missing | 文件权限或外部依赖缺失为 BLOCKED；试运行需模型凭证 | U04（未完成）/C03 | UI08、RES01–05、SEC08；**U07：I18N02/04、THEME01/03/05 在这一页失败**（写死中文 + 直接调色板类名） |
| `mcp.html` MCP server 与工具清单 | **现状：`features/mcp/MCPSection.tsx` + `features/tools/ToolsSection.tsx`（旧分区）**；U04 计划的 `McpPage/McpServerEditor` 未落地 | `McpAPI` | Pi MCP supervisor + fixture server logs | connecting/connected/error/unsupported/disabled | OAuth/协议不支持不得显示 connected | U04（未完成）/C04 | UI08、MCP01–05、SEC03–04；**U07：I18N02/04、THEME01/03/05 在这一页失败** |
| `connectors.html` 定义、健康、权限与动作 | **现状：`app/pages/index.tsx` 的共享 `NotImplementedPage`（feature=connectors）**；C08 未合入 | `ConnectorAPI`（未实现）、app-owned MCP | OS credential store + adapter fixture/专用账户 counters | 只渲染 not_implemented | 缺 OAuth client/测试账户逐 connector BLOCKED | C08（未完成）/U01 | CONN01–08、SEC01–07（NOT_RUN）；U07 的 I18N02–04/THEME02–05 只在 not_implemented 状态上有证据 |
| `schedules.html` 创建、编辑、暂停、立即运行和状态 | **现状：`app/pages/index.tsx` 的共享 `NotImplementedPage`（feature=schedules）**；C09 未合入 | `ScheduleAPI`（未实现）、RunAPI | SQLite occurrence + deterministic clock + linked run/session | 只渲染 not_implemented | Profile/workspace/Pi 不可用时任务 BLOCKED | C09（未完成）/U01 | SCH01–07、RUN03–07（NOT_RUN）；U07 的 I18N02–04/THEME02–05 只在 not_implemented 状态上有证据 |
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

## U07 验收状态（2026-11-08）

本表上面的"生产 React"一列在本次任务里按仓库现状更正：Skills/MCP 仍是旧分区，连接器/定时任务仍是共享
not_implemented 页。U07 的证据因此分成两类。

### 有真机证据的部分（`verify --suite=native --gate=G3`，构建后的 Wails 应用）

| case | 证据要点 |
|---|---|
| UI12 | 880×600 / 1024×720 / 1366×768 / 1440×900 / 1920×1080 五档 × 九页：客户区物理尺寸 = 请求值，页面 dpr = GetDpiForWindow/96，无横向滚动/无控件横向裁剪/无交互元素实质重叠/无不可达控件；WindowFromPoint 证明客户区没有被别的窗口遮挡，右侧原生视图默认不可见 |
| UI13 | 九页 × zh/en × light/dark：可访问名称齐备、文本与语义 token 满足 WCAG AA、真实渲染器可访问性树非空（≥50 节点、≥20 有名字）、键盘 Tab 14 步全部进入 `:focus-visible` 且画出焦点环 |
| UI12.detectors | 向真实页面注入违规结构后，unnamed-control / low-contrast / clipped-control / overlap / truncated-label / 无焦点环 六条检查全部报警；清理后基线恢复零发现 |
| I18N02 | 设置页切语言 → 侧栏九入口与页面文案即时切换、`<html lang>` 更新、无页面重载（timeOrigin 与页面标记不变）、搜索草稿与详情栏状态保持、run/Pi 状态不变、Preferences revision 0→1、磁盘 `config/preferences.json` 为 en-US |
| I18N03 | 真实 CAS 冲突（外部先写一次再点界面保存）→ 界面错误码 CONFIG_CONFLICT 且文案来自当前语言 catalog（中英各一份，互不串台）；导出诊断包输出的日期按 locale 不同（2026年11月8日 11:28 / Nov 8, 2026, 11:28 AM）、数字与体积走各自语言的 Intl 与 units.* |
| THEME02 | light/dark/跟随系统三种选择都解析成 light 或 dark（不产生第三套主题）；偏好写入 API 与磁盘；重启同一数据目录后首屏第一个有内容的采样点已是 dark（无闪烁） |
| THEME03 | 两主题 × 九页抽样控件的 default/hover/active 对比度 ≥3.0，且每页至少有一个控件 hover 或 active 有可见变化；语义 token（含 warning/danger）对比度写入证据 |
| THEME04 | 切主题不重载（timeOrigin/标记不变）、草稿与详情栏保持、run/Pi/BrowserState 绑定值不变、原生子窗口集合不变 |
| THEME05 | 九页 × 两主题的 CDP 截图（文件字节 + sha256）与偏好 revision、窗口/DPI 事实一并落盘 |

### 失败/未覆盖的部分（不得当成通过）

- **I18N04 失败**：en-US 下 Skills 与 MCP 两页整段中文硬编码（`features/skills/SkillsSection.tsx`、
  `features/mcp/MCPSection.tsx` 等旧分区），中文泄漏检查逐页列出原文。这一条只能由 U04 的
  `SkillsPage/McpPage` 迁移修掉，U07 不代替它做产品页重写。
- **THEME01（U00 的静态规则）仍有 667 条违规**：15 个文件里的 `text-slate-*`/`bg-[#...]`/`border-white/*`
  等直接调色板类名（`frontend/e2e/no-mock.spec.ts` 的 direct-color 检查逐条列出 file:line）。
- **审批风险态（waiting_approval 卡片）**：需要一次真实 run，本机缺 Pi 1.5.0（PATH 上 1.4.0 不可用）
  → 未产生；用 warning/danger 语义 token 的对比度作为该状态的色彩基线。
- **费用格式化（formatCost）**：需要 provider 的真实 usage/计价 → 未验证。
- **Windows 150%/200% 系统缩放、macOS Retina、Linux X11/Wayland**：ADR 0005 本轮只验收 win-x64；
  本机 96 DPI，系统缩放需交互式改设置。
- **下载不被主题切换取消、人工接管归属不变**：需要真实 run + 浏览器标签页 → 未验证（THEME04 已证明
  页面不重载、原生子窗口集合不变、BrowserState 绑定值不变）。
- **UI01–UI11 的 native runner 仍缺失**：G3 的 38 条必需 case 没有 native 执行方式，门禁按"配置缺失"
  （exit 2）处理，不当作通过。
