# Anvil V1 开发执行基线

版本：1.1；日期：2026-11-05。状态：增量执行基线；历史任务与证据保留，新增能力尚待各自门禁验证。

这份文档将讨论中未明确的细节收敛成可执行默认值。这些值是本轮设计决策，不冒充用户此前的逐项确认。执行本任务书时以此为准；若发现不可行，走 ADR，不静默替换范围。

## 1. 需求优先级和文档关系

用户后续明确指令 > `2026-11-05-anvil-v1-product-ui-lifecycle-design.md` > 本基线中未被该规格替换的部分 > 本轮分模块计划和验收矩阵 > 之前的方案与调研。本文件的 1.1 修订用于把已批准规格合并进执行口径，不反向降低该规格的权威；仍有歧义时停止并提请用户裁决。历史方案中的时间粗估、候选方案和“后续可选”不构成当前任务。调研结论不是实现完成证据。

仓库已有实施代码、历史提交和部分任务证据；不得再按“空仓库”处理，不得覆盖用户未提交工作，也不得把旧证据重写为新 UI、Pi 生命周期、连接器、调度或应用更新已经完成。

## 2. 硬约束

| ID | 基线 |
|---|---|
| R01 | Go 为 Harness 主体，桌面宿主 Wails；React + TypeScript + Vite + Tailwind CSS + shadcn/ui 为本轮前端默认值 |
| R02 | Windows/macOS/Linux 安装包均为 V1 范围；六个 OS/CPU 目标见下表，禁止把 Windows 验证等同于跨平台完成 |
| R03 | 客户端基础包不包含 Pi、Node、npm、模型权重或独立 Chromium；必要系统 WebView 依赖另计并报告 |
| R04 | 自动发现宿主 Pi；候选范围只允许进入隔离契约探测，缺失/过旧/不兼容时支持一键安装或升级到应用托管版本，失败可回滚且不覆盖外部 Pi |
| R05 | Go 不重写 Pi agent loop、provider 协议和 MCP client；Go 自有工具通过 Harness MCP 接入，权限 hooks 使用薄 Pi extension |
| R06 | 右侧是同一窗口内真实网页视图；独立顶层浮窗、iframe、截图流不满足该项，不能作为默认替代 |
| R07 | 远程网页无 Wails 桌面管理 bindings；原生引擎用公开 API，不以关闭安全隔离或私有 WebKit API 换取通过 |
| R08 | 本地优先、单用户；所有自动化验证使用隔离用户数据、临时 workspace、独立 Pi agentDir，不能改用户现有配置或登录 |
| R09 | 不自动发消息、提交订单或修改远端数据；测试外部副作用全部使用本地受控 fixture/专用测试账户 |
| R10 | 模型/浏览器真实集成验证与 deterministic mock 测试分别记录；mock 通过不能覆盖真实集成缺失 |
| R11 | 正式产品名为 Anvil；V1 支持 zh-CN/en-US 与 Light/Dark，语言和主题属于门禁维度 |
| R12 | V1 交付邮箱、钉钉、飞书、Slack、Notion、GitHub 真实 connector adapter，以及通过普通 RunService 执行的本地调度器 |
| R13 | V1 应用自动检查并后台下载签名更新，不主动重启；下次正常启动应用并健康检查，失败恢复旧程序和数据 |
| R14 | UI 数据来自 Go API/SQLite/文件或进程等权威源；静态原型、硬编码统计、假 loading、点击后直接成功 toast 不是实现证据 |

### 平台交付矩阵

| targetId | 目标 | 最低验收系统候选 | 包 |
|---|---|---|---|
| win-x64 | windows/amd64 | Windows 11 24H2 | NSIS .exe |
| win-arm64 | windows/arm64 | Windows 11 24H2 ARM64 | NSIS .exe |
| mac-x64 | darwin/amd64 | macOS 14 Intel | .dmg / .app |
| mac-arm64 | darwin/arm64 | macOS 14 Apple Silicon | .dmg / .app |
| linux-x64 | linux/amd64 | Ubuntu 24.04，X11 与 Wayland | .deb + AppImage |
| linux-arm64 | linux/arm64 | Ubuntu 24.04 ARM64，X11 与 Wayland | .deb + AppImage |

