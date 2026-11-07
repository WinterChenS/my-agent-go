# U06 报告：设置页与观测页（UI10 / UI11）

- **Worktree**: `D:\project\anvil-wt\u06` · 分支 `feat/u06`（未触碰 `D:\project\my-agent-go`）
- **任务书**: `U06-brief.md` → `docs/superpowers/plans/2026-11-05-anvil-ui.md` U06
- **状态**: **PASS**（前端范围）。UI10/UI11 的门禁判定需要构建后的 Wails 应用与外部判据，按计划属 U07/G3，本任务不声称通过。
- **范围**: 只写前端。Go 一行未改（`git diff --stat internal cmd schema tests` 为空）。

## 1. 交付内容

新增（`frontend/src/features/settings/`）：

| 文件 | 作用 |
|---|---|
| `api.ts` | 绑定：`GetPreferences`/`SavePreferences`、`ListModels`/`CurrentModel`/`TestModel`/`SetActiveModel`、`AgentProfileCeiling`、`DiscoverPi`/`SelectPi`/`InstallPi`/`CancelPiInstall`/`RollbackPi`、`myagent:pi-install` 进度订阅 |
| `Section.tsx` | 分区外壳（`data-testid=settings-<id>` + `data-state`）与共用控件样式（只用语义 token） |
| `GeneralSettings.tsx` | 语言/主题：CAS 保存（带回 `revision`）、保存后重新读取、冲突 `CONFIG_CONFLICT`、失败不显示已保存 |
| `ModelSettings.tsx` | 模型列表/连接测试（真实延迟或 code）/设为当前（`deferred` 说明下次运行生效） |
| `PermissionSettings.tsx` | `AgentProfileCeiling` 的真实上限 + 逐字段来源；网络恒不可用（不渲染开关）；shell 默认值按后端快照判定是否"已保存"；工作区信任 |
| `PiSettings.tsx` | 全部候选的真实启动器/运行时路径、版本、来源、发行形式、启动器与运行时 hash、探测状态与诊断；`选用` 仅对 `ready_verified`/`ready_compatible` 可用；`InstallPi(version)` + 进度阶段 + 权威结果；`回滚` 仅在后端 `rollbackAvailable` 时可用 |
| `UpdateSettings.tsx` | **F09 不存在**：`data-state="unavailable"` + 原因，零按钮、无进度条 |
| `SecuritySettings.tsx` | 每个模型的凭证引用存在性、凭证库状态 unavailable（缺绑定）、旧字段明文兜底风险；不渲染任何密钥输入框 |
| `SettingsPage.tsx` | 七分区装配 + 诊断导出入口 |

新增（`frontend/src/features/observability/`）：

- `api.ts`：`UsageSummary` / `RunDetails` / `ExportDiagnostics` 与 C07 DTO（指针字段 = 不知道）。
- `ObservabilityPage.tsx`：用量汇总（缺 usage/价格显示 unavailable，从不显示 0）、run 链路（tools/approvals/errors/compactions/diagnostics 按 `seq` 排序，50 条上界 + 展开）、`unavailable` 段落、来源会话深链、诊断导出。
- `DiagnosticsExport.tsx`：导出面板（路径/大小/sha256/脱敏计数/schema 版本），设置页复用同一份实现。

修改（路由与文案）：

- `frontend/src/app/pages/index.tsx`：`SettingsPage`/`ObservabilityPage` 改为从 feature 转出，删除旧的本地 `SettingsPage` 装配。
- `frontend/src/app/navigation.ts`：新增 `/observability` 路由（`nav.observability`），工具分组补一项。
- `frontend/src/app/Router.test.ts`：把 `/observability` 加入必需路由与工具分组断言（**加强**，不是放宽）。
- `frontend/src/i18n/locales/{zh-CN,en-US}.json`：`settings.*`、`observability.*`、`nav.observability`、`nav.hints.observability`（两种语言 key 集合完全一致）。

## 2. 验证（本机实际命令与退出码）

