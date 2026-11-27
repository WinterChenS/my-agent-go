# Anvil 内置 Pi 实施方案

> **给实施 agent**　按 `subagent-driven-development` 或 `executing-plans` 逐项实施。在仓库规定适用的情况下，实施由 `deepseek-2/deepseek-v4.1-flash` subagent 执行，Windows 桌面交互用 `agent-desktop` 取证。每项保留失败用例、修复后验证、原始证据和带任务 ID 的独立提交。

**目标**　安装 Anvil 时一并安装 Pi Agent，运行时始终使用包内 Pi，卸载 Anvil 时一并移除包内 Pi。Pi 的默认数据根为 `~/.anvil`，用户级 Skill 根为 `~/.anvil/skill/`。

**架构**　Tauri 把官方 Pi npm 包、私有 Node 和既有插件所需资源作为安装资源交付。Rust 将同一份包内启动配置交给现有 catalog、会话、登录和扩展入口，继续使用当前 Pi CLI 与 JSONL RPC。

**技术栈**　Rust `1.95.0`、Tauri v2、现有 React 前端、官方 `@earendil-works/pi-coding-agent`、私有 Node `24.16.0`、现有签名更新器。

## 产品契约

| 场景 | 预期 |
| --- | --- |
| 首次安装 | 安装器自带 Pi 和 Node。已有 WebView2 的受支持 Windows 环境在断网、没有 Pi、Node 或 npm 的情况下能安装、启动并显示内置 Pi 版本。模型登录或推理仍按模型提供方的网络要求运行。 |
| 外部 Pi 共存 | Anvil 的探测、聊天、登录、定时会话及现有子代理都使用包内版本。外部 Pi 仍可在系统终端独立使用。 |
| 路径 | 包内运行时位于 Tauri 安装资源 `runtime/pi/`。`PI_CODING_AGENT_DIR` 设为 `HostConfig.data_dir`，默认 `~/.anvil`。用户级 Skill 增删、目录扫描和会话路径指向 `<data_dir>/skill/`。显式 `ANVIL_DATA_DIR` 沿用现有宿主校验和隔离语义，Windows D 盘约定继续适用。 |
| 设置与升级 | 保留现有设置页、Pi 标识和能力展示，安装状态显示「已内置」及探测到的版本。Pi 检查更新复用 Anvil 更新器。装到损坏或不完整的包时显示错误和重装提示。 |
| 卸载 | 使用当前平台的标准卸载方式移除 Anvil 安装资源，包括私有 Node、Pi 代码和包内插件资源。默认保留 `~/.anvil` 中的用户数据，并保留外部 Pi 和其配置。 |
| 既有功能 | 沿用现有模型管理与 OAuth、MCP、Chat RPC、历史、定时任务、Agent 定义和子代理扩展实现、工具策略、项目资源信任规则及前端布局。 |