这些是产品验收目标，不是框架支持已验证事实。F00 必须核实依赖和测试资源；若候选系统无法运行，写出错误证据和调整建议，保持 G0=BLOCKED，不能自行删除平台。Fedora/rpm、Windows 10、macOS 13 及以下不在此版矩阵。

## 3. 版本与依赖策略

- 初始开发 Go 1.27.3、Node 24.16.0、npm 11.13.0；本地 nvm 目录 D:\Program Files\nodejs。执行前检查实际命令路径，避免微信开发者工具 Node 抢先解析。
- Wails 初始验证基线 v3.0.0；Pi 最低支持版本 v1.5.0。必须读取已认证 release 的 RPC/扩展资料，不能用主线文档代替 release 行为。
- Pi 默认候选范围为 `>=1.5.0 <2.0.0`。版本范围只决定是否进入探测：1.5.0 仍核对认证 fixture，新同系列版本通过隔离契约探测后可标 `ready_compatible`；2.x 在新兼容清单和契约套件发布前保持 `incompatible`。增加预认证版本必须跑同一 Pi 契约套件。
- F00 为 React/Vite/Tailwind/shadcn 组件来源、Go SQLite/MCP/凭证库依赖选择满足环境约束的稳定版本，生成 versions.lock.json、go.sum、package-lock.json。禁止 @latest、nightly 或浮动依赖作为可复现构建输入。后续升级是独立变更。
- SQLite 首选 modernc.org/sqlite，必须验证锁定版本支持 FTS5 trigram。无法满足则报告 ADR，不偷偷改成无检索或自造 SQL LIKE 的成功演示。
- MCP server 使用官方 Go SDK 的稳定版本作为首选，具体版本由 F00 查询并锁定；不自己实现完整 MCP wire protocol。
- 允许本机原生 glue 与 cgo，不要求客户端纯 Go 编译；原生 SDK、编译器、GTK/WebKit 版本纳入构建记录。

## 4. V1 功能范围

### 必须交付

工作台、项目与会话历史、流式聊天/停止/恢复、单 Agent/Profile、模型/API key、Skills 本地管理、MCP stdio/Streamable HTTP、统一工具目录、邮箱/钉钉/飞书/Slack/Notion/GitHub 连接器、本地定时任务、资源库、长期记忆、设置与真实观测、Pi 自动发现/兼容探测/安装/升级/回滚、执行记录与产物、右侧原生浏览器和人工接管、Anvil 签名自动更新，以及六目标安装包。所有产品页交付 zh-CN/en-US 与 Light/Dark。

浏览器公共基线：真实网页、地址和标签、前进后退刷新、文本与普通 DOM 元素观察、按 ref 点击/填写、滚动、可见区域截图、手动登录态保存、人工接管、停止、弹窗新标签、下载管理。普通 button/input/select/textarea/checkbox 的 fixture 必须三端通过。

浏览器首版不保证：canvas 操作、拖拽、闭合 shadow root、所有跨域 iframe、可信事件限制页面、任意 OAuth/SSO、最小化/隐藏页面截图、程序化上传。必须有 capability/Unsupported 错误和人工处理路径，不能伪报成功。跨域 frame/closed shadow/trusted-only fixture 的“明确不支持且无副作用”是必测负例，不是未经声明跳过。

### 明确后置

多人/云同步、子 Agent 自动编排、多运行时、Skill 市场、远程服务器、向量库、语音、内置完整终端、Monaco、云分享、任意 OAuth 迁移、自动安装全部第三方 MCP 依赖。V1 连接器自身所需 OAuth 若缺客户端或测试账户，对应真实集成标 BLOCKED，不从范围中删除也不以 mock 替代。

此后置仅冻结首版范围，不删除用户要求的资源/模型管理。若真实 Pi provider 或必需 MCP 只能 OAuth 登录，列为外部依赖与明确缺口，不能冒充已支持。

## 5. 数据与配置权威

