# 模型与运行配置的环境变量

面向"预览包 / CI / 临时指到一个 fixture provider"这一类场景：**不改用户设置文件**，
只在本进程里改一次行为。实现见 `internal/app/settings_env.go`（M1ENV）。

## 变量表

| 变量 | 生效为 | 说明 |
|---|---|---|
| `ANVIL_PROVIDER` | `settings.provider` | models.json 里的 provider 键名 |
| `ANVIL_MODEL` | `settings.model` | 模型 id |
| `ANVIL_BASE_URL` | `settings.baseUrl` | OpenAI 兼容端点（模型的 base URL 就是 `Settings.BaseURL`，没有第二个字段） |
| `ANVIL_MODEL_API_KEY` | `settings.modelAPIKeyRef` = `env:ANVIL_MODEL_API_KEY` | **只保留引用**；密钥原文只在进程内存里 |
| `ANVIL_PI_PATH` | `settings.piPath` | 与上面同一套优先级（此前只在解析安装件时补） |
| `ANVIL_NODE_PATH` | `settings.nodePath` | Pi 是 `.js` 入口时用 |
| `ANVIL_WORKSPACE` | `settings.workspace` | 工作目录 |
| `ANVIL_SHELL=1` | `settings.shellAllowed` | 只在设置里没写这个键时 |
| `ANVIL_API_KEY` | `settings.apiKey` | **旧明文路径**，仅用于 fixture/本机端点；新场景请用 `ANVIL_MODEL_API_KEY` |

旧前缀 `MYAGENT_*` 仍可用一个开发兼容周期：`ANVIL_*` 优先，命中旧名会记一条弃用日志
（见 `internal/app/env.go`）。变量名不带前缀传入 `envLookup`，因此
`ANVIL_MODEL_API_KEY` / `MYAGENT_MODEL_API_KEY` 都认。

## 优先级

```text
设置文件（settings.json 写了这个键） > 环境变量 > 内置默认值
```

- **设置永远优先**：只要 `settings.json` 里写了这个键（哪怕写的是空串），环境变量就不参与。
  "写了空串"也是用户说的话：比如他清掉了端点，环境变量不得偷偷顶回来。
- **环境变量只补缺席的字段**：键不存在时才生效。一次运行里补上的值不会改变下一次运行的配置。
- **默认值兜底**：两者都没说话时用内置默认（provider `fixture`、
  `http://127.0.0.1:0/v1`、`fixture-model`）。这个默认端点连不上，因此界面会把它当作
  "还没配置真模型"拦住发送——这是有意的诚实，不是 bug。

## 不落盘

环境变量补上的值**不会写进 `settings.json`**：任何保存路径（设置页保存、切换模型、
迁移）都会先把"仍等于环境值、且磁盘上没写过这个键"的字段清空再写
（`stripEnvDerived`）。因此在预览里保存一次设置，也不会把自动化用的值固化进用户配置。

推论：磁盘上看不到这些值，重启后如果没有环境变量，行为会回到设置/默认值——
这正是"临时覆盖"该有的样子。

## 密钥

`ANVIL_MODEL_API_KEY` 的明文只在进程内存里：

- `settings.json` 里只有引用 `env:ANVIL_MODEL_API_KEY`（且这条引用本身也不会被固化，见上）；
- 交给 Pi 的 `models.json` 里只有 `$MYAGENT_CRED_…` 这样的环境变量引用；
- 子进程环境里才有值，Pi 从那里读；
- 日志、诊断包、`EffectiveConfig` 都不含明文（`EffectiveConfig` 只给引用）。

想看"密钥到底有没有交到 Pi"：看 `models.json` 的 `apiKey` 是不是 `$` 开头的引用，
以及 worker 启动环境里有没有同名的变量——不要把它打印出来。

## 从哪里看"这次是谁给的"

- 设置页 → 模型分区顶部的「模型来源」面板：逐项显示 provider / model / baseUrl / 密钥引用
  的值、来源（设置 / 环境变量 / 默认值 / 未设置），环境变量补位的项还会显示
  `ANVIL_…` 变量名并给一条提示。
- 绑定 `Desktop.EffectiveConfig()` 的 `model` 段是同一份事实
  （`provider` / `model` / `baseUrl` / `apiKeyRef` 各带 `source` 与 `envVar`，
  `overriddenByEnv` 列出被环境补上的字段名）。界面不允许静默覆盖。

## 例子

PowerShell：

```powershell
$env:ANVIL_PROVIDER = "fixture"
$env:ANVIL_MODEL = "fixture-model"
$env:ANVIL_BASE_URL = "http://127.0.0.1:8080/v1"
$env:ANVIL_MODEL_API_KEY = "sk-local-demo"   # 只在本进程内，不落盘
```

bash：

```bash
ANVIL_PROVIDER=fixture \
ANVIL_MODEL=fixture-model \
ANVIL_BASE_URL=http://127.0.0.1:8080/v1 \
ANVIL_MODEL_API_KEY=sk-local-demo \
  ./anvil
```

验证这一轮真的用上了：对话页不再被"模型未配置"拦住，
设置页的「模型来源」显示 `环境变量 · ANVIL_PROVIDER` 等徽标。
