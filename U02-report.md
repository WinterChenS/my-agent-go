# U02 报告：生产聊天流程与右侧上下文面板（前端）

状态：**REVIEW**（前端实现与前端证据完成；UI02/UI06 的验收 case 是 NATIVE + external oracle，本轮未取证 → 仍为 NOT_RUN，见 §7）

分支：`feat/u02`（worktree `D:/project/anvil-wt/u02`）
提交：`d85ed97`（事件归约）、`5a952ba`（聊天页/输入区/右栏 + 红证据）、本报告所在提交

验证命令与实际退出码（基线 Node `D:\Program Files\nodejs\node.exe` v24.16.0，PATH 首位是 v16 的微信 Node，必须前置）：

| 命令 | 退出码 | 结果 |
|---|---|---|
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend run typecheck` | 0 | `tsc --noEmit` 无输出 |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend test` | 0 | 27 files / **222 tests** passed |
| `node scripts/check-ui-contract.mjs` | 0 | PASS：catalog 之外文案增量 0，40 个 Go 错误码两种语言齐全 |
| `"D:\Program Files\nodejs\npm.cmd" --prefix frontend run build` | 0 | `vite build` 1648 modules（仅本地验证，未作为证据提交） |

原始日志：`scratch/u02-typecheck.log`、`scratch/u02-test.log`、`scratch/u02-copy.log`。

---

## 1. 归约器的事件规则（`frontend/src/runState.ts`）

不变式：**界面状态只能由后端事件推进；点击最多表示“请求已提交”。**

| 后端事件 | 归约规则 | 为什么 |
|---|---|---|
| `approval.request` | 追加一条 `submitted=false` 的审批项；同一 `approvalId` 重复投递只保留一条 | 两个按钮组里必有一个点了会失败 |
| `approval.resolved` | **唯一**移除路径；记录 `lastApprovalResolution{approvalId, choice, reason?}` | choice=deny 时后端带 `reason`（如 `run_settled`），界面要说清“为什么没了” |
| 本地点击（`approval_submitted`） | 只把该项标 `submitted=true`，按钮禁用，**卡片留下** | 规格 §5.3/§11：乐观 UI 只能表示已提交 |
| `clarification.requested` | 只在此处生成 `ClarificationItem{requestId, kind(select/confirm/input/editor), question, options}` | assistant 文本里的问句不是澄清，不许猜 |
| `clarification.resolved` | 移除对应澄清，记录 `lastClarificationResolution{outcome, value?, confirmed?, reason?}` | 超时收口（`reason=timeout`）也要给一句解释 |
| 本地点击（`clarification_submitted`） | 只动 `clarifications`，**不产生任何 approval/grant** | 回答内容问题 ≠ 授权工具 |
| `tool.start` | 建卡片 `outcome="pending"`，挂到当前助手消息并带 `runId` | toolCallId 只在单 run 内唯一 |
| `tool.end` | 按 `(runId, toolCallId)` 精确回填，`outcome=ok|error` | 完成顺序与发起顺序无关；跨 run 复用 id 是常态 |
| `run.state` 终局（completed/failed/cancelled/interrupted） | 置 `settled`（**唯一**入口）、关闭流式、未回填的工具标 `outcome="unknown"`、清空审批条 | 工具跑完/`message.done`/点击停止都不是收口；终局后没人会回答审批 |
| `run.error` | `errorCode` 与 `error="CODE: message"` 分开存 | 文案按 code 翻译，不解析 message |
| 任意事件 | 去重同时看 `eventId` 与每 run 的 `seq` 水位；`activeSessionId` 只被“本轮 runId 的事件”改写 | 重放可能改写 seq；重放旧事件不能把页面挪到别的会话 |

`stop_requested`（abort 已由后端接受）：`phase=stopping`、`stopRequested=true`、手上未回填的工具立刻转 `unknown`；此状态下 `user_message` 直接 no-op（不再派发）。真实回填的 `tool.end` 可以推翻 `unknown`（事实优先）。

