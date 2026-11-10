# Anvil Rust + Tauri 迁移实施计划

> 协调者按任务派发给 `deepseek-2/deepseek-v4.1-flash`，独立复核、实测并推进。每个任务先记录失败用例，再提交最小实现并保留复验证据。

**目标** · 从当前重建分支切换到 Rust + Tauri，产品及验收工具完全移除 Go 依赖。
**架构** · 现有 React 前端接入 Tauri。Rust workspace 分离桌面宿主、D 盘路径检查和验收工具。
**规格** · [Rust + Tauri 重建设计](../specs/2026-11-10-anvil-rust-tauri-design.md)
**范围** · Windows 10+ 先行，当前 Win10 22H2 x64。保持 G0 → G1 → G2，迁移在 G0 内完成。

## 派发前

- 先让当前阶段正在修改旧宿主或验收脚本的实施者交回结果或暂停写入。记录干净基准 SHA、当前 R0 状态、现有矩阵与可用证据。按这个基准冻结已实现的 win-x64 G0 case ID 清单，RS03/RS04 使用同一份清单。若工作树带有其他改动，先保留并协调其归属。不得从过期 SHA 覆盖这些改动。
- 使用 [实施规范](../specs/2026-11-09-anvil-rebuild-execution-standard.md) 中的指定模型、真实桌面、本机 Pi 和 D 盘要求。此前 [R0-00 开工单](2026-11-09-anvil-r0-00-kickoff.md) 的证据结构和调度规则继续适用。工具版本和构建入口采用本计划。
- 任务证据保存到 D 盘 `.tmp\evidence\<sha>\win-x64\<caseId>\`。构建锁定后按最终 SHA 重跑。仅在产物符合 Rust 方案时用它标记通过。

## 迁移任务

### R0-RS01 · 工具链与 D 盘预检

- [ ] 记录 Windows build、WebView2、MSVC/SDK、Rust 实际版本和工具链路径。以最小 Win64 MSVC 编译证明构建前置可用。先在 PowerShell 中确认 D 盘写入目录，再启动 Cargo 或安装工具。
- [ ] 建 Cargo workspace、`rust-toolchain.toml`、`Cargo.lock` 和 `crates/anvil-paths/`。锁定已验证的 Rust、Tauri、Node 及依赖。
- [ ] 首批测试覆盖 `validate_d_path` 对已有 D 盘目录、缺失子目录、其他盘、跨卷 junction 和不可写路径的行为。用可控路径夹具覆盖拒绝分支，额外在 Windows 验证实际卷解析和临时路径。检查失败即退出，禁止创建其他盘夹具。
- [ ] 验收 `cargo test --locked -p anvil-paths`，附实际 D 盘路径、命令退出码、工具版本和哈希。MSVC 或工具写入不合规时记 BLOCKED。此任务只验证最小工具链和路径层。

### R0-RS02 · Tauri 宿主接入现有前端

- [ ] 建 `src-tauri/`、Tauri 配置和前端宿主桥接。先写偏好读写、启动恢复、直接路由及 WebView 数据路径用例。记录旧入口行为及迁移前的参考结果。
- [ ] 接入现有 Vite 构建。Tauri 在预检后创建窗口，显式使用 D 盘数据根和 WebView2 用户数据目录。窗口配置 `create: false`，在 Tauri 初始化前校验路径。用 Rust commands 接入主题、语言和测试探针，保持现有偏好格式及测试所需信号。仅给受信任主窗口开放实际需要的命令。
- [ ] 验收 `cargo test --locked -p anvil`、前端测试与生产构建。agent-desktop 实测打开应用、原型页面导航和刷新、三种主题及两种语言、重启恢复。记录实际数据目录与两次启动的证据。

### R0-RS03 · Rust 门禁与原生运行器

- [ ] 建 `tools/devtools/`，保留 `verify --case=... --target=win-x64`、`gate --id=... --target=win-x64 --evidence=...` 和 `ANVIL_EVIDENCE_ROOT`。更新 PowerShell 包装器，用 Cargo 构建 Rust 二进制并原样透传进程退出码。
- [ ] 先用证据夹具验证空矩阵、遗漏任务、未知目标、无效 SHA、跨卷或越界路径、哈希失败、有效 FAIL 和完整 PASS。实现原有 evidence schema、矩阵及 0/1/2 规则。复用现有用例 ID、命令与截图断言语义。
- [ ] 把现有原生 runner 按任务逐项迁移。保留 agent-desktop 的真实窗口、操作及结果解析。记录 Go 与 Rust 用例映射及仍待实现项。开发用夹具作为契约测试证据。
- [ ] 验收 `cargo test --locked -p anvil-devtools`，以及 `scripts/devtools.ps1` 的真实退出码。对所有已实现的 G0 用例重新构建和取证。G0 未完成的任务继续保留必测位。

### R0-RS04 · 切换并删除 Go

- [ ] 更新 AGENTS.md、CI/构建命令、协调者派发入口和运行手册。删除 Go/Wails 源、模块文件、旧 devtools 和所有活动构建或验收引用。旧提交、历史证据与原型作为历史资料保留。
- [ ] 在没有 Go 可调用的进程环境重新运行 `cargo fmt --all -- --check`、`cargo test --workspace --locked`、前端构建、Tauri 生产构建和 Rust devtools。检查分支无受跟踪的 Go 源或模块文件，构建、门禁和客户端启动无需 Go。
- [ ] 协调者在最终 SHA 用 agent-desktop 按冻结清单重跑已实现的 G0 原生用例，复验 D 盘写入并记录 Windows build、WebView2、Rust/Tauri 版本和哈希。RS01–RS04 全部通过才关闭迁移。失败修复当前任务并重跑。迁移完成只证明换栈，G0 仍需其余页面和证据齐全。

## 恢复 G0 并推进 G1/G2

- **G0**。以实际矩阵和原生证据确定最早未完成的页面任务。依原计划复刻其余原型界面，统一在 Tauri 客户端验收。只在 G0 正式 gate 返回 0 后开始 G1。
- **G1**。沿用 R1-01 至 R1-08 的任务与验收。Rust 适配器注册、SQLite 快照、模型、Skill、本地工具、MCP 分任务实现。至少保留一个未知 ID 的自动化契约用例，四类 Agent 的真实覆盖按现有矩阵执行。
- **G2**。沿用 R2-01 至 R2-05 的任务与验收。先走 Pi 与 Codex 的真实文本会话。以类型化 Tauri command 和 Channel 接入前端，后续依次实现历史、项目、附件、审批、右栏和导出分享。每项单独取证，外部内容与本地命令权限隔离。
- 每个实施 Task 仍使用指定 subagent。协调者逐项复核，通过后自动推进。同一任务未通过时，保留候选 SHA 与失败证据。任何阶段必测项 BLOCKED 时保持门禁关闭。
