# MP1 报告 — 模型维护：本地发现、供应商、以及设置页里缺失的模型区

- 分支：`feat/mp1`（worktree `D:/project/anvil-wt/mp1`）
- 任务：MP1（用户原话：需要可以自动读取本地已有的模型，并且可以额外添加模型和供应商）
- 状态：**PASS（本机开发证据）**；跨平台/真实本地服务端到端仍需在有对应服务的机器上复跑（见"未覆盖 / 风险"）。

## 1. 结论先说

1. 后端新增：`DiscoverModels`（只探回环、并行、有界、稳定错误码）、`ProbeProviderModels`
   （用户显式扫描任意端点）、`ImportModels`（按 provider+model 幂等）、
   `ListProviders` / `SaveProvider` / `DeleteProvider`，模型可继承供应商的 baseUrl/api。
2. 前端：**设置 → 模型** 成为唯一维护入口（列表/新建/编辑/删除/测试连通/设为当前 +
   扫描本地模型 + 添加供应商）。资源库不再挂模型页签（避免两处都能改出第二份真相）。
3. 用户要的"局域网明文 HTTP 网关"可用：私网 + `http://` 放行并标 `insecure: true`，
   UI 显示"明文 HTTP，凭据会以明文经过本机网络"；公网 `http://` 默认拒绝，需显式确认。
4. 删除**当前使用中**的模型被拒绝（`CONFIG_CONFLICT`，提示先切换），不是静默取消当前选择。

## 2. 后端设计

### 2.1 端点矩阵（自动发现只探这些）

| kind | 探测地址 | 写进模型定义的 baseUrl | api |
|---|---|---|---|
| `ollama` | `GET http://127.0.0.1:11434/api/tags`（失败再试 `/v1/models`） | `http://127.0.0.1:11434/v1` | `openai-completions` |
| `lmstudio` | `GET http://127.0.0.1:1234/v1/models` | `http://127.0.0.1:1234/v1` | `openai-completions` |
| `vllm` | `GET http://127.0.0.1:8000/v1/models` | `http://127.0.0.1:8000/v1` | `openai-completions` |
| `llamacpp` | `GET http://127.0.0.1:8080/v1/models` | `http://127.0.0.1:8080/v1` | `openai-completions` |

- 单端点超时 1.5s，4 个目标并行（信号量上限 8，即使以后扩表也不会一次开几十个连接）。
- 只探回环：目标清单是常量，拨号层再挡一道；非回环目标返回 `DISCOVERY_NOT_LOOPBACK`
  且**不产生任何拨号**（测试用拨号钩子断言）。
- 解析两种真实形状：Ollama `{models:[{name,size}]}` 与 OpenAI `{data:[{id}]}`
  （兼容网关的 `{models:[{id}]}` 也认）。
- 稳定错误码：`DISCOVERY_UNREACHABLE` / `DISCOVERY_TIMEOUT` / `DISCOVERY_HTTP_STATUS` /
  `DISCOVERY_BAD_RESPONSE` / `DISCOVERY_NOT_LOOPBACK`。message/detail 只放诊断细节。

### 2.2 供应商语义（本轮选了什么）

供应商是**派生的一等对象**，不是需要单独登录的实体：

- 落盘：用户配置文档新增 `providers: [{id, baseUrl, api}]`。`ModelIDs`/`Insecure` 是派生字段，不落盘。
- 列举：`ListProviders` = 显式记录 ∪ 只靠 `providerId` 出现的旧配置记录，带引用模型 ID；
  `Insecure` 由 baseUrl 算出。
- 继承：模型 `baseUrl`/`api` 留空时从供应商取值。**解析点只有两处**：`List`（展示）
  与 `compilePiModels` 的输入（`CompilePiModels` 走 `List`）。`models.json` 的 provider 分组
  仍然只由 `compilePiModels` 一处决定，没有第二份生成逻辑。
- `SaveProvider`：校验 id/api/端点；把引用该供应商的模型的 baseUrl/api **一起改**。
  理由：同一个 provider 键在 models.json 里只能有一个端点，否则编译直接 `CONFIG_CONFLICT`；
  用户改的是"这个供应商在哪"。
- `DeleteProvider`：**拒绝**语义。仍有模型引用时返回 `CONFIG_CONFLICT` 并列出引用者 ID；
  没有引用才删除，重复删除返回 `NOT_FOUND`。不选"自动迁移"：那会让用户正在用的模型无声改名。