测试（`runState.test.ts`，34 条）：approval 直到 `approval.resolved` 才消失；resolved-deny 带原因移除；`settled` 只由终局事件产生；停止后不再派发且未知不被写成成功；澄清回答后 `approvals.length===0` 且 `lastApprovalResolution===null`；同 `eventId`（即使 seq 被改写）不重复出消息；会话归属不被重放改写。

红证据：`scratch/red/u02-runstate-red.txt`（实现前 9 failed / 25 passed 的原始输出）。

## 2. 被替换的那一条测试

删除 `runState.test.ts` 中的 `it("本地点击后立即移除（不必等后端回事件）")`，替换为：

```ts
it("does not mark approval accepted until backend event arrives", ...)
```

理由：旧用例断言的正是规格 §5.3 明确禁止的行为（前端在点击后自行标记成功）。U02 计划 §Step 1 明确写明它不是受保护的回归测试，要求替换。配套新增 `it("已提交的审批仍留在原地、按钮禁用，但明确写着“等后端结论”")` 与 `it("后端已收口（移除）之后卡片才消失")`，把“点击 ≠ 完成”变成可执行断言。此外还修了 `App.tsx` 里同一处违规（原来 `decide()` 成功后直接 dispatch `approval_resolved`，现在只 dispatch `approval_submitted`）。

## 3. 澄清与审批的分离

- `ClarificationCard` 只由 `clarification.requested` 生成，与 `ApprovalCard` 是两棵子树、两个提交动作；
- 回答走 `Desktop.AnswerClarification(sessionID, requestID, {value|confirmed|cancelled})`，不经过 `ResolveApproval`；
- 归约器层断言：回答后 `approvals.length === 0`、`lastApprovalResolution === null`；组件层断言：澄清卡存在时页面上没有审批卡、也没有“允许一次”按钮；
- 相关文案明说“这是内容澄清，不是工具授权”（`chat.clarification.pendingHint`）。

## 4. 附件（ResolveAttachments 是唯一权威）

- `Composer` 收的是路径字符串（文件/目录），或从托管 Skill 列表里选的 `SKILL.md` 路径；
- 每次加入都调用 `Desktop.ResolveAttachments(sessionID, [{path, displayName}])`，整批重放（后端按内容寻址，重复解析幂等）；
- 界面分别显示 `items`（ID/大小/sha256 前缀）与 `rejected`（逐条 `code` + `message`）；一个坏文件不会让整批失败，也不会被静默丢弃；
- 发送时只把后端复核过的 `items[].id` 交给 `StartSessionRun`，路径字符串永不进入 run；
- 解析整体失败（`error`）时显示 `chat.composer.resolveFailed{code}`，且不把任何文件当成已接收。

## 5. 右栏四分区与抽屉行为（`ContextPanel.tsx`）

| 分区 | 数据来源 | 缺失时 |
|---|---|---|
| 浏览器 | 无（宿主通过 `browserSlot` 注入真实原生宿主节点） | 显示 `unavailable` + 原因码 `BROWSER_API_MISSING` + 文案 `chat.context.browserMissing` |
| 产物 | `ListRunArtifacts(runId)` → 每项 `canonicalPath` + 登记 `sha256` + 大小；`GetArtifact(id)` 取这次读到的 `observedSha256`（`verified` / `drifted` / `missing` 三态分开显示）；「回到来源轮次」用消息/工具卡上的 `data-run-id` 定位 | 列表报错显示错误码与原因；无产物显示“这一轮没有登记产物”（与“读不到”区分） |
| 引用 | 后端 `RunDetails` **没有** citations 字段 → 恒显 `unavailable` + `CITATIONS_UNSUPPORTED` | 同左 |
| 执行 | `RunDetails(runId)`：state、provider/model、事件数、usage（`availability=unavailable` 时显示不可用而不是 0）、预算 `toolCalls/maxToolCalls`、工具执行、审批、诊断、错误、`details.unavailable[]` 原样列出 | 请求报错显示错误码 |