- 用户级/工作区/Profile 配置以版本化 JSON 为权威；SQLite 是 workspace/session/run 索引、审批、记忆、产物、连接器、调度与更新事务记录的权威；skills/扩展和产物内容是文件权威；Pi transcript 是消息/分支历史权威。
- 合并优先级：默认 → 用户 → 工作区 → Profile → 会话覆盖。权限和凭证来源不允许项目扩大；数组型资源按稳定资源 ID 合并，同 ID 高层覆盖整项；显式 enabled=false 生效，不能用空数组歧义表达禁用。
- JSON schemaVersion=1，带 revision；写入必须 CompareAndSwap(expectedRevision)，外部修改冲突返回 ConfigConflict，不覆盖。所有有效值可查询来源。
- 独立 PI_CODING_AGENT_DIR；不修改 ~/.pi/agent。V1 首选在客户端重新输入 API key，写入 OS 凭证库；配置只存 credentialRef。非敏感 models/skills/MCP 显式导入。旧 Pi auth/OAuth 不自动读取或复制，保持清晰认证归属。
- 外部文件里的 !command 凭证和可执行扩展不能自动启用。项目资源自动发现必须经过 trust/allowlist 验证；无法阻止不信任代码启动时禁止该工作区运行。
- 凭证库不可用时拒绝持久保存，允许明确的仅当前会话内存凭证；不回退明文文件。

## 6. 运行与权限规范

每个活跃会话一个 Pi worker，默认同时运行上限=2；超出排队。run 状态：queued/starting/running/waiting_approval/stopping/completed/failed/cancelled/interrupted。只有对应 Pi 版本的最终 idle/settled 条件满足，才标 completed；prompt accepted 不表示完成。

RPC LF 分帧，单 record 最大 16 MiB；超过返回 ProtocolRecordTooLarge 并标 failed，不截断 JSON。写命令按 id 匹配响应；未知事件保留原始类型但不崩溃，未知最终完成语义不得瞎猜 completed。stderr 独立持续读取，脱敏后最多保留最近 1 MiB。

事件 envelope：schemaVersion=1、eventId、workspaceId、sessionId、runId、seq、type、occurredAt、payload。seq 按 run 从 1 单调递增；UI 去重。不可丢关键状态；UI 文本增量每 50ms 合并，持久 transcript 仍以 Pi 为准。run 超时默认30分钟，工具超时60秒，浏览器动作15秒；用户可在显式范围内修改。

停止时立即拒绝新的执行请求；2秒内应见 stopping/cancelled 或明确仍在清理状态；先 abort，5秒未退出则终止 worker 进程树；10秒未清理完成记 CleanupFailed 并阻止相同 session 重启，不能显示清理成功。不能保证撤销已发生的副作用。崩溃恢复只恢复 transcript/状态，不自动重放未知结果调用。

工具授权默认：注册且审查过的只读工具自动；工作区 write/edit 默认逐次审批，可授予当前会话同范围写权限；shell、外部 MCP 写操作、浏览器 click/fill/key 逐次审批，可授予当前会话特定工具/站点能力。未知 MCP 工具默认审批，不信任 readOnlyHint。危险外部操作不可因“浏览器已授权”自动放行；V1 无法可靠语义分类时，交互动作均先审批，不假定 click=只读。

approval 绑定 session/run/tool/规范化参数摘要/工作区范围/站点 origin/controlGeneration，有效期60秒；一次性许可仅消费一次。拒绝、到期、UI断开、服务不可达均阻止执行。hook 必须覆盖 Pi built-in、扩展、MCP 与嵌套调用；未能覆盖的执行通道默认禁用，不能宣称全覆盖。第三方扩展会执行本机代码，安装信任与工具审批是两层边界；首版不宣称 OS 沙箱。

预算：每 run 最多100次工具调用、30分钟；有提供方 usage/价格时显示估算费用，并标价格来源与是否估算；缺失 usage/价格时显示 unavailable，不能显示虚假0费用。费用阈值是下一次请求前的软限制，不能承诺撤回已发出的模型费用。

## 7. 记忆和浏览器固定值

记忆 scope=user/workspace，status=candidate/active/deleted；显式记住可激活，自主提取先 candidate。每次检索最多5条、总计最多6000 UTF-8 bytes，再受上下文预算裁剪，显示来源；不把 bytes 冒充精确 tokens。过期/deleted 不参与检索，删除后下一 run 不再注入；已发模型历史无法撤回。

