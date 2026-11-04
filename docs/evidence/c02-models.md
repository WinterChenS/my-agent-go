# C02 模型定义、连接测试与凭证

日期：2026-11-04
宿主：Windows 10 Pro 22H2（19045）/ amd64 / Intel N100
裁决：**PASS（范围与限制见 §7；本文件不等于 G0 通过，G0 另有门禁结论）**

本文件记录的是**在真实桌面上点出来的**证据：真实 `MyAgent.exe`、真实 Pi 1.5.0、
真实 deepseek 端点（`http://192.168.3.10:3425/v1`）。连接测试不是打桩——
它真的向 provider 发了请求，界面上的 2494 ms 是那一趟的实际耗时；那一轮 run
也是真的调了 `bash`、真的走了审批。

对应计划条目：`docs/superpowers/plans/2026-11-03-resources-workbench.md` 的 C02。
验收口径：`docs/acceptance/v1-gates.md` 的 MOD01–MOD04、SEC07。

## 1. 被测物

| 项 | 值 |
|---|---|
| 可执行 | `build/bin/MyAgent.exe`（Wails v3 + 内嵌前端产物，`index-uZbpSfzE.js`） |
| 用户数据 | `MYAGENT_HOME=D:/project/my-agent-go/.tmp/e2e/home-c01`（临时目录，不污染真实数据） |
| 模型 | 真实 deepseek：provider `deepseek` / model `deepseek-2/deepseek-v4.1-flash` |
| Pi | `.tools/pi/v1.5.0`（Node 24.16.0 启动） |
| 凭证 | Windows DPAPI（`credentials/<sha256>.cred`），配置里只有 `credentialRef` |
| 交互方式 | `agent-desktop click / set-value / find / snapshot / screenshot`（真实 UI 操作，非坐标猜测） |

## 2. 交付物

| 层 | 文件 | 内容 |
|---|---|---|
| 资源服务 | `internal/resources/models.go` | schema 校验、CAS 保存、`compilePiModels`（models.json 的唯一实现）、`CompileLegacy`、`WritePiModels`(0600) |
| 连接测试 | `internal/resources/probe.go` | 临时 Pi RPC 会话、两趟（文本/工具）、错误分类、能力三态 |
| 绑定 | `internal/app/model_api.go` | `ListModels / SaveModel / DeleteModel / TestModel / SetActiveModel / CurrentModel / ModelsForProfile` |
| 运行时 | `internal/app/desktop.go` | 设置拆分（`persistSettings` vs `SaveSettings`）、`modelSetup.key()` 运行时签名、`pendingRuntimeKey`、`scrubModelsFile` |
| 快照 | `internal/run/service.go`、`internal/contracts/{run,errors}.go` | 快照记录 provider/model/`configRevision`；新增 `RATE_LIMITED` / `PROVIDER_UNREACHABLE` / `PROVIDER_ERROR` |
| 前端 | `frontend/src/features/models/{ModelEditor,ModelsSection}.tsx` | 定义表单、密钥独立入参、计费提示、探针结果三态、旧格式标注 |
| 前端 | `types.ts` / `api.ts` / `useBackend.ts` / `runState.ts` / `RunsView.tsx` / `SettingsView.tsx` | 模型类型与调用、`runMeta`（MOD04 记录）、状态刷新、诊断行 |

测试：`internal/resources` 19 条（含 4 条真实 Pi 集成）、`internal/app` 新增 10 条、
`internal/run` 新增 1 条、前端 10 个文件 72 条。

## 3. 真机操作序列

| # | 操作 | 结果 | 截图 |
|---|---|---|---|
| 1 | 资源库 → 模型 → 新建 | 表单默认 `openai-completions`、128000 / 8192 | `01-resources.png` `02-new-model-form.png` |
| 2 | 填 8 个字段（含 API Key） | 密钥输入框显示为密文圆点 | `03-form-filled.png` |
| 3 | 保存 | `config-user.json` rev 4 出现 `m-deepseek`，**只有 `credentialRef`，无任何密钥字段**；`credentials/370ee33a….cred` 生成 | `04-saved.png` |
| 4 | 编辑 → 测试连接 | **可达 · 2494 ms · 工具调用：支持 · 视觉：未测** | `05-probe.png` |
| 5 | 设为当前 | `settings.json`：`modelRef=m-deepseek`，`apiKey` 被清空（长度 0） | — |
| 6 | 旧格式条目 `m-demo`（C01 遗留形状） | 标「旧格式」，不再显示「上下文 0 · 输出 0」，`设为当前` 置灰 | `06-legacy-listed.png` |
| 7 | 切换 `m-demo` → 切回 `m-deepseek` | `agentDir/models.json` 被重写：明文密钥消失，改为 `$MYAGENT_CRED_370EE33AEEA8`；`providers` 只剩 `deepseek`（空键 `""` 不再出现） | — |
| 8 | 工作台发「读一下 real.txt 的第一行…」 | 真实模型提出 `bash: head -1 real.txt` → 审批条 → 允许一次 → 完成，回答 `real-model-ok` | `07-run-started.png` `08-run-done.png` |
| 9 | 运行记录 | **模型 `deepseek/deepseek-2/deepseek-v4.1-flash` · 配置 REVISION 4** · 状态已完成 | `09-run-record.png` |
| 10 | 第二轮（不调工具） | 完成，回答「…在不调用任何工具的前提下我无法读到它的具体名称。」 | `10-run2-done.png` |
| 11 | 设置 → 诊断 | 当前模型来源 `资源库 m-deepseek`；实际运行模型 由「尚未启动」变为 `deepseek/deepseek-2/deepseek-v4.1-flash` | `11-settings-modelref.png` `12-settings-diagnostics.png` |

