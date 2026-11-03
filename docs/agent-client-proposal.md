# Go 桌面 Agent 客户端方案

日期：2026-11-03。状态：供评审的技术方案，尚未实施。

执行入口已更新为 `docs/README.md`。本文保留设计演变；具体范围、数值、任务和门禁以 `docs/superpowers/specs/2026-11-03-v1-execution-baseline.md` 及其执行计划为准。

## 1. 产品定位与结论

构建一个本地优先的个人 Agent 工作台：既能围绕代码仓库执行编码任务，也能借助 skills 和 MCP 完成文件处理、检索等通用任务。用户已经指定 Go 客户端、Pi 底座，以及 skills、模型、MCP、tools、记忆管理；其余选型是本方案建议。

建议采用 **Wails v3 + React + TypeScript + Go Harness + Pi RPC 子进程 + SQLite**。用户明确要求跨平台安装包：Windows、macOS、Linux 均为首版交付范围，三端一起验收。建议覆盖 x64/arm64 六个目标，具体最低系统版本与 Linux 发行版范围在 P0 验证后锁定。首版以单用户、本地数据、用户自行配置模型凭证为边界；云同步、多人协作、插件市场、多 Agent 自动协作放到后续。

这里的 Pi 指原 badlogic/pi-mono、现 earendil-works/pi 的 toolkit。使用完整 pi-coding-agent，而非只接 pi-agent-core，以复用会话、上下文压缩、skills、模型目录和 MCP。Go 掌管应用与 harness，Pi 掌管执行循环。按用户要求，客户端安装包不包含 Pi、Node 或其他 Pi 运行时，使用宿主机已有的本地 Pi；未安装时在客户端引导安装。Pi 与其运行时作为独立环境依赖管理。

## 2. 调研事实和版本基线

- 当前官方仓库重定向至 earendil-works/pi；查询时最新 GitHub release 为 v1.5.0，发布时间 2026-11-02。主线 package.json 名称为 @earendil-works/pi-coding-agent，Node 要求 >=22.19.0。旧教程中的 @mariozechner 名称不能直接当作当前安装基线。
- 本次查阅主线提交：9c9ce8547e5a88cbe834a53e9a6d83a789d08e47。下面的接口判断来自当时官方文档；实施前对固定 release 做契约验证，不能假定主线文档与每个历史版本一致。
- Pi RPC 是 stdin/stdout 上的 JSONL 协议，适合 Go 等非 Node 宿主；它不是 JSON-RPC 2.0。命令接收成功不代表任务结束，应按固定版本的事件协议判断完成。
- 当前 Pi 已有原生 MCP，支持 stdio、Streamable HTTP、OAuth、工具暴露控制；旧的“Pi 无内置 MCP”资料不适合作为当前主线依据。旧 SSE transport 不支持。
- skills 采用 Agent Skills 形式；模型支持兼容 API endpoint、自定义目录元数据及 provider extension。
- Wails 官方当前把 v3.0.0 标为 Windows/macOS/Linux 桌面稳定版本，要求 Go >=1.26。项目初始锁定已验证的稳定版本，避免跟随 nightly。

来源见文末。以上是外部事实；以下产品结构、数据模型、时间估算均为设计建议。

## 3. 桌面与前端选型

| 方案 | 与本项目的关系 | 判断 |
|---|---|---|
| Wails + React | Go 直接承担桌面服务，系统 WebView 承载前端 | 首选 |
| Wails + Vue | 同样适合 Go 客户端 | 如果团队熟悉 Vue，可整体替换 React |
| Tauri + React + Go sidecar | 需要额外 Rust 壳，再管理 Go 和 Pi 子进程 | 可行，但当前增加一层语言与生命周期管理 |
| Electron + React + Go sidecar | Chromium 环境统一，仍需 Go 服务和 Pi | 当跨平台浏览器一致性成为硬要求时再考虑 |
| Fyne/Gio | Go UI 路线 | 本方案判断：复杂聊天、Markdown、diff、配置编辑会需要更多自建 UI 工作 |

