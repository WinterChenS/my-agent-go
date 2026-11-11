# R0-RS03 · Go → Rust 用例映射

迁移基准 `e376787`（R0-RS02 之后）。Go 已于本次迁移中彻底废弃：`cargo` 等价步骤替代了
全部 `go` 步骤，`tools/devtools` 是该栈里唯一的验收入口。Go 源码在 RS04 删除，本文件
记录"原来跑什么、现在跑什么"，以及尚未迁移的部分。

## CLI 命令

| Go (`cmd/devtools`) | Rust (`anvil-devtools`) | 状态 |
| --- | --- | --- |
| `verify --case= --target= [--evidence] [--matrix] [--sha]` | 同名同参数 | 已迁移 |
| `gate --id= --target= --evidence= [--matrix] [--sha]` | 同名同参数 | 已迁移 |
| `preflight --path= [--create]` | 同名同参数 | 已迁移 |
| `key --title= --combo=` | 同名同参数 | **phase 2**（原生 shell 用例需要 alt+left 注入） |
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

## 用例 ID（19 个，未改名、未减项）

自动化（9）：`R0-00-build-gate`、`R0-00-d-drive-preflight`、`R0-01-shell`、
`R0-02-theme`、`R0-03-i18n`、`R0-04-workbench`、`R0-05-chat`、`R0-06-agents`、
`R0-07-skills`、`R0-08-mcp`。

原生（9 + 1）：`R0-00-native-window`、`R0-01-native-shell`、`R0-02-native-theme`、
`R0-03-native-i18n`、`R0-04-native-workbench`、`R0-05-native-chat`、
`R0-06-native-agents`、`R0-07-native-skills`、`R0-08-native-mcp`。

## 尚未迁移（明确记录，不伪装成通过）

| 项 | 现状 | 计划 |
| --- | --- | --- |
| 9 个原生用例的 agent-desktop 运行器 | `verify` 返回 exit 2，问题文本写明"needs the native agent-desktop runner, which is ported in R0-RS03 phase 2"；**不会**生成 PASS 证据 | R0-RS03 phase 2 |
| `key` 子命令（alt+left 注入） | 返回 exit 2 并说明 phase 2 | R0-RS03 phase 2 |
| 原生用例的截图/UIA 断言解析 | 依赖 phase 2 的运行器 | R0-RS03 phase 2 |

## 契约夹具（`tools/devtools/tests/fixtures.rs`）

12 例，覆盖计划要求的八类场景 + 4 个边界：

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
12. 未注册 case / 原生用例未迁移 → exit 2

单测另 11 例覆盖标识符、SHA、通配符、卷名归一化与词法越界。