第 7 步是这次真机验证里最有价值的一条：**它抓到了磁盘上真实的明文密钥**。
证据链是确定的——`models.json` 的 mtime（16:29）早于模型保存（16:58），内容里
躺着一串 62 字符的明文 key，来自 C01 的旧写入路径。切换模型后文件被覆盖，
再扫全目录（配置 + 会话 + 凭证 + 证据）对真实密钥及其三种 base64 变体：

```
扫描根: ['.tmp/e2e/home-c01', 'docs/evidence']
命中数: 0
```

## 4. 自动化验证（本次改动后重跑）

| 命令 | 结果 |
|---|---|
| `./scripts/devtools.sh verify --suite=static --target=win-x64` | `[PASS] gofmt` / `[PASS] go vet ./...` / `[PASS] 前端 typecheck`，退出码 0 |
| `./scripts/devtools.sh verify --suite=unit --target=win-x64` | `[PASS] go test ./internal/...` / `[PASS] 前端测试`，退出码 0 |
| `./scripts/devtools.sh verify --suite=integration --target=win-x64` | 24 条必需 case，失败 0，阻塞 0 |
| `MYAGENT_PI_WORKER_INTEGRATION=1 go test ./internal/resources -run TestProbe -v` | 4 条全 PASS：可达文本 3.45s、检出工具调用 3.50s、无工具调用 3.38s、失败分类 7.20s（401 / 429 / 慢响应 三子用例全 PASS） |
| `go test ./internal/...` | 全部 ok |
| 前端 `npx vitest run` | 10 文件 72 条全通过 |
| `./scripts/devtools.sh gate --id=G0 --evidence=docs/evidence/runs/04579b5/win-x64` | `passed=true exitCode=0`，20 条必需 case found + pass |

## 5. 真机与自查抓到的缺陷（都已修，且都被测试钉住）

| # | 缺陷 | 后果 | 修法 | 固定它的测试 |
|---|---|---|---|---|
| 1 | 旧路径 `writeModels` 把明文密钥写进 `models.json` | SEC07 直接失败：配置目录里有一份完整密钥 | 删掉 `writeModels`，旧路径改走同一套 `compilePiModels`（`CompileLegacy`），只写 `$ENV`，密钥经子进程环境变量交付 | `TestLegacyModelsKeepsSecretOutOfJSON`、`TestLegacyModelsFillsApiKeyForLocalEndpoints` |
| 2 | 切到凭证库模型后，旧的明文 `models.json` 仍留在磁盘上 | 已没有任何读取者，但一份诊断包就能把它带走 | `scrubModelsFile()`：空闲时用当前配置覆盖；有 run 在跑则等下一次重建（不碰正在读它的 Pi） | `TestActivatingModelScrubsStaleModelsJSON`（去掉调用即变红，已对抗性验证） |
| 3 | 旧形状条目会被编译成 `"providers": {"": {...}}` | Pi 要么拒绝整份文件、要么匹配到不存在的 provider，报错不会指向真正原因 | 编译时跳过并记 `PiModels.Skipped`；列表标「旧格式」；`SetActiveModel` 与 `resolveModelSetup` 双重拦截 | `TestCompileSkipsLegacyShape`、`ModelsSection > 旧格式条目标注需重建` |
| 4 | `normalizeDefinition` 无条件清空 `enabled` | 界面显示「已禁用」，实际仍被编译进 models.json | 只抹冗余的显式 `true` | `TestCompileSkipsDisabledAndGroupsProviders` |
| 5 | `SetActiveModel` 走 `SaveSettings` 会 `Close` 运行时 | 换模型把正在跑的 run 连根拔掉 | 拆出 `persistSettings`（只落盘），`SaveSettings` 保留"关运行时"语义 | `TestModelSwitchDoesNotInterruptRunningRun` |
| 6 | 设置页「实际运行模型」永远显示「尚未启动」 | 一个用来陈述事实的诊断面板在撒谎 | 新增 `refreshStatus()`，Start 成功后刷新 | —（真机验证，见 §3 第 11 步） |
| 7 | 运行记录不显示这一轮用的 provider/模型/revision | MOD04 要求"记录真实 provider/model 及配置 revision"，界面上无从核对 | 快照带 `provider/model/configRevision` → `runMeta` → 运行记录新增两行 | `TestSnapshotRecordsModelAndConfigRevision`、`runState > run_started 记录实际使用的模型` |

## 6. 几条刻意的设计选择（避免以后"优化"回去）