前端建议：React + TypeScript + Vite，shadcn/ui 负责表单、对话框和布局；业务流式状态用轻量 store，设置查询用请求缓存层。首版只引入实际需要的依赖。Markdown 展示先做安全渲染；代码编辑和 diff 在确有需求时接 Monaco；交互终端后置，需要 PTY 才接 xterm.js。

不要让 UI 状态管理框架接管 Pi 的执行循环。Go 提供命令、查询、事件三类应用接口；React 只展示规范化后的业务事件与状态。

客户端安装包仅包含 Wails/Go 应用、前端静态资源和必要的自有扩展。Pi 及其运行时在宿主机单独安装，不计入客户端安装包，但计入完整使用环境的磁盘占用。Monaco、Linux WebView 运行库等仍可能增加客户端体积；各平台安装包和安装后占用由 P0 实测，不沿用先前捆绑 Pi 时的估算。

## 4. 架构与责任边界

```text
React 工作台
  └─ Wails bindings / 应用事件
      └─ Go 应用服务
          ├─ Workspace / Profile / 配置编译
          ├─ 任务状态、权限、记忆、搜索、审计
          ├─ SQLite / 文件系统 / 系统凭证库
          ├─ 本地 Harness MCP 服务：暴露 Go 工具
          └─ PiRuntimeAdapter
              └─ Pi RPC 子进程（一个活跃会话一个进程）
                  ├─ agent loop / provider / compaction
                  ├─ Pi 会话文件
                  ├─ skills / extensions / 内置 tools
                  └─ Pi MCP client → 外部 MCP / Harness MCP
```

### Go 负责

应用生命周期、窗口与托盘、工作区、会话索引、Profile、配置来源、凭证引用、资源安装更新、长期记忆、权限规则、预算、诊断日志、Pi 进程管理，以及最终给 UI 的稳定接口。

### Pi 负责

模型协议适配、消息与工具执行循环、上下文压缩、原生会话树、skills 加载、扩展执行、MCP 连接与工具发现。MVP 不另写一套 Go LLM adapter 或 Go MCP client 来重复承担相同职责。

### 薄 TypeScript 扩展负责

连接 Pi hooks 与产品规则：执行前查询 Go 的权限决策、注入已检索记忆、转发必要的运行状态。业务规则和存储留在 Go。优先通过 Pi 已有扩展和 RPC 能力实现，不 fork 内核；缺失的能力逐项核实后再扩展协议。

### Go 工具怎样进入 Pi

Go 在本机提供 Harness MCP 服务，工具如 memory_search、memory_propose、artifact_register。Pi 把它当普通 MCP server 使用，不需要发明“RPC 注册 Go 函数”协议。

建议该服务使用随机 loopback 端口 + 每个 worker 独立的短期 bearer token，只绑定 127.0.0.1，启动时通过子进程环境注入。请求关联 workspace/session/run，校验工具参数及授权范围；只允许明确的本机调用，不开放给局域网。权限查询接口与 MCP 工具接口在同一宿主内分开鉴权和路由，不把内部管理接口暴露给模型。

浏览器 UI 仍走 Wails bindings。MCP 是模型到 Go 工具的通道。后续若要求完全无监听端口，可改为 stdio helper + 本机 IPC，届时单独验证各平台。

## 5. 必需的产品模块

| 模块 | 首版能力 | 实现归属 |
|---|---|---|
| 工作区 | 绑定目录、项目指令、资源范围与信任状态 | Go |
| 本地 Pi 环境 | 自动发现、手动选路径、版本与能力检查、一键安装、诊断与更新入口 | Go |
| Profile | 命名组合：模型、skills、MCP、工具、记忆策略、权限 | Go，编译为 Pi 配置 |
| 会话 | 流式输出、停止、继续、历史；保留 Pi 原生树标识 | Pi + Go 索引 |
| 模型 | provider、endpoint、API 类型、能力、上下文限制、测试连接 | Go 管理，Pi 调用 |
| Skills | 本地导入、编辑、校验、启停、来源和版本 | Go 管理，Pi 加载 |
| MCP | stdio/HTTP 配置、连接诊断、认证状态、工具列表和暴露方式 | Pi 执行，Go 管理界面 |
| Tools | 内置/扩展/MCP 工具统一展示；按 Profile 选择与授权 | Pi 工具体系 + Go 策略 |
| 记忆 | 用户偏好、项目约定、任务总结；检索、查看来源、修改删除 | Go |
| 上下文 | 展示指令、资源和记忆来源；压缩记录 | Pi + Go |
| 权限与预算 | workspace 范围、执行前决策、批准记录、轮数/时间/费用限额 | Go + Pi extension |
| 产物与诊断 | 文件路径、diff、调用记录、错误分类、脱敏日志导出 | Go + UI |