| 命令 | 结果 |
|---|---|
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend test -- SettingsPage ObservabilityPage` | **PASS**，2 文件 / **33 tests** |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend test` | **PASS**，36 文件 / **309 tests** |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend run typecheck` | **PASS**（`tsc --noEmit` 无输出） |
| `"D:\Program Files\nodejs\node.exe" scripts/check-ui-contract.mjs` | **PASS**，退出码 0（catalog 外文案增量为 0；40 个 Go 错误码双语覆盖） |
| `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static` | **PASS**，退出码 0（gofmt / `go vet ./...` / 前端 typecheck） |

说明（环境事实，非本次改动）：worktree 里最初没有 `frontend/node_modules`，`npm ci`（仍用基线 `D:\Program Files\nodejs\npm.cmd`）后补齐；`go vet` 的 `embed all:dist` 需要 `frontend/dist`，先跑了一次 `npm --prefix frontend run build` 生成产物（`frontend/dist/` 已被 `.gitignore` 忽略，未入库）。另：PATH 首个 `node` 是 v16，vitest 启动需要 Node ≥ 18，实际执行时把 `D:\Program Files\nodejs` 前置到 PATH，脚本自身仍由基线 npm 驱动。

### TDD 红证据

先写测试再实现。首次运行（组件尚不存在）为**红**：

```text
FAIL src/features/settings/SettingsPage.test.tsx
Error: Failed to resolve import "./SettingsPage" from "src/features/settings/SettingsPage.test.tsx"
Test Files 1 failed (1) / Tests no tests
```

实现后同两条命令全绿（33/33）。用例覆盖：偏好持久化 + 保存后重载、`CONFIG_CONFLICT`、写入失败不显示已保存、偏好读取失败与重试、模型列表/测试/切换、权限上限与来源、shell 默认值成功/失败判定、工作区信任、Pi 六种状态渲染（含 `missing` 失效路径）、选用、安装权威结果与进度分离、安装失败阶段、回滚门禁、探测失败、更新分区 unavailable、安全凭证覆盖、观测 usage/run/导出/分页/深链。

## 3. 纪律核对

- **更新分区**：`data-state="unavailable"`；测试断言该分区内 **0 个按钮**、无 `role="progressbar"`、无 `%`；文案明确"F09 缺失、没有 UpdateStatus/CheckForUpdate/DownloadUpdate/UpdateBlockers/ExitAndUpdate 绑定"以及"不显示进度条、不提供退出并更新"。
- **Pi**：候选逐条显示启动器路径、真实运行时（npm shim 时的 Node 路径）、版本、来源、发行形式、两份 hash、探测状态与诊断原文；失效路径照样出现（`missing`）。低于最低支持的候选 `选用` 禁用；回滚按钮默认禁用，只有 `rollbackAvailable && installationId` 才可用，并把 `installationId` 原样传回。未渲染任何未支持的运行时（无 Codex/Claude/运行时市场/网络开关）。
- **CAS / 保存语义**：偏好保存带回 `revision`，冲突显示 `CONFIG_CONFLICT` 并给"重新读取"，草稿保留；写入失败只显示错误、不显示"已保存"。`SaveSettings` 没有 revision（见 §4 第 4 条），所以 shell 默认值/信任的"已保存"由**后端快照与草稿一致**判定，失败时快照不变 → 不显示已保存。
- **文案/状态**：全部用户可见文案来自 catalog；分区状态走 `AsyncState`；缺口显示 unavailable + 原因（不是 0、不是空数组）。
- 无 `it.skip`，无弱化断言（`Router.test.ts` 是新增期望项）。

## 4. 缺口登记（需要 Go 侧补的绑定，本任务不改 Go）

1. **列 run / 最近错误（UI11 的"recent errors"目前只能是 unavailable）**
   C07 只有 `UsageSummary` 与 `RunDetails(runID)`，没有枚举 run 的接口。建议：
   ```go
   type RunUsageListQuery struct {
       From        string   `json:"from"`        // RFC3339，空=不限
       To          string   `json:"to"`
       WorkspaceID string   `json:"workspaceId"`
       SessionID   string   `json:"sessionId"`
       Limit       int      `json:"limit"`
       Offset      int      `json:"offset"`
   }
   type RunUsageListResult struct {
       Items []diagnostics.RunRecord `json:"items"` // 复用 RunRecord（含 usage/budget/error）
       Total int                     `json:"total"`
       Error *app.CallError          `json:"error,omitempty"`
   }
   Desktop.ListRunUsage(q RunUsageListQuery) RunUsageListResult
   ```
   界面已留位置：`observability.run.listUnavailableReason`（当前显示"没有列出 run 的接口"）。
2. **凭证库可用性（设置 → 安全）**：缺 `Desktop.CredentialStoreStatus() CredentialStoreStatusResult`：
   ```go
   type CredentialStoreStatusResult struct {
       Available bool         `json:"available"`
       Backend   string       `json:"backend"`  // 例如 windows-credential-manager / secret-service / unavailable
       Error     *app.CallError `json:"error,omitempty"`
   }
   ```
   现在该分区显示 `settings.security.storeUnavailable`（unavailable），不假装正常。
3. **Pi 受信清单（安装版本选择）**：`InstallPi(version)` 存在但没有"清单里有哪些版本/目标/大小"的绑定，界面只能让用户输入版本号（错误由后端 `UNSUPPORTED`/`UNKNOWN_TARGET` 返回）。建议：
   ```go
   type PiManifestEntry struct {
       Version   string `json:"version"`
       TargetID  string `json:"targetId"`
       AssetName string `json:"assetName"`
       SizeBytes int64  `json:"sizeBytes"`
       SHA256    string `json:"sha256"`
       Signature string `json:"signature"`
   }
   type PiManifestResult struct {
       Schema      int               `json:"schema"`
       GeneratedAt string            `json:"generatedAt"`
       HostTarget  string            `json:"hostTarget"`
       Entries     []PiManifestEntry `json:"entries"`
       Error       *app.CallError    `json:"error,omitempty"`
   }
   Desktop.PiInstallManifest() PiManifestResult
   ```
   有它之后可只列可安装版本（并按 `sizeBytes>0` 排除 loader 会拒绝的目标）。
4. **`SaveSettings` 无 CAS**：`Settings` 没有 `revision`，`SaveSettings(s Settings) error` 无法返回 `CONFIG_CONFLICT`。建议改成 `SaveSettings(expectedRevision uint64, s Settings) SettingsResult{ Settings; Revision; Error *CallError }`（或新增版本化包装）。在此之前，界面按"重新读取后的快照是否等于草稿"判定已保存，失败只显示 code。
5. **命名对不上**：计划写的 `RunChain` 在 Go 侧是 `RunDetails(runID)`；UI11 的 run 链路由 `RunDetails.tools/approvals/errors/compactions/diagnostics` 按 `seq` 合成。
6. **导出 `target` 字段无来源**：`DiagnosticsExportRequest.target` 需要目标 ID（如 `win-x64`），前端没有对应绑定（`Status.platform` 是 `windows/amd64` 这一不同口径），当前传空串。建议加 `Desktop.HostTarget() string` 或让 Go 侧默认填 `pi.HostTarget()`。

## 5. 已知遗留与风险

- **`frontend/src/features/settings/SettingsView.tsx`（及 `SettingsView.test.tsx`）保留但已不再被路由使用**：`scripts/ui-copy-baseline.json` 给它登记了 58 条历史字面量文案，而该文件不在本任务 Touch 列表内；删除/迁移它必须同步下调 baseline，否则 `check-ui-contract.mjs` 会判 `baseline-stale`。建议在 U07（或单独一个清理提交）里连同 baseline 一起移除。
- **Pi 安装的取消按钮**只在拿到进度事件里的 `installId` 后出现（`InstallPi` 同步返回，安装中才可能知道 ID）；未拿到 ID 时不显示取消，而不是显示一个点不动的按钮。
- **`PiInstallResult.error` 是 Go 的技术字符串**（不是 `CallError`），只在 `data-testid=pi-install-detail` 的诊断块里原样显示，用户可见文案仍来自 catalog。
- **安全分区只做只读呈现**：不提供修改/重写密钥的入口（避免界面把明文写进 settings.json 的旧路径变成"推荐路径"）。
- 观测页的 `recent errors` 目前只能显示"已打开的那一条 run"的错误；跨 run 的错误列表依赖 §4 第 1 条。
