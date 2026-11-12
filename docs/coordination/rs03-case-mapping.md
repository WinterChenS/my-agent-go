# R0-RS03 · Go → Rust 用例映射

迁移基准 `e376787`（R0-RS02 之后）。Go 已于本次迁移中彻底废弃：原来的 `go` 步骤全部换成
**Rust 工具链自己的命令**，`tools/devtools` 是该栈里唯一的验收入口。Go 源码（`cmd/`、`internal/`、
`go.mod`、`go.sum`，共 45 个 `.go` 文件）已在 R0-RS04 从工作树删除，需要对照时从 git 历史取。
本文件记录"原来跑什么、现在跑什么"。

> 术语：`cargo` 不是另一种技术栈，它就是 Rust 自带的构建与依赖管理工具，等价于
> `go build` + `go mod` + `go test` 的组合。所以"`go build` → `cargo build`"是同一个位置
> 换成 Rust 工具链，不是引入第三方。

## CLI 命令

| Go (`cmd/devtools`) | Rust (`anvil-devtools`) | 状态 |
| --- | --- | --- |
| `verify --case= --target= [--evidence] [--matrix] [--sha]` | 同名同参数 | 已迁移 |
| `gate --id= --target= --evidence= [--matrix] [--sha]` | 同名同参数 | 已迁移 |
| `preflight --path= [--create]` | 同名同参数 | 已迁移 |
| `key --title= --combo=alt+left\|escape` | 同名同参数（SendInput） | 已迁移 |
| `version` | 同名 | 已迁移 |

退出码契约不变：0 通过、1 有效 FAIL 证据、2 输入/证据不完整或 BLOCKED/NOT_RUN。
`scripts/devtools.ps1` 原样透传子进程退出码（实测 0/1/2 各一例）。

## 证据格式

`result.json` 的字段、`schemaVersion`、`commands`/`artifacts` 必填校验、产物相对路径
与 SHA-256 校验、越界与跨卷拒绝全部保持原语义；新增两处更严格：

- 产物除"文本路径在 case 目录内"外，还要求解析后的真实路径仍在**证据根**内。
- `gate` 与 `verify` 共用同一套 `anvil-paths` 解析（Go 时代是 `internal/preflight`）。

`runtimeVersion` 从 `wails <version>` 改为 `tauri 2 (no agent runtime in this case)`；
`toolVersions` 用 `cargo`/`rustc`/`git`/`node`/`npm` 取代 `go`/`gcc`。

## 步骤映射（自动化用例）

| 原 Go 步骤 | 现 Rust 步骤 | 说明 |
| --- | --- | --- |
| `go version`（期望 `go1.27.3`） | `rustc --version`（期望 `rustc 1.95.0`）、`cargo --version` | 工具链锁在 `rust-toolchain.toml` |
| `go build ./...` | `cargo build --locked --workspace` | |
| `go vet ./...` | `cargo clippy --workspace --all-targets --locked -- -D warnings` | clippy `-D warnings` 取代 vet |
| `go test -count=1 ./...` | `cargo test --locked --workspace` | |
| `go test ./internal/preflight/ -v` | `cargo test --locked -p anvil-paths` | 契约测试已扩到 23 例 |
| `self preflight --path=…/.tmp/tooling/go-build` | `…/cargo-target` | 缓存目录改名 |
| `self preflight --path=…/.tmp/tooling/go-mod` / `gopath` | 删除 | Go module cache 不再存在 |
| `node -v` / `npm -v` / `npm ci` / `npm test` / `npm run build` | 不变 | |
| `node scripts/check-dist.mjs` | 不变 | |
| `node scripts/capture-prototype.mjs` | 不变 | |
| `cmd /c mklink /J` + 拒绝 + `rmdir` | 不变 | 跨卷 junction 拒绝用例保留 |

## 用例 ID（27 个）

R0-00..R0-08 的 19 个用例保持 Go 时代的 ID 与语义不变（未改名、未减项）。
R0-09..R0-12 是 RS05 新增的四页，各配一条自动化用例与一条原生用例。

自动化（14）：`R0-00-build-gate`、`R0-00-d-drive-preflight`、`R0-01-shell`、
`R0-02-theme`、`R0-03-i18n`、`R0-04-workbench`、`R0-05-chat`、`R0-06-agents`、
`R0-07-skills`、`R0-08-mcp`、`R0-09-connectors`、`R0-10-schedules`、
`R0-11-library`、`R0-12-settings`。

原生（13）：`R0-00-native-window`、`R0-01-native-shell`、`R0-02-native-theme`、
`R0-03-native-i18n`、`R0-04-native-workbench`、`R0-05-native-chat`、
`R0-06-native-agents`、`R0-07-native-skills`、`R0-08-native-mcp`、
`R0-09-native-connectors`、`R0-10-native-schedules`、`R0-11-native-library`、
`R0-12-native-settings`。

## 原生运行器映射（phase 2 完成）