MCP 状态面板不是默认 RPC 已有完整 CRUD 的承诺。实施时逐项核实官方命令、RPC 和扩展 API；配置落盘和安全重启可作为首版路径。OAuth 先支持 API key/Bearer 和环境变量；远端 OAuth 在后续阶段完成授权、刷新和存储验证。

工具定义需要记录 name、来源、schema、能力范围、超时、输出大小、授权策略。自定义 Go 工具通过 Harness MCP；自定义 Pi 行为通过 TS extension。Skills 是指令和资源包，不等同于可执行工具插件。

## 6. 配置管理：单一来源与生效时机

Go 管理的配置以可导出的 JSON 文件为源；SQLite 记录索引、运行快照与安装元数据，不再保存另一套可独立编辑的配置副本。skills/扩展源码以磁盘文件为源。

建议层次为：应用默认 → 用户设置 → 工作区覆盖 → Profile 选择 → 会话临时覆盖。权限只能在既有授权边界内收紧；项目文件不能覆盖系统凭证引用或扩大信任范围。显示每个有效值的来源。

Pi 有自己的合并与自动发现规则，不能把上述产品层次想当然地套上去。配置编译器应生成一份会话有效配置，并明确处理 .pi、.agents 和祖先指令发现。受信项目可以使用原生资源；不受信项目不得自动加载其可执行扩展、MCP 命令或凭证命令。AGENTS 等上下文文本也要标明来源。原型阶段验证哪些发现规则可关闭，无法关闭时明确展示限制。

为客户端设置独立 PI_CODING_AGENT_DIR，避免修改用户原有 ~/.pi/agent。建议按工作区/Profile/配置版本生成运行目录，同一配置可以复用；凭证使用系统凭证库引用，尽可能通过最小化环境注入，禁止写入命令行参数和日志。

本地 Pi 可执行文件复用宿主安装，客户端会话配置默认独立。提供显式导入用户已有 Pi 配置/资源的入口，导入时展示来源与差异；不因选择了本地 Pi 就直接改写其全局配置。

应用设置写入使用临时文件 + 原子替换；外部修改产生冲突时提示比较，不静默覆盖。配置保存后生成新版本：新会话立即使用；运行中会话沿用快照。明确支持热更新的字段才即时更新，否则等空闲后重建 worker。需要收回权限时立即阻止新的调用，而非仅改变下一会话配置。

## 7. 会话、事件与失败恢复

初期每个活跃会话一个 Pi worker，设置并发上限，历史会话不常驻进程。不要让多个 worker 同时写同一 Pi 会话文件。

Go 对外提供 StartRun、SendMessage、AbortRun、ResumeSession、GetRun、SubscribeEvents 等应用能力。这些是本应用接口，不假定 Pi 恰好存在同名命令。PiRuntimeAdapter 负责映射至固定版本 RPC。

规范事件带 schemaVersion、eventId、workspaceId、sessionId、runId、seq、timestamp、type、payload；事件类型覆盖文本增量、工具开始/进度/结果、授权请求、压缩、完成与错误。Go 分配递增 seq，UI 重连先取快照再从游标读取增量。

RPC 实现必须处理：按 id 关联命令、LF 分帧、超长消息上限、stderr 独立消费、stdout 持续读取、背压、超时、未知事件兼容。Go 不要直接使用默认 64KB Scanner 限额处理大型结果。

prompt 的 success 表示接收成功；在当前文档协议中自动继续结束以 agent_settled 为准，不能看到 agent_end 就把整个任务标为完成。事件行为以锁定版本契约测试确认。