中文 FTS5 trigram，少于3个 Unicode 字符查询返回 QueryTooShort 并提示输入更长关键词；不静默降级成无边界全库扫描。检索先过滤 scope/status/expiry，再排名。V1 自动提取默认关闭，提供用户“保存为记忆”和候选确认路径，避免为自动化提取额外引入隐性模型调用。

浏览器 workspace 级 profile，tab session 级授权；同 workspace 不同会话可以共享登录态但不能操作对方 tab，界面明确说明。每 session 最多4 tab，总计12 tab。控制状态 Agent/Pausing/Human/Resuming，接管递增 controlGeneration，排队动作失效，恢复强制新 snapshot。

取消保证的是不再派发新的动作，不能撤回已提交给native引擎/外部服务的输入。已提交但超时/未知结果标记 OutcomeUnknown；不得自动重放。若不能确认当前动作已静止，保持Pausing/ControlUncertain，不显示已完成接管；用户可停止该tab。人工接管测试需区分queued动作和已派发动作，不能承诺后者物理上绝不延迟发生。

snapshot 单次最多300个元素/40000字符/256KiB JSON；截图最大4MiB。capabilities 由实际 backend 探测，不能只按 GOOS 写死。导航、DOM失效与接管后的旧引用返回 StaleSnapshot。截图带 CSS viewport、像素尺寸、scale、navigationId。

## 8. Pi 安装行为

发现优先级：用户指定 → 已保存路径 → 应用托管目录 → PATH → 常见位置；界面列出所有候选的真实路径、版本、发行形式、来源和 hash，并按确定优先级选择，禁止静默切换。状态为 discovering/ready_verified/ready_compatible/upgrade_available/incompatible/missing/installing/rollback_available/failed。版本进入候选范围后仍须在隔离 agentDir/workspace/provider 下验证 RPC 握手、settled、MCP、extension hook、停止和清理。

用户点击安装后，根据 app 内兼容 manifest 选择精确版本、OS、arch、下载URL和SHA256；URL 只走 HTTPS 官方 allowlist，重定向也校验。下载到 staging，校验哈希，防 ZipSlip/越界 symlink/hardlink，检查版本和 RPC 后原子切换 active 指针。取消/失败不改变 active；保留1个上版。默认不需要管理员、不改系统 PATH、不覆盖外部 Pi。

没有 Node 的机器也要验证独立 Pi 包。若独立发行包不能满足 RPC/扩展，G0 不通过，输出替代安装方案，不能临时将 Node 塞回客户端包中。下载失败/代理不通提供手动选择已有 Pi，不能自动执行任意网页安装脚本。

## 9. Anvil 应用更新行为

应用自动检查稳定渠道并后台下载完整更新包，但不得主动重启。清单与包分别验签，并校验 schema、channel、target、架构、最低升级版本、来源/重定向、大小、SHA-256、数据库迁移版本和 Pi 兼容范围。下载完成进入 ready，用户下次正常启动时由签名 updater helper 交换版本；用户也可在无活跃 run、审批、Pi 安装、浏览器接管、未保存编辑或清理失败时选择退出并更新。

健康检查、数据库迁移或启动失败必须恢复旧程序和数据备份。Windows、macOS、Linux AppImage 和 Linux `.deb` 分别走已定义的平台流程；无权限或缺签名资源时标 BLOCKED。应用更新不得修改外部或托管 Pi。UPD01–14 必须用两份真实版本不同、hash 不同的构建包验证，不能只写状态文件或改 UI 文案。

## 10. 工程交付边界

开发按现有代码和模块计划增量执行，不重置仓库或覆盖已完成任务。每个任务遵守：失败用例 → 最小实现 → 本任务检查 → 记录原始证据 → 独立提交；原生 spike 可先记录人工失败/预期再实现，但必须有可重复 fixture 和最终验收。

测试代码不得为满足覆盖率复制实现。关键负例全部有外部副作用计数器/文件/fixture服务器日志作为 oracle，不能仅断言 UI toast。未具备机器、签名、模型凭证或网络时写 BLOCKED；缺失前置不是通过。