抽屉：**只有一个**容器元素，宽窗是 `<aside>`、窄窗是 `fixed` 覆盖抽屉，二者仅 class/`data-mode`/`aria` 不同；分区树结构与顺序不变，关闭时用 `hidden`（隐藏）而不是卸载。测试用挂载计数 + DOM 节点身份双重断言（`ContextPanel.test.tsx`）：

```ts
expect(document.querySelector("[data-browser-probe]")).toBe(node); // 同一节点
expect(mounts.count).toBe(1);                                     // 只挂载过一次
```

红证据（变异检查）：把抽屉写成另一棵树后，两条用例立刻变红 → `scratch/red/u02-mutation-drawer-remount.txt`。

## 6. 导出与会话动作

- 导出三个格式都走 `Desktop.ExportSession(sessionId, format, target="")`，界面只显示后端写出的 `path`/`bytes`/`sha256`/`redactions`，前端不生成内容；失败显示 `chat.export.failed{code}`。
- 改名/置顶/归档/删除用 C06 稳定 ID + `expectedRevision`；`CONFIG_CONFLICT` 显示 `chat.sessionActions.conflict` + 冲突码 + “重新读取”（重新 `ListSessions` 后再操作）。不做乐观更新。
- 重试：错误横幅的“重试”重发最后一条用户消息；`Replay` 按钮用 `Desktop.Replay(runId, lastSeq)` 把事件送进同一个归约器（重复投递由去重挡住）。

## 7. 覆盖的验收行（以及没有覆盖的部分）

| Case | 状态 | 说明 |
|---|---|---|
| UI02 | **NOT_RUN**（本任务只做前端部分） | C06 侧流式/工具/审批/停止/重放的 Go 证据已存在；本任务补的是前端事件驱动与附件/导出调用，未产生 NATIVE 证据 |
| UI06 | **NOT_RUN** | 需要构建后 Wails 应用的真实会话与 fixture 副作用计数（NATIVE + external oracle）。本轮只有组件/归约测试，不能替代 |
| UI03/UI04（右栏 native 侧） | **BLOCKED** | B07 的浏览器绑定在后端尚不存在，见 §8 缺口 (a) |
| UI12/UI13（布局、无障碍） | **NOT_RUN** | 归 U07；本任务只保证 1024px 断点下的抽屉逻辑与语义 token |

明确不声称：G3 任何 case 已通过。

## 8. 后端缺口（需要的确切 DTO/字段；本轮未改 `internal/**`）

(a) **浏览器面板状态（B07）**：`internal/app` 没有任何 `Desktop.*` 浏览器方法（`internal/browser` 只有库，没有绑定），前端拿不到 tab/几何/接管状态。
需要（示例形状）：
```go
type BrowserTab struct { TabID, Title, URL string; Active bool; Owner string } // owner: agent|human
type BrowserState struct {
    SessionID string; RunID string
    Tabs []BrowserTab; ActiveTabID string
    Geometry struct{ X, Y, Width, Height int }
    ControlState string // agent|human|control_uncertain
    Unavailable []UnavailableSection // 与 diagnostics 同形
}
func (d *Desktop) BrowserState(sessionID string) BrowserStateResult        // {state, error?}
func (d *Desktop) SelectBrowserTab(sessionID, tabID string) BrowserStateResult
func (d *Desktop) BrowserGeometry(sessionID string) BrowserStateResult
```
外加事件 `browser.state` / `browser.control`（走现有 run 事件通道即可），前端才能把右栏浏览器分区从 `unavailable` 换成真实状态。

(b) **引用（citations）**：`diagnostics.RunDetails` 没有引用字段。
需要：`RunDetails.Citations []CitationRef`，`CitationRef{RunID, SessionID, Seq uint64, EventID, Kind string /*artifact|url|file*/, Title, URI, ArtifactID string, Snippet string}`；无法提供时在 `RunDetails.Unavailable` 里给出 `{section:"citations", reason}`（现在的实现只能由前端硬报 `CITATIONS_UNSUPPORTED`）。