Pi 会话文件是对话和分支历史的权威；SQLite 是产品的工作区、任务、记忆与会话索引的权威。审计日志单独记录外部副作用。UI 文本增量可批量推送，关键状态和工具结果持久化；不必永久存每个 token。

进程异常退出后任务标记 interrupted，保留最后已确认状态并允许继续。恢复会话不等于恢复正在执行的外部操作；发帖、写远端数据等非幂等调用结果不明时，先核查结果，不能自动重放。用户停止时先发 abort，超时再关闭 stdin/终止进程树；Windows 验证 Job Object 或等价清理机制，防止 MCP/脚本孤儿进程。

## 8. 记忆设计

分清三种东西：Pi 会话历史用于当前对话延续；项目文件/AGENTS 用于明确约定；长期记忆保存跨会话有价值的信息。上下文压缩由 Pi 负责，不等同于长期记忆。

第一版使用 SQLite + FTS5，不部署向量数据库。检索按 scope、关键词、时间和人工确认权重排序，再按 token 预算注入。中文检索要验证 tokenizer 效果，可用应用分词后的搜索字段或 trigram 方案，不能假设默认分词就足够。语义召回确有收益时，再加入 embedding 和混合检索。

Memory 字段建议包含 id、scope(user/workspace)、kind、content、sourceSession/sourceArtifact、createdAt、updatedAt、expiresAt、status、supersedesId。跨项目默认不注入项目记忆。来源必须可追溯。

写入流程：用户明确要求记住 → 保存；模型自主总结 → 生成候选，用户确认后生效。检索内容作为带来源的数据注入，不把其中的文本升级为系统指令。修改删除同时更新索引、缓存和后续注入；提供关闭记忆、导出和按 scope 清空。首版禁存凭证和完整工具日志作为记忆。

## 9. 权限与凭证

权限能力从最小闭环开始：只读、允许工作区写入、外部副作用需确认三类策略。策略按工具及实际资源判断，不能只相信 MCP readOnlyHint。对执行 shell、运行 skill 脚本、扩展加载分别建模；路径校验覆盖 symlink、junction、UNC 等情况。

执行前 Pi extension 调用 Go 决策；需要确认时复用 RPC extension UI 的 confirm/select 流程。拒绝、超时、UI 断连均阻止该次执行。授权绑定具体调用和参数摘要，不能把“允许一次”扩大成永久权限。MCP 内嵌调用也要验证是否经过相同 hook。

扩展和 skill 脚本可以执行本机代码，shell 也可能间接访问任意资源；工具 hooks 并非 OS 沙箱。首版只面向用户信任的本地项目和扩展。若提供不受信任务模式，应先实现容器或平台隔离，并验证文件/网络范围，不能只靠 prompt 和审批弹窗宣称隔离。

API key/Bearer 保存于 OS 凭证库；Pi OAuth 的原生存储是独立问题。后续 OAuth 接入必须决定是否通过受支持的适配层接凭证库，或在受限运行目录保留 Pi token 文件并明确说明，不能宣称 Pi 所有 token 天然都在系统凭证库中。

## 10. 页面与使用闭环

建议五个主要入口：工作台、资源库、记忆、运行记录、设置。工作台左侧工作区与会话，中间聊天及工具卡片，右侧按需打开上下文、diff 和产物详情。

资源库切分 Profiles、Models、Skills、MCP、Tools。每项显示来源、状态、作用域、版本、生效时间和诊断结果。Profile 可以创建“编码”“研究”“文档”等组合，首版不自动切换角色。

首次使用闭环：选择工作区 → 配置一个模型并测试 → 使用默认 Profile → 发起任务 → 观察工具调用 → 必要时授权 → 查看产物 → 确认可保留的记忆 → 下次会话检索使用。

## 11. 建议目录与数据