2026-11-27 调查基线为 `main` 的 `02d440c`。本机 AppData 中 Pi npm 包的 `package.json` 显示 **`1.6.0`**，入口为 `dist/bundle/cli.js`。首轮锁定 `1.6.0`，实施前用当前 Anvil 实际探测结果核对它与已有可用版本一致；如不同，以现有可用且满足 `>=1.5.0 <2.0.0` 的版本固定锁文件并记录差异。本任务的目标是改变交付方式，Pi 版本升级、换第三方子代理插件、改会话生命周期或增加终端界面均在本次范围之外。[Pi CLI 模式](https://github.com/earendil-works/pi/blob/v1.6.0/packages/coding-agent/docs/cli.md) · [Pi 配置文件](https://github.com/earendil-works/pi/blob/v1.6.0/packages/coding-agent/docs/configuration.md)

## 实施前置

- 阅读 [AGENTS.md](../../../AGENTS.md)、[ADR 0007](../../adr/0007-rust-tauri-rebuild.md)、[ADR 0008](../../adr/0008-acceptance-platform-after-rust-migration.md) 和[现有 Pi 验收规范](../specs/2026-11-09-anvil-rebuild-execution-standard.md)。运行 `git status --short --branch`，保留当前工作树上已有改动。
- 安装资源路径与可写数据根分开。默认 Home 在 Windows 不满足 D 盘校验时，验收用现有显式 `ANVIL_DATA_DIR` 指定合法位置。Windows 部署记录实际 `data_dir` 和安装卷。此次不变更 WebView2 策略及现有平台发布矩阵。
- 建议沿用三个顺序任务 `PIB01`–`PIB03`。在 `PIB02` 完成前，打包中间产物仅用于验证。

## PIB01 · 固定版本并打进安装包

**文件**　新增 `docs/adr/0009-bundle-pi-with-anvil.md`，首个实现提交前检查 ADR 编号是否仍空闲。新增 `src-tauri/runtime-pi/package.json` 与 `package-lock.json` 作为依赖输入，产物放在受忽略的 `src-tauri/bundle-runtime/pi/`。修改 `src-tauri/tauri.conf.json`、`.github/workflows/release.yml` 和 `.gitignore`，在现有 `tools/devtools` 增加生成与检查产物的命令。

- [x] 写 ADR，记录由宿主安装切换为应用内置的决定。采用官方 npm 包和同版私有 Node；Anvil 更新器负责内置 Pi；安装器移除安装资源，数据根继续独立；新版真实验收以本次安装包的实际入口为准，原有验收证据作为历史记录。与 ADR 0007 及旧宿主 Pi 验收约定的冲突以本次决定为准。
- [x] 写失败用例，用错误架构的 Node、缺失的 Pi CLI、缺失的既有 subagent 示例资源分别触发打包准备检查失败。发布产物也要能通过文件清单验证。
- [x] 构建时按 **目标架构** 准备官方 Node，校验发行文件的固定 SHA-256；使用锁文件生成生产依赖，例如 `npm ci --omit=dev --ignore-scripts --no-audit --no-fund`。当前 macOS Intel 交叉构建也按目标架构准备资源。若锁定依赖包含平台专用包，按目标生成并核验产物。保留 Pi `dist`、`pi-ai`、`examples/extensions/subagent` 及其引用文件、分发许可证和运行所需资源。检查 Node、CLI、OAuth 模块入口和插件示例的相对路径，生成 `manifest.json`，记录平台、架构、Node/Pi 版本与路径。安装或首启阶段无需执行 npm。
- [x] 在负责构建安装包的 Tauri 配置中通过 `bundle.resources` 将目录映射到 `runtime/pi/`，在现有 release job 执行准备与校验，继续使用当前签名更新发布链路。若直接在基础配置声明生成目录会让干净工作树的 Rust 测试失败，用构建安装包时加载的 Tauri 配置覆盖声明此映射。开发 `cargo test` 使用注入的测试路径，发布构建单独校验完整的包内资源。打包命令失败时停止该平台的发布，避免上传缺少 Pi 的安装包。[Tauri 资源打包](https://v2.tauri.app/develop/resources/)
- [x] 验证 Win x64 staging，使用其绝对 Node 路径执行包内 CLI `--version`，确认输出与清单一致。保留版本输出、SHA-256、产物清单和命令退出码，提交 `PIB01`。

## PIB02 · 统一运行入口和默认数据路径

**文件**　桌面入口查看 `src-tauri/src/desktop.rs`；运行时状态与安装查看 `src-tauri/src/catalog.rs`、`runtimes.rs`；会话查看 `src-tauri/src/session.rs` 及 `crates/anvil-session/src/adapters/{mod,pi}.rs`；登录查看 `src-tauri/src/pi_auth.rs` 和 `crates/anvil-catalog/src/providers.rs`；数据路径查看 `src-tauri/src/{skills,subagents}.rs`、`crates/anvil-session/src/extensions.rs`、`crates/anvil-catalog/src/runtime/adapters/pi.rs`。用 `src-tauri/src/schedules.rs` 核对定时任务调用链。

- [ ] 先写失败回归测试。分别在有外部 `pi`/`node`、继承了外部 `PI_CODING_AGENT_DIR`、安装资源缺文件的情况下构造实际桌面调用所用的命令。前两者必须选包内路径和注入的数据根，第三种必须报包内资源错误。旧 catalog 缓存里的外部 Pi 条目不得提供可用执行入口。
- [ ] 在桌面初始化时用 Tauri `resource_dir()` 定位并校验清单，构造一份共享配置。建议接口为 `BundledPi::load(resource_dir: &Path, data_dir: &Path) -> Result<BundledPi, String>` 与 `BundledPi::command(&self) -> std::process::Command`。类型放在已有共享依赖可引用的位置，或者注入等价路径参数。配置至少包含 `node`、`cli`、`pi_ai`、`subagent_source`、`agent_dir`、`skill_root`。调用顺序固定为 `Command::new(node).arg(cli)` 加现有参数，`cwd` 继续使用本次会话工作区。用子进程环境明确设置 `PI_CODING_AGENT_DIR`，按需重定向或清除外部 `PI_PACKAGE_DIR`、`NODE_PATH`、`NODE_OPTIONS` 等会改变包解析的值；保留现有工具和模型需要的环境。使用 `OsString` 路径和独立参数，覆盖含空格和 Unicode 的路径。
- [ ] 将包内启动配置注入 catalog 的 Pi 探测、模型/MCP 同步、`PiAdapter`、OAuth helper 以及定时会话链路。正式桌面入口由该配置提供唯一可用 Pi，已有测试或开发注入继续使用显式依赖。探测应保留「未登录」与「启动失败」的区别。把旧外部安装记录按既有失效流程处理，保留 `runtime_id = "pi"`、Agent 引用和原生会话 ID。禁止旧安装按钮、自动 launcher 修复和 runtime 更新绕回 `npm install -g`。
- [ ] 现有子代理状态和安装/更新行为使用包内 `examples/extensions/subagent` 资源，沿用现有扩展及 Agent 定义格式。Pi 1.6.0 自带示例的 `getPiInvocation` 优先用 `process.execPath` 和存在的 `process.argv[1]` 重启 Pi，因此直接用绝对 Node 路径运行绝对 CLI 路径时，原实现可继续复用。测试时让 PATH 中的外部 Pi 一旦被执行就留下标记，并检查实际子进程。如果用户原有的 Anvil 管理版扩展仍使用裸 `pi`，沿用已有更新流程以包内版本更新该副本，保留自定义 Agent 定义和配置。
- [ ] 路径示例为 `agent_dir = data_dir`、`skill_root = data_dir.join("skill")`。在现有模型、认证、扩展和 Skill 管理链中使用它们。给 Pi 添加该 Skill 根，保留用户已有的资源设置、项目资源处理和每会话 `--skill` 挂载。应用增删及 catalog 扫描同一根目录。启动重复执行时，合并资源配置应保持幂等。检查已保存的 Agent-Skill 引用和 Pi 原生会话恢复；如目录迁移会导致 Skill 标识变化，只在导入成功后重新关联对应记录。
- [ ] 兼容旧版用户。复用现有导入或启动迁移机制，对用户确认导入的旧配置和 Skill 从原位置复制到新根，跳过已有目标，记录冲突，支持重试。旧位置默认是 `~/.pi/agent` 和 `~/.pi/skills`，旧版实际使用过显式 agentDir 时，以用户确认的来源为准。既有聊天索引、会话文件及其恢复路径继续使用 Anvil 的现有目录。旧 OAuth/API Key 默认由用户在现有登录界面重新配置，凭证复制仅在用户明确选择且 ADR 覆盖时启用；测试和证据均不记录凭证值。在完全空白的安装上直接初始化新根。
- [ ] 运行 `cargo test --locked -p anvil-catalog -p anvil-session -p anvil`，记录失败用例及修复后结果。至少验证包内 `--version`、探测和 RPC 都走同一入口，外部 Pi 标记未生成，并验证旧会话与 Skill 导入。提交 `PIB02`。

## PIB03 · 接通界面状态并验收安装、升级、卸载

**文件**　`frontend/src/onboarding/OnboardingDialog.tsx`、`frontend/src/catalog/{CatalogPanels.tsx,client.ts}`、现有对应前端测试及 `src-tauri/src/{catalog,updater}.rs`。检查 `frontend/src/pages/settings/SettingsPage.tsx` 是否还有静态运行时状态。发布验收使用 `docs/evidence/` 下本任务的独立收据。

- [ ] 先写前端回归用例。正常包显示 Pi 已内置和当前版本，首次启动进入原有模型配置，Pi 页面没有触发 npm 安装的动作。损坏包显示可辨识的错误。更新按钮调用现有 Anvil 更新接口。维护现有中英文文案。
- [ ] 最小改动接通上述状态。保留现有 Pi 头像、模型配置、子代理界面及既有插件动作，数据来源换成包内探测与包内资源。旧 `runtime_install` 如果为兼容仍保留，Pi 分支返回已内置状态或安装损坏错误，避免旧页面触发外部安装。
- [ ] 运行 `cargo test --locked --workspace`、`npm --prefix frontend test`、`npm --prefix frontend run build`。Windows 按 `AGENTS.md` 先加载 `scripts/env-rs.ps1`，发布构建沿用 Tauri job。验收使用实际打包的 NSIS 和 MSI；Win x64 优先，其他现有发布目标按实际构建和真机结果记状态。
- [ ] 执行下列独立安装用例并收集证据，最后提交 `PIB03`。安装器验证需检查实际落盘资源和进程，可用录屏或截图证明界面状态。版本文字本身不足以证明进程来源。

| 用例 | 实际操作与通过条件 |
| --- | --- |
| PIB03-A 干净安装 | 在 Windows 10 22H2 x64、有 WebView2 的干净环境中断开网络。安装后打开应用，探测 Pi 版本成功。离线阶段无额外 Pi/Node/npm 安装，也无新的全局命令。联机后通过现有流程登录并真实对话一轮。 |
| PIB03-B 外部共存 | 设置 PATH 优先的外部 Pi 和 Node，再设外部 `PI_CODING_AGENT_DIR`。检查探测、登录、聊天、定时会话及一次现有子代理实际的进程路径与配置路径。全部使用包内入口和指定数据根，外部目录保持原状。 |
| PIB03-C 正常升级 | 从旧 Anvil 升至内置版，确认原会话可以恢复、原 Agent 可用、Skill 可导入且不覆盖同名项。再走一次已签名的整包更新，确认 Pi 版本跟随包内清单，用户数据可读。签名或更新环境缺失时据实记录。 |
| PIB03-D 卸载 | 分别通过 NSIS 和 MSI 的标准卸载入口卸载相应安装。确认安装前记录的包内 Pi CLI、Node 和插件资源路径已移除。确认外部 Pi 仍可启动、Anvil 数据根的会话和 Skill 仍存在。 |
| PIB03-E 损坏包 | 在隔离安装副本移除包内 CLI，机器同时装有外部 Pi。打开 Anvil，必须出现包内运行错误或重装提示，外部 Pi 未启动。 |

## 交付要求

提交实现、测试、安装产物构建入口和 ADR。验收记录包括 Task ID、执行模型和运行 ID（若可取得）、提交 SHA、系统架构、命令与退出码、安装包与其 SHA-256、包内 Node/Pi 的实际路径及版本、数据根和验收截图。对未执行的平台或用例明确记录 `NOT_RUN`，遇到签名、凭证、环境等阻塞时记录 `BLOCKED` 及原因。发布结论以真实安装、使用与卸载证据为准。
