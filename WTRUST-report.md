# WTRUST 报告 — 工作区信任：就地面包屑授权、信任列表、选目录不等于执行、撤销语义

- 分支 / worktree：`feat/wtrust` @ `D:\project\anvil-wt\wtrust`
- 合同：`docs/specs/2026-11-08-workspace-trust.md`（只读，未改）
- Go 侧改动：**无**。前端用到的绑定（`ListWorkspaces` / `CurrentWorkspace` / `CreateWorkspace` /
  `SetWorkspaceTrust`）已全部存在，`internal/app/workspace_api.go` 未动。
- `internal/run/**`、`tests/**`、`docs/**`、`schemas/**`、其它 features：未动。

## 状态

| 项 | 状态 | 证据 |
|---|---|---|
| ① 就地面包屑授权（阻断卡主按钮，名字含绝对路径，点一次即授权，就绪自动重算） | PASS | `ChatReadiness.test.tsx`（组件测试，见下） |
| ② 设置 → 工作区：已信任与未信任都列出（名称/绝对路径/信任时间/信任·撤销），空态“还没有工作区” | PASS（信任时间口径受限，见 concerns） | `WorkspaceTrust.test.tsx` |
| ③ 选目录不等于执行：添加/选择目录不要求信任，只有 run 启动要求 | PASS | `WorkspaceTrust.test.tsx`「未信任状态下添加目录成功，且不会顺手信任它」 |
| ④ 撤销语义：正在跑的 run 不受影响，界面写清“当前运行不受影响，下一条消息需要重新信任” | PASS | `WorkspaceTrust.test.tsx`「撤销后状态翻转，并说明…」 |
| 真机（agent-desktop）复核 | NOT_RUN | 本轮只做组件级验证；未构建 Wails 产物做真机走查（见 concerns） |

## 做了什么

### ① 聊天页就地面包屑授权

- `features/chat/readiness.ts`：`ReadinessBlocker` 新增 `action`（`kind: "trust_workspace"`，
  带 `workspaceId` 与被信任的 `path`）。只有 `WORKSPACE_UNTRUSTED` 带这个动作——
  其它阻塞项不发明后端没有的绑定。判定层仍然只读、不写，安全不变量仍在后端。
- `features/chat/ReadinessPanel.tsx`：带 `action` 的条目渲染主按钮「信任此工作区」，
  `aria-label` = `chat.readiness.trustWorkspaceAria`（**含绝对路径**并写明“允许在该目录执行命令与写入文件”）；
  点击后 `data-readiness-trust` 按钮禁用、显示“正在信任…”，失败显示该条的 `{code} + catalog 文案`。
  原来的“去设置 · 权限（工作区）”降为次级入口（`data-readiness-jump` 保留）。
- `features/chat/ChatPage.tsx`：`onTrustWorkspace` 只转发 `shell.trustWorkspace(id, true)`；
  成功后壳层重读工作区事实 → `currentWorkspace` 变化 → 就绪自动重算 → 输入区可用（不刷新页面）。
  失败时壳层把结构化错误放进 `actionError`，输入区的 `sendError` 照实显示。
- 文案：`chat.readiness.codes.WORKSPACE_UNTRUSTED.next` 改成“信任只覆盖执行：点下面的
  「信任此工作区」即允许在该目录执行命令与写入文件……读取、新建会话、选目录都不需要信任”，
  并新增 `trustWorkspace` / `trustWorkspaceAria` / `trusting` / `trustFailed` 四条（中英各一份）。

### ② 设置 → 工作区（`features/settings/PermissionSettings.tsx`）

- 已信任与未信任**都列出**：名称 + 状态 + 绝对路径 +（已信任时的）信任时间 + 信任/撤销动作。
  动作名带路径（`trustAria` / `untrustAria`），两行同名字按钮不会混淆。
- 信任时间：一行的“信任时间”来自 `app/trustLog.ts`（界面**观察到**这次信任动作的时刻），
  没观察到的已信任条目显示 `trustedAtUnknown`（“本次界面未记录（该项在界面启动前已信任）”），
  不编一个时间。原因见 concerns：后端 `Workspace` 没有 `trustedAt`。
- 空态：`还没有工作区` + “选目录不需要信任，信任只在执行时要求”，不是加载失败的样子。

### ③ 选目录不等于执行

- 设置页工作区区新增“添加目录”（路径输入 + 添加）：只调 `shell.addWorkspace(path)`，
  **不碰信任**；失败把结构化原因留在界面上。后端 `CreateWorkspace` 本来就写 `Trusted=false`，
  信任门禁只在 `Start`（`assertWorkspaceReady` → `workspace.AssertRunnable`）和 schedule 上。
- 回归测试：未信任状态下添加目录成功、`addWorkspace` 调一次、`trustWorkspace` **零次调用**、
  新行是 `data-workspace-trusted="false"`。

### ④ 撤销语义

- 撤销后（且列表**真的翻面**后才）显示：`已撤销信任：当前运行不受影响，下一条消息需要重新信任。`
  撤销失败（列表仍是已信任）**不**显示这句话——失败的撤销不能看起来像成功。
- 后端语义本就如此（run 按受理时的 binding 执行；撤销只影响下一次 `AssertRunnable`），未改 Go。