```text
cmd/desktop/              Wails 入口
internal/app/             用例与应用服务
internal/workspace/       工作区与信任
internal/profile/         Profile 与配置编译
internal/runtime/pi/      RPC、进程、事件适配
internal/resources/       skills、models、MCP、tools 配置
internal/policy/          授权与预算
internal/memory/          记忆与检索
internal/storage/         SQLite、迁移、文件、凭证引用
internal/harnessmcp/      Go 工具的 MCP 服务
frontend/                 React 工作台
pi-extensions/            薄 TS hooks
packaging/                各平台发行
docs/                     设计与接口说明
```

数据库核心实体：workspaces、profiles 的索引、sessions、runs、run_events、approvals、memories、artifacts、resource_installs。运行记录携带 piVersion、profileRevision、effectiveConfigHash 和资源版本，支持诊断与复现；不要复制第二份完整 Pi 会话树。

资源安装记录 origin、revision、hash、scope。第一版支持本地导入；之后再做 Git/npm 安装、升级、回滚和依赖诊断，不默认安装时执行任意脚本。

## 12. 交付顺序与验收

以下为一名熟悉 Go/前端的开发者的粗估，取决于运行时集成、权限和跨平台要求，不是承诺工期。

| 阶段 | 时间粗估 | 交付与验收 |
|---|---|---|
| P0 集成与发行验证 | 约 1 周 | 确立 Pi 兼容版本基线；三平台发现并启动宿主 Pi；验证无 Pi 时安装引导；真实模型流式输出、工具调用、停止、恢复；stdio/HTTP MCP；确认弹窗；六个目标构建，锁定支持矩阵并测量体积 |
| P1 跨平台最小可用版本 | 2–3 周 | 工作区、会话、模型管理、本地 skills、MCP 配置与诊断、Profile、历史和错误恢复；三端安装包均能完成本地文件任务并再次打开继续 |
| P2 Harness 完整化 | 2–3 周 | 长期记忆、上下文来源、Go tools、执行前权限、预算、产物、资源版本；拒绝调用确实不执行，跨项目记忆隔离 |
| P3 发行完善与扩展 | 1–2 周起 | OAuth、自动更新回滚、性能与诊断；扩大支持环境，三端完成全场景回归 |

三端安装包 MVP 目标约 3–4 周；包含完整 P2/P3 的版本约 6–9 周起。估算以能够使用六个目标的构建/测试环境，以及签名、公证所需账户和凭证可用为前提；这些条件不满足时重新估算。原型失败时先调整工期与边界。

P0 的关键决策：优先直接集成宿主 Pi 的 RPC，避免额外依赖宿主 Node。若 RPC 缺少必要的结构化管理能力，先通过 Pi 扩展补足；只有明确需要 SDK bridge 时才评估，其宿主 Node 需求须单独检测和展示，不能静默变成客户端强制依赖。

发布采用客户端版本与宿主 Pi 兼容版本矩阵，不附带 Pi/Node。用户可以使用已安装的 Pi，也可以通过客户端引导选择受支持的安装方式。依赖由安装方式决定：独立发行形式不预设需要 Node，npm 形式则检测相应 Node/npm。第三方 MCP 所需 Python、uv、浏览器等单独检测和说明。客户端更新与 Pi 更新分开管理。

### 本地 Pi 环境管理

环境页展示：当前 Pi 路径、实际版本、安装来源、CPU 架构、启动所需环境、RPC/扩展能力检测结果、诊断信息和当前客户端支持范围。PiLocation 记录路径和来源；每次启动前核实真实版本，不能只依赖上次扫描结果。

发现顺序建议为用户指定路径 → 已保存且仍有效的路径 → 进程 PATH → 各平台常见安装位置；搜索结果供用户选择，不能在多个版本中静默切换。macOS 从 Finder 启动时的 PATH 与终端可能不同；Windows 的 npm shim、带空格路径与脚本入口需适配。解析后以明确的可执行文件与参数启动，避免把用户输入拼成 shell 命令；shell alias 不视为可执行文件。

状态至少包括：未发现、依赖缺失、版本不兼容、可用、启动失败。检查版本之外还要做受限时长的 RPC/扩展能力探测；探测使用独立配置与临时会话，不发模型请求或连接用户外部 MCP。协议识别不依赖 npm 名称和版本号的单一判断。

