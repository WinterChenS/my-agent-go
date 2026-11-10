# Anvil Rust + Tauri 重建技术方案

2026-11-10 · `RUST-DESIGN` · 目标分支 `codex/anvil-rebuild`

## 目标与范围

以 `57cb4ce` 为制定方案时看到的基线。复用现有 React 页面、原型、验收用例意图和任务顺序。先替换桌面宿主、构建与门禁工具，复跑受影响用例，再删除分支里的 Go 源码及构建入口。最终产品、开发构建、CI 和验收不依赖 Go 或 Wails。产品名称、仓库名、任务 ID 和原型保持可追溯。开始迁移前协调者记录实际 HEAD 与当前任务，先结清已派发的写入，统一切换实施路线。

阶段顺序仍是 G0 九页原型、主题与中英文 → G1 多 Agent 发现注册和同步 → G2 完整会话。换栈迁移放在 G0 内，迁移子门禁通过后再续做剩余页面。原型范围、平台、真实运行时、指定 subagent 和 D 盘规则继续按现有约定执行。Windows 10+ 优先，当前以 Windows 10 22H2 x64（build 19045）取证。macOS 和 Linux 后续在各自环境实测，落盘根未指定前记录为 BLOCKED 或 NOT_RUN。

## 技术栈与代码边界

| 层 | 选择和职责 |
| --- | --- |
| 桌面 | Tauri v2，Rust 主进程承载窗口、路由、主题偏好、系统交互和 IPC。Windows 使用 WebView2 |
| 前端 | 继续使用 React、TypeScript、Vite、Tailwind 和 shadcn，复用现有 JSX、样式、路由、语言和测试。原型 CSS 和交互是视觉验收基准 |
| 后端 | Rust、Tokio、Serde。原生 CLI、会话、模型、权限与持久化留在 Rust，前端通过类型化桥接调用 |
| 数据 | SQLite，计划使用 `rusqlite` bundled，实测 FTS5 trigram。采用 SQLite 事务维护应用索引。原生历史按各运行时的会话契约恢复 |
| 工具 | Cargo workspace、`rust-toolchain.toml`、`Cargo.lock`、Node 24.16.0 和 npm lockfile。Rust 实现 devtools，PowerShell 保留为环境设置和退出码透传入口 |

`src-tauri/` 持有窗口配置、commands、capabilities 与宿主偏好实现。共享 D 盘路径验证在 `crates/anvil-paths/`。门禁与原生运行器在 `tools/devtools/`。G1 再建 `crates/anvil-runtime/` 及 catalog 模块，G2 再建 chat 模块。安装、编译、打包和测试的工具版本在首项迁移任务验证后锁定。所有 Rust 构建使用 lockfile。

