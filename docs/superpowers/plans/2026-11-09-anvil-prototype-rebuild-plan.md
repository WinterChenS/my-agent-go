# Anvil 原型重建实施计划

> 逐任务实施。每项先写失败用例并确认失败原因，再做最小实现，运行 focused 验证，保存原始证据，最后用 Task ID 独立提交。阶段只按 G0 → G1 → G2 推进。

**目标** · 九页原型复刻与主题/i18n，多运行时发现和同步，最后实现原型完整会话。
**架构** · Wails 桌面窗口承载 React 页面，Go 提供类型化 Wails 绑定、运行时适配和持久化，SQLite 保存应用索引。
**技术栈** · Go 1.27.3、Wails v3.0.0、React、TS、Vite、Tailwind/shadcn、modernc SQLite。
**规格** · [原型重建设计](../specs/2026-11-09-anvil-prototype-rebuild-design.md)
**范围** · 仅本设计的阶段 0、1、2。当前 Windows x64 / Windows 10 22H2（build 19045）先跑通。

## 执行方式与证据

计划中的命令由 `R0-00` 新建。例 `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --case=R0-01 --target=win-x64`，结束时执行 `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 gate --id=G0 --target=win-x64 --evidence=<absolute-path>`。G1/G2 同理。命令透传进程退出码，0 通过，1 断言失败，2 输入或证据不完整。验收矩阵 `tests/acceptance/rebuild.json` 串行维护。证据保存在隔离目录 `<evidence>/<sha>/<target>/<case>/`，含命令、退出码、真实/界面数据来源、OS build、应用版本、运行时版本、日志或截图的相对路径与 SHA-256。不同提交的结果先确认代码与用例适用范围，再在门禁目标提交上重跑必要用例；门禁拒绝错误 SHA 或错误平台。

每项的 **失败用例** 是先写的断言，**实现** 给出入口和边界，**验收** 是必须运行的结果。阶段变动由用户决定。先按每个 Task 查工作树，禁止同时开始多个后续阶段。

## 阶段 0 · 九页复刻

| Task | 失败用例与实现 | 验收证据 |
| --- | --- | --- |
| R0-00 构建与门禁 | 建 `go.mod`、`cmd/anvil/`、`frontend/`、`cmd/devtools/` 和脚本。先验证空矩阵、缺证据、哈希错误、错 SHA/平台、FAIL 证据分别触发预期退出码。再实现 Wails 窗口、前端构建和矩阵判定 | 在当前 Win10 构建并打开窗口；命令原样返回 0/1/2；空矩阵必失败 |
| R0-01 原型基准与共用壳 | `frontend/src/shell/`、`frontend/src/design/`。从 manifest 锁定 9 个 route、原型提交、CSS token、图标和视口。先写宽窄屏壳层和导航断言，再实现侧栏、顶栏、滚动、焦点和菜单 | 原型/实现同条件截图，导航与回退正常，窄屏无非预期横向溢出 |
| R0-02 主题 | `frontend/src/preferences/theme.ts`。先测默认 system、浅色/深色手动优先、切回 system 后即时同步、重启保持设置。语义 token 统一应用，Windows WebView 系统事件通过实测确认，必要时通过 Wails 转发宿主事件 | 三种模式切换录像或截图；真实系统主题切换和重启的结果 |
| R0-03 国际化 | `frontend/src/i18n/` 和偏好持久化。先测词条键一致、语言选择重启恢复、中文/英文 aria 与动态状态、用户文本保持。实现设置及对话快捷切换 | 全部页面的双语清单，缺失 key 检查通过；文本和无障碍名称切换生效 |
| R0-04 工作台 | `frontend/src/pages/workbench/`。先测各模块、入口、新建对话和空态/样例态，再按 `index.html` 复刻 | 桌面与窄屏对照图、导航和键盘检查 |
| R0-05 对话 | `frontend/src/pages/chat/`。先测侧栏、输入、项目选择、菜单、右栏四分区、窄屏抽屉、停止/失败/上下文/审批/澄清样例。再按 `chat.html` 完成 UI；页面业务绑定留到 G2 | 逐状态截图、焦点返回、输入法与发送快捷键、右栏抽屉关闭验证 |
| R0-06 Agent | `frontend/src/pages/agents/`。先测列表筛选、详情、Agent 配置表单与进入对话，再复刻 | 样例/空态、菜单及配置对照证据 |
| R0-07 Skill | `frontend/src/pages/skills/`。先测详情、创建、步骤和演练交互，再复刻 | 列表、详情和工作流截图与键盘检查 |
| R0-08 MCP | `frontend/src/pages/mcp/`。先测服务筛选、详情、工具区、配置弹窗，再复刻 | 页面与弹窗对照；运行时未接入时正常页显示真实状态 |
| R0-09 连接器 | `frontend/src/pages/connectors/`。先测连接状态筛选、详情、授权样例及关闭，再复刻 | 各原型连接状态截图和交互证据 |
| R0-10 定时任务 | `frontend/src/pages/schedules/`。先测列表、详情、创建编辑、运行状态，再复刻 | 模态与表单、任务状态对照 |
| R0-11 资源库 | `frontend/src/pages/library/`。先测资源类型筛选、详情和空态，再复刻 | 窄宽屏列表、筛选及详情证据 |
| R0-12 设置 | `frontend/src/pages/settings/`。先测分区、搜索、深链接、语言/外观与键盘，再接入 R0-02/03 | 九页通过设置实时变化；重启保存；设置各面板原型对照 |
| G0 | 在隔离样例入口对照九页原型，在正常桌面入口验证空态与不可用行为；逐页固定截图与交互清单 | 所有 R0 用例通过，九个视口和本机 Wails 验收完整 |

