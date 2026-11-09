# M1ENV 报告 — 模型配置从环境变量读到（这样一轮对话才真的能开始）

- **Status**: PASS（本机开发证据；没有新增门禁用例，未改变任何验收口径）
- **Worktree**: `D:\project\anvil-wt\m1env`，分支 `feat/m1env`（未触碰 `D:\project\my-agent-go`）
- **Commits**:
  - `1b08574` M1ENV: read model config from env with settings-precedence and report the source
  - `006a113` M1ENV: show where the effective model config comes from on the Settings page
- **工作树**: 提交后干净（`git status --short` 空）

## 1. 动手前查到的真实情况（与任务书的假设有出入，按实际实现）

任务书说 `ANVIL_PROVIDER / ANVIL_MODEL / ANVIL_BASE_URL` "没有任何代码读取"。实测：
`internal/app/desktop.go` 的 `applyEnvOverrides` **会**读这三个（经 `envLookup` 加
`ANVIL_` 前缀），所以它们不是完全的死旋钮。真正坏掉的是另外三件事，症状同样是
"环境变量看起来没用"：

| 缺陷 | 后果 |
|---|---|
| 环境变量**无条件覆盖**设置 | 用户在设置里改的模型永远不生效（和"设置优先"完全相反） |
| 被覆盖后的快照会经保存落盘 | 一次编辑就把预览/CI 的 provider/baseUrl 永久写进用户 `settings.json` |
| `ANVIL_MODEL_API_KEY` 完全没人读 | 唯一能进密钥的是 `ANVIL_API_KEY`（旧明文路径），`MODEL_API_KEY` 是纯假旋钮 |
| 任何地方都看不到来源 | 界面显示的"设置值"和实际跑的"环境值"无法区分 |

另外：`internal/config` 里**没有** `Settings`（那是 `internal/app` 的类型），也没有任何
provider/base URL 字段。模型的 base URL 早就由 `Settings.BaseURL` 承担，因此按任务书的
允许分支**复用 `BaseURL`，不新增 `Settings.ModelBaseURL`**（同一件事两个字段必然分叉）。
新增的只有 `Settings.ModelAPIKeyRef`（引用，不是密钥）。

## 2. 做了什么

1. `internal/app/settings_env.go`（新）
   - `resolveSettings`：**设置 > 环境变量 > 默认值**的唯一解析点。
     "设置"以 `settings.json` 的**键是否出现**为准（不靠零值猜）：缺席才是"没说话"，
     显式空串是一个设置（用户清掉端点后，环境变量不得偷偷顶回来）。
   - 每项返回来源：`settings | env | default | none`，env 时带命中的完整变量名
     （`ANVIL_*`，旧 `MYAGENT_*` 仍可用并记弃用）。
   - 覆盖：`PROVIDER / MODEL / BASE_URL / MODEL_API_KEY / API_KEY / PI_PATH / NODE_PATH /
     WORKSPACE / SHELL`（后四者是同一套优先级，此前 `PI_PATH` 的内存值与解析值口径不一致）。
   - `Settings.ModelAPIKeyRef`：`ANVIL_MODEL_API_KEY` → `env:ANVIL_MODEL_API_KEY`（**引用**）。
     明文由 `modelSecret` 从进程环境现取，只经子进程环境交给 Pi。
   - `stripEnvDerived`：保存前把"仍等于环境值、且磁盘上没写过这个键"的字段清空，
     环境值因此不会经任何保存路径固化；内存按"刚写下的文档 + 环境"重新解析，
     **本次运行行为不变**，只是磁盘上没有它们。
2. `internal/app/desktop.go`
   - `loadSettings` / `SettingsWithRevision` / `saveSettings` 全部改走 `resolveSettings`
     （单一解析点，删掉 `applyEnvOverrides`）。
   - `legacyModelSetup` 的密钥改为 `modelSecret(s)`：`env:` 引用读环境变量，否则退回旧明文。