### 顺带修的误导文案

`app/DetailPanel.tsx` 里未信任提示原本写“左栏的锁形按钮可以切换”——左栏并没有那把锁，
属于同一类“把用户赶去别处找不存在的东西”。改成指向聊天页的「信任此工作区」/ 设置 · 权限。

## 改了哪几条既有断言（原来断言“去设置是唯一路径”的）

1. `features/chat/ChatReadiness.test.tsx` → `工作区未信任：原因带目录与信任动作`
   改名为 `工作区未信任：就给地面包屑授权按钮，可访问名带绝对路径，设置是次级入口`。
   原来只断言 `[data-readiness-jump="WORKSPACE_UNTRUSTED"]` 文本含设置分区；
   现在断言**主按钮存在**、`aria-label` 精确等于 `t("chat.readiness.trustWorkspaceAria", { path })`
   且**包含绝对路径**、按钮文本是「信任此工作区」、风险文案含“执行命令”，并**额外**断言旧设置入口仍在
   （次级，不再是唯一出路）。
2. `features/settings/SettingsPage.test.tsx` → `工作区信任来自真实列表并可切换`
   撤销按钮的可访问名从 `t("settings.permissions.untrust")`（“取消信任”）改为
   `t("settings.permissions.untrustAria", { path: "D:/work" })`，与“动作名带路径”一致。
3. `features/chat/readiness.test.ts` → `全新状态…` 增补断言：`WORKSPACE_UNTRUSTED` 带
   `action = { kind: "trust_workspace", workspaceId, path }`，其余阻塞项 `action` 为 `undefined`。
4. 文案键：`settings.permissions.untrust` 由“取消信任”改为“撤销”（与“撤销语义”同一口径）。

新增用例（未删除任何用例、无 `skip`、无弱化断言）：
`ChatReadiness.test.tsx` 新增「未信任：发送被拦；点一次「信任此工作区」只调一次后端，就绪自动重算、
输入区随即可用」；新文件 `features/settings/WorkspaceTrust.test.tsx` 7 条（列表两态、信任时间口径、
空态、撤销翻面 + 说明、撤销失败不谎称、未信任下添加目录、添加失败原样报错）。

## 实际执行的验证（命令 + 结果）

```text
# typecheck（本机 PATH 上的 node 版本会导致 vitest 启动失败，一律显式用 Node 24.16.0 可执行文件）
D:\Program Files\nodejs\node.exe node_modules/typescript/bin/tsc --noEmit          -> exit 0
D:\Program Files\nodejs\node.exe node_modules/vitest/vitest.mjs run                -> exit 0
   Test Files 46 passed (46) / Tests 417 passed (417)
D:\Program Files\nodejs\node.exe scripts/check-ui-contract.mjs                      -> exit 0
   [PASS] UI 契约：catalog 之外的文案增量为 0，两种语言覆盖全部错误码
```

说明：`frontend/node_modules` 在本 worktree 里原本不存在，用
`npm ci --prefer-offline --ignore-scripts`（Node 24.16.0 的 npm）装的；`node_modules/` 已在
`.gitignore` 内，未进提交。`npm run test` 走 PATH 上的旧 node 会报
`node:events does not provide EventEmitterAsyncResource`，因此上面的证据都用显式 node 可执行文件跑。

Go 侧未改，故未跑 `go test ./internal/...`。

## Concerns

1. **后端没有“信任时间”这个字段**。`internal/workspace` 的 `Workspace` 只有 `createdAt`，
   `SetTrust` 不记录时刻。任务书把可改文件限定在 `frontend/**` + `internal/app/workspace_api.go`
   （“仅当绑定缺失”），所以**没有**去改域模型。当前实现：界面记“本次界面观察到这次信任动作的时刻”，
   界面上如实写明口径；界面启动前就已信任的条目显示“本次界面未记录”。
   要让“信任时间”成为持久事实，需要另开任务给 `internal/workspace.Workspace` 加 `trustedAt`
   （`SetTrust` 落库、列表透出、`Workspace` TS 类型同步、迁移旧数据）——这是真正的修法。
2. **没有真机（agent-desktop）走查**。本轮证据是组件级：按钮可访问名、一次点击、就绪重算、
   输入区可用、撤销说明、未信任下添加目录。规格里的真机验收表（未信任目录选中后历史可见、
   真机点击后 `trusted=true`）需要构建 Wails 产物再走一遍，本轮未做。
3. **添加目录仍是手输路径**。原生目录选择对话框还没接（`features/workspaces/Sidebar.tsx` 里的旧注释
   也这么写）。本轮只保证“添加不需要信任”，没有引入新的选择器。
4. **信任时间的时间来源是界面时钟**，不是后端时钟；跨窗口/多实例时两边看到的记录会不同
   （后端事实仍是可信的 `trusted` 布尔）。这与第 1 条同源。
5. `features/workspaces/Sidebar.tsx`、`features/workbench/WorkbenchView.tsx` 里仍有“去设置/先信任”
   的旧措辞与旧的信任开关；它们不在本任务 Touch 列表内，未改。若后续把左栏接回主界面，
   建议同批改成指向就地授权。