(c) **按轮次的 Profile / 思考级别**：`Desktop.StartSessionRun(sessionID, message string, attachmentIDs []string)` 不接受 Profile 与 thinking level，而 `run.StartRunRequest` 已经有 `ProfileID` 字段。
需要下列之一：
```go
type SessionRunRequest struct {
    SessionID, Message string
    AttachmentIDs []string
    ProfileID string      // 空 = 沿用设置
    ThinkingLevel string  // 空 = 沿用 Profile/模型默认
}
func (d *Desktop) StartSessionRun(req SessionRunRequest) (contracts.RunSnapshot, error)
```
另外需要一个只读的“当前生效 Profile”：`func (d *Desktop) CurrentProfile() ProfileResult`（形状对齐 `CurrentModelResult`）。在拿到之前，输入区的 Profile 与思考级别只显示“未知（后端未提供）”，不做点了没用的下拉框。

(d) **上下文超限的稳定错误码**：`run.error` 目前只会出现 `INTERNAL_ERROR`/`CLEANUP_FAILED`/`PROVIDER_ERROR`/`COMPATIBILITY`（预算走 `BUDGET_EXCEEDED`），没有专门的上下文超限码。需要新增例如 `contracts.CodeContextLimitExceeded = "CONTEXT_LIMIT_EXCEEDED"`（并补两种语言的 `errors.CONTEXT_LIMIT_EXCEEDED`）；否则界面只能给通用排查提示，无法给出“压缩/清上下文后重试”的准确下一步。

(e) **Skill 附件的语义**：`session.AttachmentRequest` 只有 `{path, displayName}`，所以“附加一个 Skill”在 run 里等同“附上一个 SKILL.md 文件”，丢掉了 Skill 身份。
需要：`AttachmentRequest{Path, DisplayName, Kind string /*file|directory|skill*/, SkillID string}`（`AttachmentMeta` 同步带回 `Kind/SkillID`），或者一个独立的 `AttachSkill(sessionID, skillID)` 绑定。

(f) **命名与计划不一致（不影响功能，只需要文档对齐）**：计划写 `SendMessage`/`AbortRun`/`ReplayRun`，实际绑定是 `StartSessionRun`/`Abort`/`Replay`；前端调的是实际存在的名字。`SendMessage`（对已存在会话发一条消息而不新建 run）在后端没有对应入口。

## 9. 已知限制

1. 浏览器分区只有宿主注入点（`browserSlot`）与不可用状态；真机原生视图的挂载/接管属 B07，本轮没有可验证实现。
2. 引用分区恒为不可用（见 §8b）。
3. Profile/思考级别只显示“未知”，因为后端没有按轮次传入的入口（见 §8c）。
4. `ResolveAttachments` 需要会话已存在；未选工作区时会话创建失败，附件区会显示解析错误码（`CONFIG_MISSING` 等），不是“上传失败”。
5. 会话归属保护：绑定层只有一个活跃 run，页面按会话渲染；`activeSessionId` 与页面不一致时本页只显示“另一个会话正在运行”、不渲染那条 run 的审批/澄清（避免在看不见上下文的地方批准工具调用）。此保护依赖 `user_message{/sessionId}` 与事件里的 `sessionId`，若后端将来支持并发多 run，需要按 run 记录完整列表。
6. 所有测试都在 jsdom 中跑；原生 WebView2/真实 Pi 会话未参与本轮（UI02/UI06 仍是 NOT_RUN）。

## 10. 提交内容

```
d85ed97 U02: drive approvals, clarifications and settlement from backend events only
5a952ba U02: build the production chat page, composer and four-section context panel
<script> U02: record the chat workflow report, backend gaps and case states
```

改动范围：`frontend/src/features/chat/**`（新增）、`frontend/src/runState.ts` + `runState.test.ts`、`frontend/src/app/{App,shell}.tsx`、`frontend/src/app/pages/index.tsx`（仅把 ChatPage 转出到 features/chat）、`frontend/src/i18n/locales/{zh-CN,en-US}.json`（新增 `chat.*` 178 行，未改其它 key）。未触碰 `internal/**`、`tests/**`、`docs/**`、`schemas/**`、`frontend/src/features/{workbench,history,search}/**`、`frontend/src/components/**`。