3. `internal/app/config_api.go`
   - `EffectiveConfigResult.Model`（+ `Desktop.ModelConfig()`）：`provider/model/baseUrl/
     apiKeyRef` 各带 `value/source/envVar` 与 `overriddenByEnv`。取自**当前生效的内存设置**
     （与启动 run 同源），不从磁盘现读，避免"来源"和"实际跑的"两份事实。
4. `frontend/src/features/settings/ModelProvenance.tsx`（新，挂在模型分区顶部）
   - 逐项显示值 + 来源徽标（env 时带 `ANVIL_*` 变量名）+ 被环境补上的字段提示；
     密钥只显示引用；读失败走 `role=alert`。
   - `settingsApi.effectiveConfig()` 绑定 `Desktop.EffectiveConfig`（只取 `model` 段）。
   - 文案全部走 catalog（`settings.model.env.*`，两种语言），未新增字面量。
5. `docs/model-config-env.md`（新）+ `docs/README.md` 入口：变量表、优先级、不落盘、
   密钥引用规则、从哪看来源、PowerShell/bash 示例。

## 3. 测试与证据（本机，实际命令与退出码）

新增用例（`internal/app/settings_env_test.go`，均无 skip、无弱化断言）：

| 用例 | 钉住的事实 |
|---|---|
| `TestModelEnvFeedsStartWhenSettingsAreEmpty` | 设置里没有模型时：`Settings/Status/CurrentModel` 反映 env；`Start` 的 `--provider envprov --model envmodel`；`agentDir/models.json` 的 provider 键、`baseUrl`、模型 id 都来自 env，`apiKey` 是 `$ENV` 引用；密钥出现在 worker 环境里；`SettingsWithRevision().baseUrl` 非占位（前端就绪检查据此不再拦 MODEL_MISSING/MODEL_ENDPOINT）；`EffectiveConfig()` 四项 source=env + `overriddenByEnv` 全列；保存后 `settings.json` 里既无明文密钥也无自动引用、也没有 env 的 provider/model/baseUrl；全目录扫不到明文；保存后 env 值仍生效 |
| `TestModelEnvDoesNotOverrideSettings` | `settings.json` 写了三项时 source=settings、`overriddenByEnv` 为空、`resolveModelSetup` 用设置里的 provider/model/baseUrl |
| `TestModelEnvKeyIsNotLogged` | `log` 输出里没有密钥明文（`log.SetOutput` 抓取），配置目录扫不到明文 |
| `TestModelEnvUnsetKeepsTodaysBehaviour` | 无配置文件 → provider/baseUrl 走默认值（占位端点）、`apiKeyRef` 为空、`overriddenByEnv` 空；显式清空的模型配置 → `CONFIG_MISSING`（与今天一致，不被默认值掩盖） |

前端：`ModelProvenance.test.tsx`（4 例：env 标明变量名、设置来源无 env 警告、失败必须露出、
面板里不出现 `sk-` 形态的串）。

实际命令与退出码：

```
go test ./internal/... -count=1                 → PASS（含真实 Pi 的 integration-only 用例）
go vet ./...                                    → PASS
gofmt -l internal cmd scripts                   → 空输出
scripts/devtools.ps1 verify --suite=static      → 退出码 0（gofmt / go vet ./... / 前端 typecheck）
scripts/devtools.ps1 verify --suite=unit        → 退出码 0（go test ./internal/... + 前端 vitest）
  └ 前端：45 test files / 409 tests 全过
node scripts/check-ui-contract.mjs              → PASS（catalog 之外文案增量为 0，40 个错误码双语文案齐全）
```

