# MDIAG 报告 — 说清模型配置为什么不可用

- Worktree：`D:\project\anvil-wt\mdiagnose`，分支 `feat/mdiagnose`
- 起点：`4366a28`（Merge WTRUST）
- 状态：**PASS**（本机可验证范围内）

## 1. 症状与根因

用户实测：自定义模型能保存（`3037296`），启动仍只回「读不到有效配置：缺少必要的配置」。

根因不是"没写文案"，而是三件事：

1. `DiagnoseModelConfig` 这一类事实根本不存在——没有任何接口能一次回答
   "选中的是谁 / 哪条进了 models.json / 为什么没进 / 文件在哪"。
2. `describeSkipped` 只吐 ID，而且把原因**硬编码**成"旧格式，缺少 provider 名或模型名"。
   凭证引用悬空、被禁用、provider 漂移都走同一句，用户照着修只会更困惑。
3. 前端 `formatError` 一律显示 `errors.CONFIG_MISSING` 的通用描述（这是有意的：
   en-US 下不能显示 Go 的中文 message）。所以 Go 侧消息再详细，界面上也看不见——
   必须有一条界面自己的诊断路径。

## 2. 做了什么

### 2.1 resources（`internal/resources/models.go`、`probe.go`）

- `PiModels.Skipped` 从 `[]string` 改为 `[]ModelSkip{ModelID, Reason}`。
  理由：诊断与启动报错都要回答"为什么"，而修法因原因而异（改定义 / 启用 / 重存密钥）。
- 跳过原因分别是：缺 provider 名 / 缺模型名（`missingShapeReason`）、`已禁用`、
  凭证不可用（携带凭证 ref 与底层错误）、provider 的 baseUrl/api 漂移。
- 新增 `Service.CompilePiModelsDiagnostic`：与严格编译**共用同一份** `compilePiModels`，
  只增加一个 `lenient` 开关——单条失败记进 `Skipped` 并继续。
  `CompilePiModels`（启动走的那条）仍是严格模式，**没有**放宽：
  凭证悬空时它照旧整份失败（有专门用例钉住）。

### 2.2 app（`internal/app/model_api.go`）

新增绑定 `Desktop.DiagnoseModelConfig() ModelDiagnosis`（只读：不写文件、不启动 Pi、
不调用模型）：

| 字段 | 含义 |
|---|---|
| `selectedRef` / `selectedSource` | `resources` / `settings` / `none` |
| `selected` | providerId、modelId、baseUrl、api、credentialRef（**只有引用**） |
| `models[]` | 逐模型 `ok` / `skipped` + `reason` |
| `compiled` | 真正进了 models.json 的条数 |
| `skipped[]` | `{modelId, reason}`（编译输出原样） |
| `modelsJsonPath` / `modelsJsonExists` | 真正会被 Pi 读的那份文件 |
| `ready` / `reason` | 不可用时的唯一一句原因（含下一步动作） |

判定规则全部来自编译结果，绑定层不重写一遍。`models.json` 还没生成**不算**不可用
（下一次启动才写），有用例钉住这条。

### 2.3 app（`internal/app/desktop.go`）

- `describeSkipped` 改为带原因：`（未参与编译：m-x：缺少 provider 名；…）`。
- 新增 `modelConfigHint` / `withModelHint`，并把包装放在 `resolveModelSetup` 的**唯一**
  出口上（不是每个分支各写一遍，新增分支时不会有人忘记加）。覆盖到的
  CONFIG_MISSING 分支：
  - 旧字段路径端点/模型为空（"保存了但没设为当前"）→ 补"资源库里有 N 个模型（ids），
    但没有一个被设为当前模型；请点设为当前模型"+ 跳过原因；
  - `pm.JSON` 为空 → 原本就带 `describeSkipped`，现在带上原因；
  - 选中的 ref 指向空 → `DEPENDENCY_MISSING` 的原文已经是可执行句子，
    `withModelHint` 只在有新信息（跳过原因）时追加，不重复啰嗦。

### 2.4 前端（`frontend/src/features/settings/**`、`types.ts`、i18n）

- 新增 `ModelDiagnosis.tsx`：设置→模型里一个「检查模型配置」按钮，**点击才**诊断
  （诊断会读凭证库，不该在打开设置页时静默发生）。渲染：
  - `ready=false` 时 `role="alert"` 的醒目原因行（结论先行，不埋在列表里）；
  - 当前模型：ref + 来源 + provider/model/baseUrl/api/credentialRef（引用）；
  - 逐模型状态：`ok` / `跳过：<原因>`；
  - 跳过段：**仅 `ready=false` 且非空时**出现；
  - models.json 路径 + 是否已生成；`compiled` 计数；
  - 结构化失败（读不到配置）时说"诊断失败"，不假装可用；
  - 不展示原始 JSON；任何位置都不出现密钥值。
- `types.ts` 增加 `ModelDiagnosis` / `ModelDiagnosisEntry` / `ModelDiagnosisSelection`；
  `settings/api.ts` 增加 `diagnoseModelConfig()`。
- 文案全部进 catalog：`settings.model.diagnosis.*`（zh-CN + en-US，key 集合一致）。

## 3. 验证（实际命令与结果）