未安装时提供「一键安装 Pi」，显示推荐兼容版本、来源、目标位置和下载大小。用户主动点击后，自动识别 OS/CPU、下载、校验、解压安装、检查版本与 RPC 能力，并保存可执行文件路径。界面展示下载与安装进度，支持取消、失败重试和诊断；同时保留手动选择路径和安装说明入口。不能启动时仍可进入设置、管理资源和查看历史，发起任务入口展示修复步骤。

默认优先评估官方独立发行包：本次 v1.5.0 release 提供 Windows/macOS/Linux 的 x64/arm64 压缩包及 SHA256SUMS。先在 P0 验证独立包的 RPC、自有扩展、MCP 与资源加载，确认通过后作为一键安装默认渠道。客户端安装包不含这些文件，首次使用时联网下载。官方脚本/npm 安装作为替代渠道，按官方要求检测或安装 Node；不能将 npm 方式的 Node 需求误认为所有独立包都需要宿主 Node。

自动安装器是 Go 的独立模块：使用应用维护的兼容版本清单选择明确版本，不直接无条件跟随 latest；优先从官方 release 下载，通过随客户端发布或经过签名验证的清单固定 SHA-256，官方 SHA256SUMS 用于交叉核对。HTTPS 下载和同源哈希校验不能替代发布者签名。解压限制路径穿越和越界链接，先写 staging 目录，验证通过再切换版本指针；失败保留当前可用版本，不留下被识别成可用的半成品。

建议安装到当前用户的独立、按版本划分的 Pi 管理目录，由客户端记录归属，不覆盖已有 Pi，不默认修改系统 PATH，也不需要以管理员身份写入系统目录。客户端以绝对路径启动；如用户希望终端也使用这个 Pi，可另外选择创建命令入口。升级沿用相同下载/验证流程，保留上一个可用版本以便回滚；卸载客户端时将是否清理托管 Pi 与用户数据分开选择。具体目录遵循 OS 规范，避免依赖安装目录写权限。

管理能力包括选择版本、重新扫描、连接自检、查看脱敏日志与升级入口。宿主 Pi 可能同时供终端和其他程序使用：不静默升级/卸载全局 Pi；对客户端引导安装且记录了归属的版本，才能提供明确的移除操作。更新不会中断活跃会话，worker 使用启动时固定的路径/版本；不兼容版本给出已验证版本的安装或切换入口。

兼容性策略先支持经过集成测试的版本范围；更高未知版本允许自检和提示，不假定协议兼容。新版本的能力使用 feature detection，缺失时说明功能限制或禁止运行。客户端随附的薄 TS 扩展也要纳入 Pi 兼容测试。

### 首版安装包与跨平台工程要求

跨平台意味着同一套业务代码生成各系统/CPU 对应的安装包，不是一个文件在所有系统通用。以下为建议交付矩阵，不代表当前已构建或测试完成。

| 系统 | 建议架构 | 安装包 | 发行要求 |
|---|---|---|---|
| Windows | x64、arm64 | NSIS 安装程序 .exe | 使用宿主 Pi；检测 WebView2，提供缺失时的安装路径；发行签名 |
| macOS | Apple Silicon arm64、Intel x64 | 分架构 .dmg，内含 .app | 签名应用及内嵌可执行文件，公证、staple；暂不要求 Universal 包 |
| Linux | x64、arm64 | AppImage + .deb；.rpm 按支持范围提供 | AppImage 验证捆绑运行库，系统包声明依赖；发布明确的发行版/桌面会话支持列表 |

Wails 官方文档提供 Windows NSIS、macOS 应用包和 Linux AppImage/deb/rpm 的打包路线。具体安装格式、CPU 和第三方依赖的组合必须由本项目验证，不能据框架支持就宣布产品支持。

每个安装包包含桌面应用、前端静态资源、自有内置扩展与资源清单，不含 Node/Pi。宿主 Pi 经过发现和选择后，以解析后的明确路径启动。用户数据存 OS 标准应用数据目录，与安装目录分离，升级不覆盖配置、会话和记忆，也不覆盖宿主 Pi 安装。