九个原生用例的 agent-desktop 运行器已全部移植。共享的"启动 → 观察 → 断言 → 关闭"链路
集中在 `native/session.rs`，解析集中在 `native/parse.rs`，每个用例只保留自己的断言：

| Go 源文件 | Rust 模块 | 用例 |
| --- | --- | --- |
| `runner_native_desktop.go` | `native/case_window.rs` + `native/session.rs` | `R0-00-native-window` |
| `runner_shell_native.go` + `runner_shell_parse.go` | `native/case_shell.rs` | `R0-01-native-shell` |
| `runner_theme_native.go` + `runner_theme_parse.go` + `runner_theme_robust.go` | `native/case_theme.rs` | `R0-02-native-theme` |
| `runner_i18n_native.go` + `runner_i18n_parse.go` | `native/case_i18n.rs` | `R0-03-native-i18n` |
| `runner_workbench_native.go` + `runner_workbench_focus.go` | `native/case_workbench.rs` | `R0-04-native-workbench` |
| `runner_chat_native.go` | `native/case_chat.rs` + `native/pages.rs` | `R0-05-native-chat` |
| `runner_agents_native.go` | `native/case_agents.rs` + `native/pages.rs` | `R0-06-native-agents` |
| `runner_skills_native.go` | `native/case_skills.rs` + `native/pages.rs` | `R0-07-native-skills` |
| `runner_mcp_native.go` | `native/case_mcp.rs` + `native/pages.rs` | `R0-08-native-mcp` |
| `cmd/devtools/key_windows.go` | `src/key.rs` | `key --title= --combo=` |

R0-09..R0-12 没有 Go 前身（Go 时代这四页是路由占位），新建的四个运行器直接复用了
`native/pages.rs` 的"普通入口 → 样例入口 → 紧凑视口"链路：

| Rust 模块 | 用例 | 该页独有的断言 |
| --- | --- | --- |
| `native/case_connectors.rs` | `R0-09-native-connectors` | 六张连接器卡、策略表、凭据健康、添加连接器对话框 |
| `native/case_schedules.rs` | `R0-10-native-schedules` | 任务表、行选中切换详情、新建任务对话框与焦点回归 |
| `native/case_library.rs` | `R0-11-native-library` | 网格两列并排、上传对话框与 Escape 回归 |
| `native/case_settings.rs` | `R0-12-native-settings` | 五个分区按 ref 可达、模型/观测/安全面板切换 |

端口与 Go 的差异只有三处，都源于客户端从 Wails 换成 Tauri：

| 项 | Go（Wails） | Rust（Tauri） | 原因 |
| --- | --- | --- | --- |
| 构建 | `go build -o .tmp/bin/anvil.exe ./cmd/anvil` | `cargo build --locked -p anvil` → `.tmp/tooling/cargo-target/debug/anvil.exe` | Go 已废弃 |
| 窗口尺寸 | `list-windows` 的 bounds 直接等于请求值 | `ANVIL_WINDOW_WIDTH/HEIGHT` 定**客户区**，`resize-window` 定**外框**；断言改用前端 `layout-probe` 的视口，并用实测边框换算 | 宿主语义不同 |
| 关闭判定 | `close-app` + 空窗口列表 | 同上，但 `close-app` 之后追加 `--force` 清理进程 | 窗口消失不等于进程退出，残留会让下一次 `launch` 报 "Application is already running" |

失败分类不变：`INTERNAL`/`PERM_DENIED`/`POLICY_DENIED`/`ACCESS_DENIED`/`E_ACCESSDENIED`、
DACL 投诉、CLI 缺失、`icacls` 失败 → BLOCKED(2)；无窗口、标题不符、标记缺失、截图过小、
视口不符 → FAIL(1)。

## 契约夹具（`tools/devtools/tests/fixtures.rs`）

12 例，覆盖计划要求的八类场景 + 4 个边界（第 12 例改为"矩阵里每个 native 用例都必须有
运行器"，因为原生运行器已全部迁移，缺运行器会让门禁静默关闭）：

1. 空矩阵（`gates: []`）
2. 遗漏任务（required task 无 case）
3. 未知目标
4. 无效 SHA（非 40 位）
5. 证据根在 C 卷（注入 resolver，不建 C 盘夹具）
6. case 目录解析到证据根之外
7. 产物哈希不符（status 记 `INVALID`）
8. 有效 FAIL → exit 1
9. 完整 PASS → exit 0
10. `BLOCKED` 缺 `limitations`
11. `result.json` 缺字段（报"missing field"而非默认值）
12. 未注册 case → exit 2；矩阵登记为 native 却无运行器 → 测试直接失败

单测另 33 例覆盖标识符、SHA、通配符、卷名归一化与词法越界，以及原生侧的日志解析
（layout-probe / window-options / theme / i18n / 标题 / reg DWORD）、元素矩形、
`find` 的 `null` 属性、边框换算与按键序列。
