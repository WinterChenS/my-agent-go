# B2 报告：设置/观测/编辑页报告缺失的后端绑定

- **Worktree**: `D:\project\anvil-wt\b2` · 分支 `feat/b2`（未触碰 `D:\project\my-agent-go`）
- **任务书**: `C:\Users\Administrator\AppData\Local\Temp\anvil-sdd\B2-brief.md`；缺口来源 U06 §4、U03 §6
- **状态**: **PASS**（Go 绑定 + 领域支持 + 自动化验证全部通过；前端接线与真机/外部证据不在本任务范围，未声称）

## 1. 交付的绑定

| # | 绑定 | 落点 | 关键约定 |
|---|---|---|---|
| 1 | `Desktop.ListRunUsage(RunUsageListQuery) RunUsageListResult` | `internal/app/diagnostics_api.go` + `internal/diagnostics/usage.go` | 复用 `run_usage`；`usageWhere` 让 count 与页共用同一份条件；排序固定 `(updated_at DESC, run_id DESC)`；`availability=reported`（含 0 条，`items:[]`）与 `unavailable`（+`CallError`+`note`）分开；`limit` 默认 50、上限 200 且回显实际值，负值 `INVALID_INPUT` |
| 2 | `Desktop.CredentialStoreStatus() CredentialStoreStatusResult` | `internal/app/desktop.go` + `internal/credentials/{store,native_*}.go` | 只有 `available/backend/detail/error`；后端名 `windows-credential-manager` / `macos-keychain` / `linux-secret-service` / `unavailable`；不可用一律 `CREDENTIAL_STORE_UNAVAILABLE`；Windows 用内存里一次 DPAPI 往返自检（**不往用户凭证库写探测项**）；无任何值字段 |
| 3 | `Desktop.PiInstallManifest() PiInstallManifestResult` | `internal/app/pi_install_api.go` + `internal/piinstall/manifest.go` | 读**安装服务实际使用的那份清单**（含注入）；`Manifest.InstallableEntries()` 只列 loader 接受的条目，`usable` 判据来自 `Entry()`；五个 `sizeBytes=0` 的目标显式 `usable=false` + 原因，原样报 0 |
| 4 | `Desktop.SaveSettingsWithRevision(expectedRevision, s) SettingsResult` | `internal/app/desktop.go` | 过期凭据 → `CONFIG_CONFLICT`（带 `expected`/`actual` params），不建目录、不写文件、不改内存；空工作目录 → `INVALID_INPUT`；`SaveSettings` 签名与语义不变仍可用 |
| 5 | `Desktop.HostTarget() string` | `internal/app/pi_install_api.go` | 直接返回 `pi.HostTarget()`，映射只此一处（测试同时对齐 `build/platforms.json`） |
| 附 | `Desktop.SettingsWithRevision() SettingsResult` | `internal/app/desktop.go` | **额外加的读侧**：没有它 CAS 写无法使用（新开 app 时文件已存在，调用方拿不到凭据就只能永远冲突）。不改变任何既有 DTO |

## 2. 提交

| 提交 | 内容 |
|---|---|
| `664c641` | `ListRunUsage` 领域 + 绑定 + 测试 |
| `55c34da` | `InstallableEntries` + `PiInstallManifest` + `HostTarget` + 测试 |
| `0a2a6f4` | 设置 CAS（`SettingsWithRevision`/`SaveSettingsWithRevision`）+ `CredentialStoreStatus` + 测试 + 红证据/日志 |

每个提交前 `git status --short` 为空（证据文件随最后一个提交入库）。

## 3. 验证（实际命令与退出码）

| 命令 | 退出码 | 结果 |
|---|---|---|
| `go test ./internal/app ./internal/diagnostics ./internal/piinstall ./internal/credentials -count=1` | 0 | `ok internal/app 24.2s` / `diagnostics 1.0s` / `piinstall 42.4s` / `credentials 0.1s`（`scratch/b2/go-test.log`） |
| `go test ./internal/... -count=1` | 0 | 37 个包 `ok`，无 FAIL（同上日志） |
| `go vet ./...` | 0 | 无输出 |
| `gofmt -l internal/` | 0 | 无输出 |
| `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static` | 0 | gofmt / `go vet ./...` / 前端 typecheck 全 PASS（`scratch/b2/static.log`） |
| 新增用例逐条 `-v` 复跑 | 0 | app 10 + diagnostics 3 + piinstall 2 + credentials 4 = **19 PASS** |
| 交叉编译探测 `GOOS=freebsd/darwin/linux GOARCH=... go build ./internal/credentials ./internal/app ...` | 0 | 新增的 per-platform 钩子不破其它目标 |

### TDD 红证据

`scratch/red/b2-bindings-red.txt`（四个包都是编译红，`exit=1`）：