关于真实 Pi 用例：本 worktree 起初没有 `.tools/pi`（gitignored 的只读引用包），
`TestDiscoverPiAcceptsRealPiThroughProductionProbe` / `TestContractProbeAgainstRealPi` /
`TestRealPiClarificationEndToEnd` / `TestInstallRealStandalonePackage` /
`TestEmbeddedManifestCarriesRealWinX64Entry` 会以"缺官方独立包"失败（BLOCKED，不是 skip）。
为了拿到完整证据，我把主仓库同版本的 `.tools/pi`（官方 1.5.0 独立包，只读引用）
**复制**到本 worktree（复制而不是 junction：`TestDevMachineSafetySnapshot` 会拒绝符号链接），
之后整包 PASS。这是补齐开发证据用的本地夹具，未进版本库。

## 4. 与任务书 Touch 列表的对照（无越界）

改动/新增：`internal/app/**`（desktop.go、config_api.go、pi_lifecycle_api.go 的一处注释、
settings_env.go、settings_env_test.go）、`docs/**`（model-config-env.md、README.md）、
`frontend/src/features/settings/**`（ModelProvenance 组件与测试、ModelSettings 挂载、
api.ts 绑定）、以及前端共享定义 `frontend/src/types.ts` 与 `frontend/src/i18n/locales/*.json`
（新增字段与文案，任何前端绑定都必须先在这两处声明）。
另有既有两个测试文件只补了新绑定的替身（`features/settings/SettingsPage.test.tsx`、
`app/SettingsRoute.test.tsx`），未放宽任何断言。

未触碰：`internal/run/**`、`internal/devtools/**`、`tests/**`、`schemas/**`、其它 feature
（`features/models/**`、`features/chat/**` 均未改）。`internal/runtime/pi/**` 未需要改动
（`models.json` 的 `$ENV` 引用与子进程环境交付路径本来就够用）。

## 5. Concerns

1. **就绪检查只在后端侧被间接证明。** 前端 `evaluateReadiness` 读 `CurrentModel` 的
   provider/model 与 `settings.baseUrl`，这三者的 env 生效都有断言，但"面板从阻塞变绿"
   这句话没有端到端 UI 断言（`features/chat/**` 不在本任务的 Touch 列表内，且前端就绪
   逻辑没改）。要升级成 PASS 级 UI 证据，建议在 U 系列加一条 e2e：env 起应用 → 对话页
   不再显示 `MODEL_MISSING/MODEL_ENDPOINT`。
2. **"显式空串 = 用户设置"是刻意的取舍。** 因此如果某个入口把 provider/model 显式写成
   空串保存下来，env 就不再补位（这是"设置优先"的直接推论，也是今天 `CONFIG_MISSING`
   仍可复现的原因）。当前存活的设置入口（模型页/开启模型）不会这么写，但新的设置界面
   若把"空串"当成"没设置"来保存，就会堵住 env 这条路。
3. **`ANVIL_API_KEY` 仍然存在。** 它是旧明文路径（本机/fixture 端点），本任务只保证
   它补位出来的值也不落盘、不进日志；真正的新路径是 `ANVIL_MODEL_API_KEY`。
   要彻底废掉明文路径得动"模型定义/凭证库"那条线（C02/SEC07），不在本任务范围。
4. **`Desktop.ModelConfig` 与内存设置同源刷新**（只在 `New` / 保存时更新）。测试若直接改
   `d.settings`（多数绑定层测试都这么做），`EffectiveConfig()` 仍报上次解析的来源。
   当前没有用例依赖这两者一致；若要"任何时候都能回答来源"，需要把写入内存的入口收敛成
   一个 setter。
5. **本机证据的边界**：Win10 22H2 上的开发证据，不能当作 win-x64 验收结论（AGENTS §4）。
   本次没有新增门禁用例，也未改 `docs/acceptance/v1-gates.md` 的口径。
6. **没做的一件事（有意）**：没有给 `Status` 增加"模型来源"字段，也没改
   `CurrentModelResult.Source` 的取值（env 生效时仍是 `settings`，表示"走旧字段路径"）。
   加进去会同时改前端就绪判定的语义，属于 U 系列的范围。
