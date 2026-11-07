# U03 报告：单个 Agent/Profile 产品编辑页（UI07）

状态：**REVIEW**（实现与自动化验证完成；UI07 的验收口径仍缺两项，见 §6，因此案例状态是 **BLOCKED**，不是 PASS）

分支：`feat/u03`（worktree `D:/project/anvil-wt/u03`）
提交：

| 提交 | 内容 |
|---|---|
| `d309790` | 绑定层：上限比较、来源、复制、删除（+ 红证据 `scratch/red/u03-app-api-red.txt`） |
| `86f8977` | `AgentProfileCeiling`：新建前也能读到上限 |
| `015a6a9` | 前端编辑器、i18n、路由转出、红证据 `scratch/red/u03-frontend-red.txt` |
| 本报告提交 | 报告 + 原始日志 |

---

## 1. 验证命令与实际退出码

基线 Node：`D:\Program Files\nodejs\npm.cmd`（v24.16.0）；本 worktree 的 PATH 首位是旧 Node，必须把 `D:\Program Files\nodejs` 前置，否则 `vitest` 直接 startup error（`EventEmitterAsyncResource`）。日志均在同目录。

| 命令 | 退出码 | 结果 |
|---|---|---|
| `go test ./internal/app ./internal/profile -count=1` | 0 | `ok internal/app 22.694s` / `ok internal/profile 0.114s`（`scratch/u03-go-test.log`） |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend run typecheck` | 0 | `tsc --noEmit` 无输出（`scratch/u03-typecheck.log`） |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend test` | 0 | 32 files / **253 tests** passed（U03 新增 16 条；进入前基线 30 files / 237）（`scratch/u03-test.log`） |
| `node scripts/check-ui-contract.mjs` | 0 | PASS：catalog 之外文案增量 0；40 个 Go 错误码两种语言齐全（`scratch/u03-copy.log`） |
| `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static` | 0 | gofmt / `go vet ./...` / 前端 typecheck 全 PASS（`scratch/u03-static.log`） |

环境备注（可复现用，均 gitignore，不入库）：

- `frontend/node_modules` 与 `.tools/pi/standalone/v1.5.0-win-x64` 在本 worktree 里原本不存在，`go test ./internal/app` 会因此报 `TestDiscoverPiAcceptsRealPiThroughProductionProbe` 失败、`verify --suite=static` 的 `go vet` 会因 `frontend/embed.go` 的 `all:dist` 失败。分别从主仓库只读复制 `node_modules`（187M）与 `.tools`，并 `npm run build` 生成 gitignore 的 `frontend/dist` 后，两项都恢复到 PASS。这两个失败与 U03 代码无关。

## 2. 红证据（实现前）

- `scratch/red/u03-app-api-red.txt`：`go test ./internal/app -run TestAgent` → 编译失败，`SaveAgentProfile / DuplicateAgentProfile / DeleteAgentProfile undefined`。
- `scratch/red/u03-frontend-red.txt`：移走实现文件后 `vitest run agents` → 两个 suite `Failed to resolve import "./AgentEditor" / "./AgentsPage"`。

## 3. 新增绑定（`internal/app/profile_api.go`，全部 additive）