- `ImportModels`：幂等键 `providerId + modelId`；重复导入更新 baseUrl/api，
  **不覆盖**用户自定义的 `contextWindow`/`maxOutput`（为 0 时才用默认值），
  保留 credentialRef/thinkingLevels/enabled；同时 upsert 供应商记录。
- 本地端点不写 `credentialRef`：`compilePiModels` 会填 `local-no-key` 占位符
  （写一个不存在的 credentialRef 反而会让编译失败）。测试断言 models.json 里是 `local-no-key`
  且不含 Authorization。

### 2.3 明文 HTTP 规则（父会话修正后的产品决定）

| 场景 | 是否允许 | `insecure` |
|---|---|---|
| `https://` 任意主机 | 允许 | false |
| `http://` 回环（127/8、::1、localhost） | 允许 | false（本机通信） |
| `http://` 私有网段（RFC1918、100.64/10、IPv6 ULA） | 允许 | **true** |
| `http://` 公网 IP/域名 | 默认拒绝；`allowInsecure=true` 显式确认后允许 | **true** |

- 判定只看 URL 主机，不做 DNS（"公网域名解析到内网"按公网处理，更安全）。
- 主机名（非 IP）无法证明是私网，按公网处理，需要 HTTPS 或显式确认。
- 用户真实场景 `http://192.168.3.10:3425/v1` + Bearer：`ProbeProviderModels` 接受并标 insecure；
  `ImportModels`/`SaveProvider` 同样接受（私网不需要确认）。

### 2.4 删除当前使用中的模型

`Desktop.DeleteModel` 在删除前检查 `settings.ModelRef`：命中则拒绝（`CONFIG_CONFLICT`，
"请先切换到别的模型再删除"）。后果：列表清理不会让下一次 run 指向不存在的 ref；
外部删除/文件损坏仍会被 `Start` 的 `DEPENDENCY_MISSING` 接住（既有测试保留，改成绕过绑定模拟外部删除）。

## 3. 前端

- `settings/ModelSettings.tsx` 现在只挂 `ModelsMaintenance`：这是唯一维护入口。
- `models/ModelsMaintenance.tsx`：当前模型来源、扫描本地模型（按端点分组、勾选、导入选中）、
  供应商（添加/探测/删除、insecure 标记）、模型列表。
- `models/ModelsSection.tsx`：新建（手动添加模型）、编辑、删除、**逐条测试连接**、设为当前；
  最近一次扫描判为不可达的端点下的模型显示"不可用"且不能设为当前。
- `models/ModelEditor.tsx`：全部文案走 catalog；baseUrl 留空说明"继承供应商端点"。
- `models/api.ts`：模型维护的绑定调用（与根 `api.ts` 分开，避免继续堆）。
- 资源库：**不再有模型页签**（父会话允许"干脆不给资源库加模型页签"）。指向资源库的引导已改：
  - `agents.createHint` / `agents.modelHint` / `agents.noResources` → 指向「设置 → 模型」
  - `settings.model.hint` / `listTitle` / `emptyHint` → 改为本页维护
  - `settings.security.credentialsEmptyHint` / `legacyInactive` → 去掉"来自资源库"的说法
  - `settings.model.source.resources` 文案改为"设置里的模型定义"（en-US 同步）
- `check-ui-contract.mjs` PASS；`ModelEditor.tsx`、`ModelsSection.tsx` 已从 baseline 移除
  （baseline total 366 → 285，只减不增）。

## 4. 用户怎么端到端接上

所有操作都在 **设置 → 模型**。

### Ollama
1. 启动：`ollama serve`（默认 `127.0.0.1:11434`），先 `ollama pull llama3.2`。
2. 设置 → 模型 →「扫描本地模型」→ 找到 `ollama` 端点 → 勾选模型 →「导入选中」。
3. 列表里选中它 →「测试连接」→ 成功后「设为当前」。
   （导入的 baseUrl 是 `http://127.0.0.1:11434/v1`，Pi 走 OpenAI 兼容接口。）

### LM Studio
1. 启动 LM Studio，打开 Developer → Start Server（默认 `127.0.0.1:1234`），加载一个模型。
2. 扫描本地模型 → 导入 `lmstudio` 下的模型 → 测试 → 设为当前。