官方参考 [Tauri 构建前置条件](https://v2.tauri.app/start/prerequisites/)、[Tauri 配置](https://v2.tauri.app/reference/config/)、[Tauri commands 与 channels](https://v2.tauri.app/develop/calling-rust/)、[Cargo 配置](https://doc.rust-lang.org/cargo/reference/config.html)。

## 前端复用与交互性能

前端增加一个宿主桥接入口。用 Tauri commands 读取及写入偏好，保留 D 盘 `ANVIL_DATA_DIR` 下 `preferences.json` 的主题和语言格式及失败回退行为。保留 `ANVIL_WEBVIEW_DATA_DIR` 及原有窗口尺寸和初始路由验收入口。迁移前盘点 WebView 存储，验证产品偏好可从应用数据恢复。若发现仍有需要迁移的用户状态，先补明确的数据转换用例和验收。首屏在应用挂载前得到本次保存的主题和语言，避免启动闪白或错误语种。系统主题变化实时同步，手动主题优先。生产构建要验证直接打开九个页面路由及刷新后的行为。

样式从原型提取 token，沿用尺寸、留白、字体、图标、键盘和焦点规则。Tailwind 用于样式组织，shadcn 用于可访问的基础控件，组件最终样式以原型验收为准。会话流通过 Tauri Channel 传输有序增量，前端按轮次合并更新，历史消息保留稳定组件标识，滚动离开底部后不抢滚动位置。G2 用固定长会话和真实流式场景记录输入、滚动和增量更新表现。测出瓶颈后再决定虚拟列表或 Markdown 优化。视觉对照和 Windows agent-desktop 实测继续分别取证。

## 多 Agent 发现、同步与使用

- Rust 中定义可动态分发的适配器接口，以稳定字符串标识注册。基础接口提供 `discover` 与 `probe`。模型、Skill、本地工具、MCP、自定义模型和会话按独立能力启用。共享服务仅从注册表查找适配器。新增运行时只改适配器和应用注册入口，并新增对应验收。
- 适配器处理安装位置、版本、认证、协议解析、错误映射及进程收尾。共享层管理扫描调度、去重、超时和 SQLite 事务。探测不安装、不登录或自动执行导入的 MCP 命令。资源快照按运行时实例和来源保存，刷新失败保留上一份结果并标记过期。已检测、认证可用和能力不支持分别展示。
- 首批 Pi 和 Codex 为可运行验收基准，Claude Code 和 OpenCode 必须有真实发现注册证据。本机 Pi 使用现有安装和登录。未知 ID 的测试适配器必须从注册一路走到同步、展示和会话分发。各运行时的真实桌面证据单独保留。
- 开会话时固定运行时实例、模型、工作区及权限。用原生会话 ID 恢复。前端增量携带会话 ID、轮次 ID 和序号。停止受理与终态分开，后台处理超时和子进程清理。运行时不可用时保留历史并报告原因。自定义模型验证走真实适配器。
- 主 WebView 只暴露当前任务需要的结构化 commands。外部网页或产物预览隔离在单独的 WebView 或受控表面，禁止访问本地文件、Agent 及命令 IPC。Rust handlers 同时校验调用窗口与资源权限。G2 加入从外部内容调用本地命令被拒绝的用例。[Tauri capabilities](https://v2.tauri.app/security/capabilities/)

## D 盘与工具链

Windows 的实际落盘卷必须是 D。启动前配置并验证 `CARGO_HOME`、`RUSTUP_HOME`、`CARGO_TARGET_DIR`、构建中间目录、临时目录、npm 与浏览器缓存。只读取本机已有工具和运行时安装可以位于其他盘。工具链安装若产生无法重定向的写入，记录 BLOCKED。应用在启动 Tauri builder 或插件前验证应用数据和 WebView2 数据绝对路径的实际卷。初始窗口设置 `create: false`，由 Rust 显式指定已校验的 D 盘 WebView2 路径后创建。验收核对进程实际使用的目录及可取得的写入证据。SQLite、日志、截图和证据沿用 D 盘。Pi Agent 自身写入是唯一例外。

自定义模型凭证的 Windows 首选设计为使用系统保护能力加密、密文写到 D 盘。进入 R1-05 前验证所选方案的实际写入位置。其他 Agent 的写入路径同样预检。无法证明合规则保持 BLOCKED。可写路径检查覆盖相对路径、跨卷 junction、目录不存在和不可写，禁止回退。

## 切换与验收

1. 用 Rust 完成宿主、偏好、路径预检及验收工具。迁移期间旧 Go 代码作为历史参考，Rust 取证不调用它。
2. 保留现有矩阵 case ID、证据字段与退出码。用相同判定夹具验证 0、1、2，再冻结迁移基准 SHA 上已实现的 G0 case ID 清单，在 Tauri 实机构建上按清单重跑。迁移子门禁只评估 win-x64，其他目标按实际情况记录 NOT_RUN 或 BLOCKED。失败保留证据，继续修复当前迁移任务。
3. 删除 Go 源、模块文件、Wails 依赖和构建调用。对新提交运行无 Go 环境的编译、前端、门禁和原生复验。只在新提交证据完整后关闭迁移子任务，再继续 G0。
4. G1/G2 的用户范围按 [既有原型重建设计](2026-11-09-anvil-prototype-rebuild-design.md)执行。原计划的 Go 接口和 Wails 绑定由 Rust 计划替代。外部分享服务与非 Windows 验收前置仍在对应任务解决。