| 命令 | 结果 |
|---|---|
| `go test ./internal/resources/ -count=1` | ok |
| `go test ./internal/app/ -count=1` | 仅 `TestDiscoverPiAcceptsRealPiThroughProductionProbe` 失败（缺 `.tools/pi/standalone/v1.5.0-win-x64/pi.exe`，基线同样失败） |
| `go test ./internal/... -count=1` | 除 4 条"需要本机官方 Pi 独立包"的用例（app / piinstall×2 / run / runtime/pi）外全部 ok；已用 `git stash` 在基线上复核这些用例**同样**失败 |
| `go vet ./...` | 0（需先 `vite build` 生成 `frontend/dist`，基线同样要求） |
| `gofmt -l internal/ cmd/` | 空 |
| `tsc --noEmit` | 0 |
| `vitest run`（Node 24.16.0 显式路径） | 47 files / **422 tests passed** |
| `node scripts/check-ui-contract.mjs` | PASS（catalog 之外增量 0，两种语言覆盖 40 个错误码） |

新增用例：

- `internal/resources/models_test.go::TestCompileDiagnosticReportsDanglingCredential`
  —— 严格编译仍整份失败；诊断编译 `Skipped` 含 `m-cred` 且原因提到悬空的凭证名；
  正常模型仍在 JSON 里；输出无明文密钥。
  （另在 `TestCompileSkipsLegacyShape` 补了"原因要指明缺什么"的断言。）
- `internal/app/model_diagnosis_test.go`
  - `TestDiagnoseModelConfigReportsDanglingCredential`：not ready、`Skipped` 含该模型与原因、
    `Models[0].Status=="skipped"`、`compiled==0`、`credentialRef` 只给引用、序列化后无明文密钥；
  - `TestDiagnoseModelConfigReadyWithOneCompiled`：ready、`compiled==1`、无 `Skipped`、
    models.json 未生成不影响 ready、selected 三字段正确；
  - `TestStartReportsSkippedReasonWhenNothingSelected`：CONFIG_MISSING 消息含被跳过模型 ID、
    跳过原因（缺 provider）与"设为当前模型"；
  - `TestStartReportsDanglingCredential`：启动失败消息含悬空凭证名，且不含明文密钥。
- `frontend/src/features/settings/ModelDiagnosis.test.tsx`（5 例）：不挂载即诊断（只有点击才调）、
  原因行是最醒目的一行、逐模型状态 + 跳过段带原因、路径显示而 JSON 正文不出现、
  ready 时无跳过段、结构化失败不假装可用。

## 4. 断言变更（不是放宽）

- `TestCompileSkipsLegacyShape`：`pm.Skipped[0] != "m-old"` → `pm.Skipped[0].ModelID != "m-old"`，
  并**新增**"原因必须指明缺 provider"的断言（原断言在类型变化后无法表达，且原来的断言更弱）。
- 没有删除用例、没有 `t.Skip`、没有弱化任何断言。

## 5. Touch 列表核对

改动文件：`internal/app/{desktop.go,model_api.go,model_diagnosis_test.go}`、
`internal/resources/{models.go,models_test.go,probe.go}`、
`frontend/src/{types.ts,i18n/locales/*.json,features/settings/{ModelSettings.tsx,api.ts,ModelDiagnosis.tsx,ModelDiagnosis.test.tsx}}`。

未触碰：`internal/run/**`、`internal/devtools/**`、`tests/**`、`docs/**`、`schemas/**`、
其它 features（含 `frontend/src/features/models/**`、`chat/**`）。

## 6. Concerns

1. **`ready.reason` 是 Go 中文散文**，不是 catalog key。与 `errors.<CODE>` 的
   "只给 code + params"纪律有张力：`reason` 里天然含有编译原因（"缺少 provider 名"、
   "凭证 cred-x 不可用"），把它拆成可翻译的 code/params 需要一整套原因枚举，
   超出本任务书范围。现状是：**结论行**走 catalog（`settings.model.diagnosis.notReady`），
   `reason` 作为技术细节附在后面——与 `Skipped[].reason` 同性质。若要 en-US 干净，
   下一步应把原因收敛成 `code + params`。
2. **聊天页仍只显示通用错误文案**。`Start` 的消息现在细了，但 `formatError` 不用
   `error.message`（这是对的），所以用户在对话页看到的还是 `errors.CONFIG_MISSING`；
   差别在于他现在可以点「检查模型配置」看到原因。要让聊天页的失败就地可读，
   需要给 Start 失败加结构化 `params`（catalog 插值）——那会动 chat feature，本任务书不允许。
3. **Pi 侧的 CONFIG_MISSING 未覆盖**：`resolveInstallation` 的"未配置 Pi 路径"同样
   会显示同一句通用文案，但它不是模型配置问题，不属于 MDIAG 范围。
4. `CompilePiModelsDiagnostic` 会为每条模型取一次凭证（`creds.Get`）。诊断由用户点击
   触发、模型数量是十级，不会成为问题；但不要把它挂到挂载/轮询上。
5. 本机是 Windows 10 22H2：本轮所有 Go/前端证据都是**开发证据**，不构成 win-x64 验收结论。
6. 为跑 `go vet ./...` 在 worktree 里执行了 `vite build`，`frontend/dist` 是 gitignore
   产物（基线同样要求先构建），未进入提交。