CI 使用 Windows/macOS/Linux 原生构建环境，CPU 架构分别构建与验证；同一发布版本统一生成校验和、依赖清单及版本 manifest。签名、公证在打包阶段执行；自动更新按系统和架构分流，并验证更新签名。首版至少支持手动安装新版并保留数据，自动更新可在 P3 完成。

平台差异集中在 adapter：数据路径、凭证库、进程树、shell、文件权限、文件监听、原生对话框和更新器。Windows shell 工具与 Pi 的 bash 需求在 P0 验证，通过环境检查引导配置兼容 shell 或实现工具适配，不能假定用户已装 Git Bash；Unix 同样检测工具依赖。skills 标注 OS/命令依赖，不兼容的资源显示不可用原因；不承诺任意第三方 skill/MCP 天然跨平台。

验收分两条路径：无 Pi/Node 的干净环境可安装并打开客户端，完成缺失环境诊断、一键安装和重新检测；已有兼容 Pi 的环境可完成模型请求、读写文件、MCP、停止/恢复、退出清理、升级保留数据和卸载。补充测试多个 Pi、路径失效、宿主更新为不兼容版本，以及独立 Pi 与 npm 安装方式；自动安装还要测试断网、取消、校验失败、解压失败、重试及回滚。离线机器需要事先安装兼容 Pi 或准备独立安装文件，客户端不保证从零离线安装完整运行环境。Linux 额外验证目标发行版的系统库、X11/Wayland、文件选择器和凭证服务；在支持矩阵外明确标为未验证。

## 13. 必须覆盖的验证场景

1. RPC：命令交错、流式消息、超大结果、Unicode、取消、压缩/重试后最终完成。
2. 生命周期：启动失败、worker 崩溃、应用退出、进程树清理、恢复后无重复副作用。
3. 配置：优先级、项目自动发现、来源追踪、外部编辑冲突、运行快照、旧配置迁移。
4. 权限：内置 tools、MCP、codemode 子调用均覆盖；拒绝/超时/断连不可执行；明确扩展和 shell 的信任边界。
5. 记忆：作用域、来源、确认、删除生效、中文检索、注入 token 预算。
6. 发行：Windows/macOS/Linux 目标架构均验证客户端单独安装、无 Pi 引导、已有 Pi 发现与启动、版本不兼容处理；覆盖中文与空格路径、WebView 依赖、凭证不进日志、签名/公证和更新失败恢复；Linux 明确发行版支持范围。

## 14. 新增需求：右侧浏览器与 Agent 浏览器操作

用户希望在工作台右侧打开浏览器，Agent 能直接操作，用户可以观察并接管。此处以用户描述的交互为目标，不推断 Codex 内部实现。该能力作为产品方向纳入方案，具体浏览器承载路线尚待原型选择，不把它视为 Wails 内置能力。

架构分开处理 Browser Surface（展示与人工输入）和 Browser Controller（Agent 操作）：Pi → Harness MCP 的 browser 工具 → Go Browser Service → 浏览器；右侧面板与 Browser Service 订阅同一个 session/tab 的状态。页面文本、元素快照、截图、操作结果必须来自用户正在看的页面，不能各自运行一套浏览器。

建议工具包含 open/navigate、list_tabs/select_tab、snapshot、click、fill、scroll、wait、screenshot、back/forward、download。优先使用元素语义和刷新后的引用定位，必要时配合截图坐标；不把任意 JS 执行作为默认模型工具。工具返回明确的 tabId、URL、状态与错误，元素引用过期时重新观察。浏览器工具统一经过现有权限和运行日志体系。

三种路线的判断：

| 路线 | 优点 | 工程代价与限制 |
|---|---|---|
| 系统原生 WebView 嵌入右侧 | 直接呈现网页，复用系统组件 | 三端引擎和控制接口不同；Wails 多窗口/SetURL 不等同于可嵌入的跨平台 BrowserView，须做平台原型 |
| 本地 Chromium + 右侧实时画面与输入转发 | 浏览器控制统一，复用本机 Chrome/Edge，安装包保持轻量 | 面板呈现的是浏览器画面流，不是原生 DOM；中文输入法、剪贴板、弹窗、缩放、上传下载和可访问性需专门实现 |
| 专用 Chromium/CEF 浏览器组件 | 便于追求统一的真实内嵌浏览器体验 | 新增运行时下载、集成维护和内存成本；运行时可按需下载，但总体磁盘占用仍会增加 |