```text
internal\diagnostics\usage_list_test.go:50:24: env.svc.ListRunUsage undefined (type *Service has no field or method ListRunUsage)
internal\piinstall\installable_entries_test.go:22:15: m.InstallableEntries undefined (type Manifest has no field or method InstallableEntries)
internal\credentials\status_test.go:25:8: undefined: StatusFromOpen
internal\app\credential_store_status_test.go:25:4: d.credStoreOpen undefined (type *Desktop has no field or method credStoreOpen)
```

## 4. 关键设计：设置 CAS 的凭据从内容派生

写 `settings.json` 的入口不止一个：`SaveSettings`、`SaveSettingsWithRevision`、`persistPiSelection`（选 Pi 只改一个字段）、用户手工编辑、品牌迁移复制。

- 若把"递增计数"存进文件，上述任何一个入口忘了加一，另一个拿着旧凭据的写者就会**静默覆盖**它——CAS 变成装饰。
- 因此凭据 = `fnv64a(settings.json 字节)`（无文件 = 0，非空文件永不为 0）。任何写入都会自动失效旧凭据，不需要写者配合。
- 代价（已写入 DTO 注释）：不是递增序号，只支持相等比较；冲突后调用方必须用结果里带回的 `Revision` 重试，不能 `+1`。
- 文件形状不变：测试用 **`Settings` DTO 自己的 JSON 键集合**校验落盘文件没有多出任何键（`TestSaveSettingsWithRevisionRejectsStaleRevision`），并做一次 unmarshal 回环比对。
- 顺带把设置写入改成 `tmp → fsync → rename`（原来 `os.WriteFile` 崩溃会留下半个 JSON）。

## 5. 纪律核对

- 未改动：`internal/devtools/**`、`tests/acceptance/**`、`docs/**`、`schemas/**`、`frontend/**`、`internal/run/**`、`internal/session/**`；`git diff --stat` 只在 Touch 列表内（+ `internal/piinstall/manifest.go`、`internal/credentials/native_*.go`，任务书允许 Additive accessor/status）。
- 无新增 `t.Skip`；断言未放宽（`HostTarget` 测试对齐 `build/platforms.json`，`InstallableEntries` 对齐 `Entry()`，credential 测试覆盖可用/不可用两个方向）。
- 既有行为：`SaveSettings` 签名不变；`loadSettings` 仍以默认值为基线 unmarshal（缺失字段不回退成零值）；`persistPiSelection` 的失败路径仍报错且不写内存；`TestSelectPi*`、`TestModel*`、`brandmigrate` 全部仍绿。

## 6. Concerns / 后续

1. **前端未接线（范围外）**：`frontend/**` 属禁改目录。U06 的 `features/settings/api.ts` 仍调 `SaveSettings`，`SecuritySettings` 仍显示 `storeUnavailable`，观测页仍显示 `listUnavailableReason`。需要后续一个前端任务消费 `ListRunUsage`/`CredentialStoreStatus`/`PiInstallManifest`/`HostTarget`/`SettingsWithRevision`，并可能触发 `scripts/ui-copy-baseline.json` 与 catalog 更新。
2. **`persistPiSelection` 仍未持 `settingsMu`**：它在 `internal/app/pi_lifecycle_api.go`（不在本任务 Touch 列表），本轮未改。CAS 层面安全（它的写入会让旧凭据失效，后续 CAS 写必冲突），但"`SaveSettings`（无 CAS）与选 Pi 并发"仍可能丢一次更新——这是既有行为，登记而不是静默修。建议后续把它改走 `saveSettings`。
3. **`CredentialStoreStatus` 的副作用**：Windows 上 `NewNativeStore` 会 `MkdirAll(<appRoot>/credentials)`；探测只做 DPAPI 内存往返，不写探测凭证。macOS/Linux 探测会调用 `security list-keychains` / `secret-tool lookup`（只读），无 secret-tool 时如实报不可用（测试不 skip，两个方向都断言）。
4. **`PiInstallManifest` 依赖安装服务可装配**（需要 `pi.ManagedRoot()`）。装不出来时 `error` 有值、`entries:[]`——设置页必须显示"读不出清单"，不能当成"没有可装版本"。
5. **`ListRunUsage` 的分页是 offset 语义**：range 内新增更新记录会让后续窗口整体位移（首屏稳定，深翻可能重复/漏行）。当前 UI 是"最近 N 条"，可接受；若要严格快照翻页，需要 cursor（keyset）分页。
6. **本机环境事项（非本次改动）**：worktree 原本缺 `.tools/pi/standalone`、`frontend/{node_modules,dist}`，导致 piinstall 集成用例与 `go vet ./...`（embed `all:dist`）直接失败。为跑通验证，从主仓库**只读复制**了这三份 gitignore 产物（未入库）。真机 win-x64 验收仍按 AGENTS.md/ADR 0005 在 Windows 11 24H2 上重跑，不在本任务声称范围。
7. **U03 报告里 `profile.Profile` 缺 `Description/SystemPrompt/ReasoningLevel/ToolIDs`** 与本任务的 5 个绑定无关（需改 `internal/profile/**` + `internal/config/compile.go`），未触碰。