### vLLM
1. `vllm serve <model> --port 8000`（默认 `127.0.0.1:8000`）。
2. 扫描 → 导入 `vllm` → 测试 → 设为当前。

### llama.cpp
1. `llama-server -m <gguf> --port 8080`。
2. 扫描 → 导入 `llamacpp` → 测试 → 设为当前。

### 局域网网关（用户的主场景：`http://192.168.3.10:3425/v1` + Bearer key）
1. 设置 → 模型 →「添加供应商」：ID 例如 `lan-gw`，端点 `http://192.168.3.10:3425/v1`，
   协议 `openai-completions`，保存（私网 http 直接允许，界面标"明文 HTTP…"）。
2. 在模型编辑器里新建模型：Provider 名填 `lan-gw`，Base URL **留空**（继承供应商端点），
   模型名填网关要求的名字，凭证引用填例如 `cred-lan`，API Key 输入 Bearer key 后保存。
   （密钥写入 OS 凭证库；配置里只有引用名。）
3. 「测试连接」→「设为当前」。
4. 如果端点其实在公网：默认会被拒绝（`UNSUPPORTED`）；确需明文时在供应商表单勾选
   "这是公网明文 HTTP 端点；我确认要这样保存"。

## 5. 验证与证据

| 检查 | 命令 | 结果 |
|---|---|---|
| Go 资源包（含新增发现/供应商/导入测试） | `go test ./internal/resources/ -count=1` | PASS |
| Go 绑定层模型相关 | `go test ./internal/app/ -run 'TestModel\|TestDeleteActive\|TestSaveModel\|TestSetActive\|TestStartRejectsDeleted\|TestActivating'` | PASS |
| Go 绑定层全量 | `go test ./internal/app/ -count=1` | 1 项环境依赖失败：`TestDiscoverPiAcceptsRealPiThroughProductionProbe`（本机无官方 Pi 独立包，见第 6 节） |
| 前端全量 | `node node_modules/vitest/vitest.mjs run`（frontend） | 40 文件 / 361 用例 PASS |
| 类型检查 | `tsc --noEmit` | PASS |
| 文案门禁 | `node scripts/check-ui-contract.mjs` | PASS |
| 构建 | `go build ./internal/app` | PASS（`go build ./...` 的 `all:dist` 失败是未生成 frontend/dist 的既有前提） |

红证据（TDD 首轮失败）：
- `scratch/red/resources-mp1-red.log`：新增 Go 类型/选项/方法未实现时的编译失败。
- `scratch/red/frontend-mp1-red.log`：模型维护 UI 首次运行 5 个用例失败（找不到模型页签/编辑器/导入流程）。

## 6. 未覆盖 / 风险（不伪装成通过）

1. **真实本地服务端到端未在本机取证**：本机没有运行中的 Ollama/LM Studio/vLLM/llama.cpp，
   发现矩阵的"能看到真实模型"只在 `httptest` 形状级验证；端口探测本身在真实网络栈上跑过
   （默认 4 个端口均按超时/拒绝给出稳定错误码）。接上真实服务后需在对应机器复跑扫描→导入→测试→运行。
2. **`go test ./internal/app/` 有 1 项环境失败**：`TestDiscoverPiAcceptsRealPiThroughProductionProbe`
   需要 `.tools/pi/standalone/v1.5.0-win-x64/pi.exe`（官方 Pi 独立包），本机没有。
   与本次改动无关，但全量绿需要补该证据包。
3. **本机是 Windows 10 22H2**：win-x64 证据只是开发证据，不能当验收结论（AGENTS.md 第 4 节）。
4. 供应商是 derived 模型：没有"只有供应商、零模型"之外的额外状态机；删除语义是拒绝不是迁移，
   如果后续产品要求"删供应商时把模型一起删"，需要新的一版决定（当前不做）。
5. `http://` 私网判定不做 DNS：用域名指向内网的端点会被要求 HTTPS 或显式确认，
   这是安全侧偏保守的选择，需要在文案/报告里保持一致。
6. 扫描不跟随重定向；需要重定向的网关要用户直接填最终地址。
7. 局域网网关的 TLS 自签证书目前探测阶段不校验证书（只读模型清单）；真正跑请求时由 Pi 侧
   按用户配置决定，这可能与"探测通过、运行失败"不一致，需在接真实网关时验证。

## 7. 提交
见最终回复中的 commit hashes（backend / frontend+report 各一条）。