在用户进一步要求 Wails 的详细实现方案后，推荐先验证 **NativeBrowserHost + 三端系统 WebView**，以实现同窗口右侧真实网页，详见同目录 `wails-browser-design.md`。本机 Chromium + 画面流作为备选，不作为真正嵌入网页的替代验收结果。系统 WebView 的 DOM 基础操作与完整可信输入分级验收，不承诺三端自动化天然一致。React iframe 不作为通用网页浏览器方案，站点可通过 frame-ancestors 等策略禁止嵌入，且跨域 DOM 不能直接读写。

浏览器控制默认通过 Go CDP adapter 评估，避免仅为浏览器再引入强制 Node 依赖；若采用 Playwright/MCP，要明确其独立依赖、安装方式与版本兼容。复用本机浏览器是启动一个由客户端管理的专用实例/用户数据目录，不默认接管用户正在使用的个人浏览器和登录配置。缺少兼容浏览器时提供按需安装入口，浏览器运行时不随客户端基础包发布。

浏览器会话保留登录状态，提供查看与清除入口。用户接管时暂停 Agent 的写操作，恢复时重新读取页面，避免两方同时输入。登录、验证码可交由用户处理；提交、发送、购买等外部副作用按现有策略显示操作目标并审批。远程页面不获得 Wails/Go 桌面管理 bindings，页面文本作为不可信数据处理。调试接口仅本机可达，按 tab/session 限定 Agent 操作范围。

原型验收覆盖三端：导航、登录态保存、iframe 页面观察、元素定位、表单填写、截图、页面变化等待、暂停/人工接管、恢复、多标签、上传下载、弹窗及进程退出清理。右侧面板拖动尺寸和中文输入是重点体验项。先建立浏览器原型的可行性与性能结果，再估算新增工期；原有 3–4 周 MVP 粗估不包含本次新增的完整浏览器集成。

参考：[Wails Window API](https://wails.io/reference/window/)、[Playwright CDP](https://playwright.dev/docs/api/class-browsertype#browser-type-connect-over-cdp)、[Playwright WebView2](https://playwright.dev/docs/webview2)、[CSP frame-ancestors](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Security-Policy/frame-ancestors)。

GooeyPi 源码调研补充：它以 Electron webview 承载真实页面，Pi extension 经本机 HTTP bridge 调用主进程 WebContents/CDP，没有解决 Wails 的三端原生浏览器嵌入问题。可借鉴其服务分层、DOM ref、常驻 tab、坐标归一化和授权机制；详细证据见同目录 `gooeypi-browser-review.md`。浏览器承载路线仍待原型决定，保留用户已选的 Wails。

## 15. 外部资料索引

- [Pi 官方仓库](https://github.com/earendil-works/pi)
- [Pi v1.5.0 release](https://github.com/earendil-works/pi/releases/tag/v1.5.0)
- [Pi RPC](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md)
- [Pi RPC extension UI](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-extension-ui.md)
- [Pi MCP](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md)
- [Pi models](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md)
- [Pi skills](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/skills.md)
- [Pi configuration](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/configuration.md)
- [Wails desktop status](https://wails.io/status/)
- [Wails v3 release](https://wails.io/blog/wails-v3-released/)
- [Wails 构建与打包](https://wails.io/guides/build/building/)
- [Wails Windows packaging](https://wails.io/guides/build/windows/)
- [Wails Linux packaging](https://wails.io/guides/build/linux/)
- [Tauri sidecar](https://v2.tauri.app/develop/sidecar/)
- [React](https://react.dev/)、[Vite](https://vite.dev/guide/)、[shadcn/ui](https://ui.shadcn.com/docs)

当前工作目录没有既有代码和 Git 仓库，因此本方案按新项目设计；只写入本方案文档，没有初始化工程、安装依赖或生成产品代码。