| 绑定 | 作用 | 关键约定 |
|---|---|---|
| `AgentEditorState(id, wsID)` | 读取编辑现场：文档 + 上限 + 来源 + 缺失引用 + unavailable 字段清单 | 引用解析不到时来源写 `unavailable` 并单列 `missing`，不回落到"看起来有值" |
| `AgentProfileCeiling(wsID)` | 单独返回上限（新建时无文档也要能选档） | 不让前端从某份已存在 profile 反推上限 |
| `SaveAgentProfile(id, wsID, expectedRevision, draft)` | 先比上限，再交给 `profile.Service.Save`（秘密/引用/CAS 仍在领域层） | 越界 → `INVALID_INPUT` + `params{field,requested,ceiling,source}`，写入前拒绝，源文件不变 |
| `DuplicateAgentProfile(sourceID, newID, wsID)` | 复制引用与策略 | 新文档 `revision` 从 1 开始；秘密形状字段不随复制落地；同 ID 覆盖 → `CONFIG_CONFLICT` |
| `RenameAgentProfile(id, wsID, expectedRevision, name)` | CAS 改名 | 过期 revision → `CONFIG_CONFLICT`；改名不做上限检查（改名不改策略） |
| `DeleteAgentProfile(id, expectedRevision)` | 按 revision 删 | 先 CAS 占住这一版（旧 revision 的并发写者从此冲突），再删文档文件；只删 profile，不动被引用的资源 |

上限口径（重要，且与 `config.Merge` 对齐）：

- 上限 = 应用策略（`effectivePermissions`：filesystem=workspace、network=false、shell=设置值）与**显式设置过的**配置层取更严一侧；
- 只有 `Provenance` 里出现 `permissions.*` 的层才参与收紧。`Merge` 对没人设置的字段用"未设置哨兵"起算，那是最严默认值而不是上限，照它比较会让 filesystem 永远停在 `none`；
- 下层配置**不用** `Store.Compile(ProfileID:"")`：它的 key 是 `config-profile-default`，一个恰好叫 `default` 的 profile 会混进"上限"里，检查就成了自证。

## 4. 前端（`frontend/src/features/agents/**`）

- `AgentsPage.tsx`：左栏真实 Profile 列表 + 新建（ID + 模型，走 `SaveAgentProfile(expected=0)`）；空/加载/错误+重试三态来自壳层后端快照；选中态由路由 `?agent=` 决定。
- `AgentEditor.tsx`：名称、模型、Skills、MCP、权限边界、记忆；`AsyncState`/`StatusBadge`；文案全部 `agents.*`；来源用 `agents.source*` 显示（`system`/`default`/`user`/`workspace`/`unavailable`）；缺失引用单列。
- `api.ts`：FQN 集中；`resources()` 逐项 try/catch，读不出来的项进 `unavailable`（不显示 0）。
- `permissions.ts`：宽严序纯函数（界面禁用越界选项用），Go 侧 `TestAgentFilesystemOrderMatchesMerge` 用 `Merge` 的实际行为逐对校验，防止两边分叉。
- 路由：`frontend/src/app/pages/index.tsx` 只做转出（`export { AgentsPage } from "../../features/agents/AgentsPage"`），不保留第二份页面状态。
- i18n：`agents.*` 中英各 72 条，key 集合一致（契约检查通过）。

## 5. 五个必须行为 → 可执行断言

| 行为 | Go（`internal/app/profile_api_test.go`） | 前端（`AgentEditor.test.tsx`） |
|---|---|---|
| 过期 revision 暴露冲突且不覆盖 | `TestAgentProfileStaleRevision*`：第二个 writer 胜出；过期保存 `CONFIG_CONFLICT`；**重启后**（同一 root 重建 Desktop）读回仍是别人的值 | 保存带 `baseRevision`；冲突面板出现"什么都没写"；不出现"已保存"；重载后取磁盘值 |
| 复制只复制引用不复制秘密 | `TestAgentProfileDuplicateCopiesReferencesNotSecrets`：源文档被手工塞入 `apiKey`/`credentialRef` 哨兵，复制后的文档里两者都不存在；引用与策略一致；`revision=1`；同 ID → 冲突 | 复制只发 `(sourceId, newId, workspaceId)`；界面上没有可填密钥的字段 |
| 删除要确认且不删被引用资源 | `TestAgentProfileDeleteNeedsRevision*`：错 revision → 冲突且 profile 仍在；删后 `NOT_FOUND`；用户层的 models/skills/mcp 一条不少；同一批引用还能再存新 profile | 第一次点击不调用后端；确认后 `remove(id, revision)`；拒绝时编辑区仍在并显示删除失败 |
| 试图放宽权限被结构化拒绝且 UI 显示原因 | `TestAgentProfileRejectsWideningBeyondCeiling`：filesystem/shell/network 三种越界都 `INVALID_INPUT`，`params` 点名字段、请求值、上限、来源层；被拒后没有任何落盘；收紧允许；`AgentProfileCeiling` 与编辑现场同口径 | 越界选项 `disabled`；上限与来源可见；后端拒绝时显示 `agents.ceilingRejected`（字段/请求/上限/来源），不出现"已保存" |
| 重启后读回同样的值 | `TestAgentProfileRenameAndRestartReadBack`：改名走 CAS，过期改名冲突；重启后 `AgentEditorState` 的名字/revision/引用/策略一致 | 重新挂载显示后端返回值（不显示上一次的草稿） |