## 阶段 1 · 发现、注册、资源同步

计划入口集中在 `internal/runtime/`。适配器最小接口为 `Discover(ctx) ([]Candidate, error)`、`Probe(ctx, candidate) (Registration, error)`、`Inventory(ctx, registration, kind) (Snapshot, error)` 和 `VerifyModel(ctx, registration, model) (ModelStatus, error)`。类型声明运行时/来源/认证/能力状态。新 `internal/catalog/` 保存注册和快照，`internal/desktop/` 负责 Wails 绑定。阶段 2 再加会话接口。

| Task | 失败用例与实现 | 验收证据 |
| --- | --- | --- |
| R1-01 扫描及注册 | 同路径不同大小写/shim、带空格路径、旧版本、探测超时、刷新竞争、拔除二进制。实现固定搜索顺序、规范化 ID、版本与健康状态，SQLite 事务 upsert | 真机识别、重扫无重复、移除后状态、单个探测失败与取消；用户目录无写入 |
| R1-02 Pi | 测版本过低、可用、隔离、RPC 探测失败、认证缺失。实现 `internal/runtime/pi/` 适配器 | 真 Pi 隔离探测、握手及目录记录 |
| R1-03 Codex | 测仅装桌面端时状态、app-server 握手、版本和未认证。实现 `internal/runtime/codex/` | 真 Codex CLI 探测及握手。原生会话留给 G2 |
| R1-04 Claude Code、OpenCode | 分成两个提交，分别验证原生安装路径、CLI 版本、探测状态和资源能力。实现各自 adapter，输出支持能力及来源 | 两类运行时真实安装发现与注册，授权/能力按矩阵标注 |
| R1-05 模型与自定义模型 | 测来源去重、未授权、无效端点/ID、错误密钥、默认模型回退、失败不覆盖。实现 `internal/catalog/models.go`、系统凭证存储及设置表单 | 至少一个可配置的适配器经真实请求验证自定义模型；重启保持，日志及数据库无明文凭证 |
| R1-06 Skill 和本地工具 | 分开测试根目录边界、软链/Windows junction、损坏 Skill、同名不同源、资源删除、未授权命令。实现元数据扫描/读取、库存和来源说明 | 从隔离的真实配置同步并证明删除更新、坏数据隔离、同步不运行命令 |
| R1-07 MCP | 测 stdio/远端配置、头或环境变量脱敏、恶意路径、同步失败保留旧快照、显式探测。实现声明导入及受控枚举 | 从真实配置同步，明确操作后连接测试服务并枚举；同步时无子进程、无网络请求 |
| R1-08 UI 接入 | 测发现、刷新、未安装、未认证、同步失败/过期与默认配置。通过绑定把目录与结果接入原型 Agent、设置、Skill、MCP 页面 | Windows 从首次扫描到资源展示全链路 |
| G1 | Pi 和 Codex 是可运行的必测基准，Claude Code/OpenCode 必须验证真实发现与注册。每类资源至少有一个真实同步用例，其他运行时按支持矩阵逐项验收 | 任一必测结果缺失、失败或阻塞，阶段不能推进 |

## 阶段 2 · 完整会话

计划入口 `internal/chat/` 和 `frontend/src/pages/chat/`。运行时新接口 `OpenSession(ctx, spec)`、`ResumeSession(ctx, nativeID)`、`Send(ctx, input)`、`Abort(ctx, runID)`。投递 `RunEvent{sessionID, runID, seq, kind, payload}`，客户端按 ID/序号合并。SQLite 事务维护索引和应用状态，原生历史来源按适配器记录。

| Task | 失败用例与实现 | 验收证据 |
| --- | --- | --- |
| R2-01 真实会话 | 先 Pi 后 Codex，分别测试新建、发送/追问、流式、停止竞态、超时、认证失败、回退、并行串线和断线恢复。接入工作台、Agent 和历史入口 | 两种真实运行时逐项跑通基本闭环及对应失败路径 |
| R2-02 项目和历史 | 测隔离目录、重启、分页、搜索、置顶、重命名、归档、删除、分支、上下文压缩。新增会话及项目索引，验证原生恢复 | 重启、无重/漏条目、无串项目，分支与压缩各有真实运行时证据 |
| R2-03 上下文与审批 | 逐项做文件/目录/Skill、@ 引用、工具消息、澄清和权限卡。先测撤销、拒绝、会话错配、路径逃逸和停止后迟到事件 | 真实接入附件与至少一个受限工具，允许、拒绝、取消、澄清各有真实事件 |
| R2-04 右栏 | 按执行、输出物、引用、浏览器四个独立验收。测真实来源映射、产物回到原始轮次、路径限制、窄屏抽屉、浏览器状态与权限 | 每栏真实数据或浏览页面；关联轮次、返回动作和响应布局的证据 |
| R2-05 导出及外部分享 | 测 HTML/Markdown/JSON 内容转义、输出路径、取消、显式确认、隐私范围及失败。外部分享服务来源须先明确并单独记录 | 本地文件与会话一致，外部分享须真实服务回执；缺服务记 BLOCKED |
| G2 | 将原型会话功能逐条映射到 R2 用例。用至少两个真实运行时证明基本对话。每项其余能力至少指定一个真实适配器或本地实现作证 | 全部必测用例通过才完成会话交付 |

每阶段验收报告记录当前支持的运行时及平台范围。Windows 本机优先完成，Windows 11、macOS、Linux 没有实机证据前记 `NOT_RUN`，功能范围变更和额外页面业务实现等待用户另行下达。