- **API 白名单来自真实 Pi bundle**：`TestSupportedAPIsMatchInstalledPiBundle` 扫描
  `.tools/pi/v1.5.0/**/chunks/*.js` 里的 `api: "…"` 字面量，收录 9 个对话协议、
  显式排除 7 个并写明原因（分类器接口、图像接口、radius 专用、占位 `unknown`）。
  Pi 升级新增 API 时这条测试会红，逼人复核，而不是默默漏掉。
- **密钥两段式**：`models.json` 只有 `"apiKey": "$MYAGENT_CRED_<sha256[:6]>"`，
  真实值在子进程 env 里。env 名用哈希而不是 ref 原文（env 名只允许下划线+大写）。
- **无凭证端点填 `local-no-key`**：Pi schema 要求 apiKey ≥1 字符，空串会让整份文件
  校验失败，症状伪装成"provider 不存在"。
- **能力三态用字符串而非 bool**：`unknown / supported / unsupported`。
  没测过的能力写成 `false` 或 `true` 都会让用户对我们并不掌握的判断产生信任。
  本轮视觉恒为 `unknown`（界面显示"未测"）。
- **同 ProviderID 的 baseUrl/api 不一致时拒绝编译**（CONFIG_CONFLICT），不"取最后一个"。
- **`thinkingLevelMap` 只对 effort 型 API 下发**：anthropic 用预算而不是这个枚举，
  凭空映射等于把错的值发给 provider。
- **错误分类分开**：`UNAUTHORIZED` / `RATE_LIMITED` / `RPC_TIMEOUT` / `PROVIDER_UNREACHABLE` /
  `PROVIDER_ERROR`。三者混成"连接失败"等于让用户自己猜下一步。
- **连接测试关掉自动重试**（`set_auto_retry enabled:false`）：否则 429 会被重试成"超时"。

## 7. 限制与未覆盖（不要据此推断已完成）

1. **视觉能力没有被测过**。连接测试只发文本（工具趟发 `read`），`Vision` 恒为
   `unknown`，界面显示"未测"。真实多模态能力在 X01 验证。
2. **fixture 只能验证错误分类，不能认证外部模型**。401/429/超时是 fixture 端点
   真实返回的，但"这个模型是否真的支持工具调用"只有真实 provider 才算。
3. **只跑了 Windows**。DPAPI 之外的凭证库（macOS Keychain、Linux Secret Service）
   未验证；Linux 上无 Secret Service 时 `CREDENTIAL_STORE_UNAVAILABLE` 的分支只有单测。
4. **"等空闲生效"只有单测覆盖**。真机上要触发 `pendingRuntimeKey` 需要在一个长 run
   进行中换模型；本轮真机的两次 run 都太短，窗口没打开。
5. **旧设置路径的明文密钥仍在**（用户不迁移时）。C02 只保证这条路径不再把密钥
   复制到 `models.json`，并让界面明确告警"当前走明文路径"。把设置里的 key 迁进
   凭证库属于另一次迁移，不在 C02 范围。
6. **历史运行不持久化**。运行记录里的 provider/revision 来自内存快照，
   重启后不可查（C06）。

## 8. 复现命令

```bash
cd /d/project/my-agent-go
"D:/Program Files/nodejs/npm.cmd" --prefix frontend run build
go build -o build/bin/MyAgent.exe ./cmd/myagent

MYAGENT_HOME="D:/project/my-agent-go/.tmp/e2e/home-c01" \
MYAGENT_UI_A11Y=1 \
MYAGENT_SHELL=1 \
./build/bin/MyAgent.exe
```

`MYAGENT_UI_A11Y=1` 是必须的：不开时 WebView2 在可访问性树里只剩一个空的
`Chrome Legacy Window`，任何点击都只能靠坐标猜。

连接测试集成用例（需要 Pi 1.5.0 就位）：

```bash
MYAGENT_PI_WORKER_INTEGRATION=1 go test ./internal/resources -run TestProbe -v
```

## 9. 截图索引

`docs/evidence/raw/c02-models/`

| 文件 | 内容 |
|---|---|
| `01-resources.png` | 资源库顶部模型区块 |
| `02-new-model-form.png` | 新建表单，API 下拉来自后端白名单 |
| `03-form-filled.png` | 8 个字段填好，API Key 为密文 |
| `04-saved.png` | 保存后列表出现 `m-deepseek`，无「C02 未实现」字样 |
| `05-probe.png` | 真实连接测试：可达 2494 ms / 工具调用 支持 / 视觉 未测 |
| `06-legacy-listed.png` | 旧格式条目标「旧格式」、设为当前置灰 |
| `07-run-started.png` | 真实 run 的审批条（`bash: head -1 real.txt`） |
| `08-run-done.png` | run 完成，回答 `real-model-ok` |
| `09-run-record.png` | 运行记录：模型 + 配置 revision 4 |
| `10-run2-done.png` | 第二轮（无工具）完成 |
| `11-settings-modelref.png` | 设置页：当前模型来自资源库的告警 |
| `12-settings-diagnostics.png` | 诊断区：实际运行模型 `deepseek/deepseek-2/deepseek-v4.1-flash` |