## 6. 缺口与限制（为什么 UI07 仍是 BLOCKED）

1. **领域模型不承载 `Description / SystemPrompt / ReasoningLevel / ToolIDs`。**
   本任务书的 Touch 列表明确禁止改 `internal/profile/**`（要求"报告缺口"）。`profile.Profile` 没有这四个字段，前端写进去会被 `json.Unmarshal` 静默丢掉——所以编辑器把它们标成 `unavailable`（`agents.unavailableField`），没有画假输入框。
   后果：UI07 里"model、prompt、思考级别……真实保存，且 system prompt 与思考级别实际进入 Pi 请求（fixture provider 记录）"这半句**没有实现**，需要后续在 `internal/profile` + `internal/config/compile.go` + run 启动路径补齐（属于被本任务书禁止触碰的范围）。
2. **领域层没有上限检查。** `profile.Service.Save` 只做形状/引用/秘密校验；`config.Merge` 对越界取值是**静默夹回**。上限检查落在新绑定 `SaveAgentProfile` 上。旧的 `Desktop.SaveProfile`（B06 的 `features/profiles/ProfileEditor`，资源库页还在用）仍然可以让 `filesystem=unrestricted` / `shell=true` 落盘，然后被 Merge 静默收紧。建议后续在领域层收口（会动 `internal/profile/**`，本轮不做）。
3. **删除的并发边界。** 删除是"先 CAS 占住这一版（原 body 写回，revision+1）再删文件"：并发写者的旧 revision 从此冲突；若删文件失败，文档停在 revision+1，数据不丢，只报失败。跨进程没有 OS 文件锁（与 `config.Store` 已登记的同一限制）。删除也不做级联：已存在的 run/会话各自持有配置快照，本轮不新增"删除即中断"的语义。
4. **网络权限在 V1 恒为 false。** `effectivePermissions` 硬编码 `Network:false`，所以 profile 的 `network=true` 永远被拒（上限来源 `system`）。这是诚实的结果（否则开关只是摆设），但在系统策略支持之前，界面上的"允许联网"会一直是禁用状态。
5. **来源覆盖面。** 只有 `modelRef` / `skillIDs` / `mcpIDs` / 三个 `permissions.*` 有来源；`name`、`memoryPolicy` 没有来源，因为 `Merge` 的 Provenance 不跟踪它们（它们不是资源或权限字段）。
6. **验收形态。** UI07 是 NATIVE + external oracle：需要真实 Wails 构建下的真机观察（截图/DB 对齐）才能判 PASS。本轮只在单元/组件层取证，没有生成 native 证据，因此案例状态是 **BLOCKED**，不是 PASS，也没有用前端测试冒充。

## 7. 未触碰的东西

`internal/profile/**`、`internal/config/**`、`internal/contracts/**`、`schemas/**`、`tests/acceptance/**`、`docs/**`、`internal/run|session/**`、`frontend/src/features/profiles/**`（B06 编辑器保持原样）、其它 feature。`git status --short` 在每个提交前均为干净。
