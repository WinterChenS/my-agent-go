# D2b — 对话页按原型几何重建

**任务**：把对话页按 `docs/ui/Agent-客户端产品原型/chat.html` 的**真实几何**重做（上一轮只换了样式，没换骨架）。
**Worktree**：`D:\project\anvil-wt\d2b`（分支 `feat/d2b`）。未触碰 `D:\project\my-agent-go`。

## 状态

| 项 | 结论 |
|---|---|
| 容器链 / 消息节奏 / 输入区解剖 / 右栏四 tab / 窄屏抽屉 | **PASS**（网页侧证据） |
| 几何实测（`chat-diff.json`，1366×768 + 1440×900） | **PASS**，18/18 项在 **0px** 误差内（容差 1px） |
| 组件测试（骨架、a11y 名字、焦点顺序、禁用态、抽屉） | **PASS** |
| `npm run typecheck` / 全量前端测试 / `check-ui-contract.mjs` | **PASS** |
| 真机（Wails 宿主 / WebView2 / DPI 缩放） | **NOT_RUN**（本任务只做网页侧；真机几何属 `tests/native`） |

## 区域映射（原型 class → 产品组件 / 类名）

改动全部落在 `frontend/src/features/chat/**`；样式集中在新文件 `chat.css`（类名加 `achat-` 前缀避免与其它
feature 的通名冲突，数值逐字照抄原型）。

| 原型（chat.html / studio.css / pages.css） | 产品 |
|---|---|
| `.chat-page` | `ChatPage.tsx` → `.achat`（flex 列、`min-height:0`） |
| `.topbar` 里的 `.chat-head-main` / `.chat-title` / `.chat-subline` / `.chat-head-actions` | `.achat-head` / `.achat-title` / `.achat-subline` / `.achat-head-actions`（页头带，标题 + 副行 + 待办徽标 + 语言 + 导出 + 右栏开关 + 更多） |
| `.chat-layout` / `.chat-layout.rail-open` | `[data-chat-layout][data-rail="open"]` → `grid-template-columns: minmax(0,1fr) 400px` |
| `.thread-col` | `[data-chat-thread-col]` → `.achat-thread-col`（开栏时 `border-right`） |
| `.thread` | `[data-chat-thread]` → `.achat-thread`（唯一滚动区，`padding:22px 0 8px`） |
| `.thread-inner` | `[data-thread-inner]` → `.achat-thread-inner`（`max-width:760px; padding:0 26px; gap:26px`） |
| `.msg` / `.msg-user` / `.msg-user .bubble` / `.msg-meta` | `MessageList.tsx` → `.achat-msg[data-role]` / `.achat-bubble`（`9px 13px`、`r-md`、`13.5px`）/ `.achat-msg-meta` |
| `.agent-head` / `.agent-badge` / `.agent-name` / `.agent-model` | `.achat-agent-head` / `.achat-agent-badge`（22×22 r6）/ `.achat-agent-name` / `.achat-agent-model` |
| `.prose` | `.achat-prose`（13.5/1.75、`--fg-2`） |
| `.tool` / `.tool-row` / `.tool-ico` / `.tool-name` / `.tool-arg` / `.tool-body` | `ToolCallCard.tsx` → `.achat-tool` / `.achat-tool-row`（38px、`8px 12px`）/ …（参数预览把 JSON 前两个字段压成 `k=v · k=v`） |
| `.confirm` / `-head` / `-body` / `-desc` / `-code` / `-foot` | `ApprovalCard.tsx` → `.achat-confirm*`（警示头 + 参数代码块 + `拒绝/总是允许/允许本次`） |
| `.inline-thil` / `-mark` / `-kicker` / `-note` | `ClarificationCard.tsx` → `.achat-thil*`（强调色卡，与审批卡的形状刻意不同） |
| `.conversation-state`（错误语气） | `.achat-error-card`（`RunErrorBanner`） |
| `.conversation-state`（常规） | `.achat-state-row`（`SettledBanner`） |
| `.stream-status` / `.live-dot` | `.achat-stream-status` / `.achat-live-dot` |
| `.composer-wrap` | `Composer.tsx` → `.achat-composer-wrap`（`padding:10px 26px 14px` + 向上渐隐） |
| `.composer` / `textarea` / `.composer-bar` / `.ctl` / `.composer-plus` / `.send` / `.composer-foot` | `.achat-composer`（760 上限、r-lg）/ `textarea`（`13px 14px 4px`、2 行）/ `.achat-composer-bar` / `.achat-ctl` / `.achat-plus`（28×28 r-sm）/ `.achat-send`（30×30）/ `.achat-composer-foot` |
| `.composer-project-row` / `.current-project-trigger` | `.achat-project-row` / `.achat-project-trigger`（胶囊：文件夹图标 + 项目名） |
| `.composer-attachments` / `.composer-attachment` | `.achat-composer-attachments`（有附件才出现）/ `.achat-attachment` |
| `.attachment-drawer` | `.achat-attachment-drawer`（`+` 打开：路径输入 / 文件 / 目录 / Skill） |
| `.rail` / `.rail-head` / `.rail-tab` / `.rail-body` / `.rail-pane` / `.rail-foot` | `ContextPanel.tsx` → `.achat-rail*`（head 42px、tab 26px；四个分区**始终在 DOM**，切换只改 `data-active`） |
| `.rail-drawer-handle` / `.rail-scrim` | `.achat-rail-handle` / `[data-rail-scrim]`（≤1100px 抽屉） |
| `.btn` / `.btn-sm` / `.btn-primary` / `.iconbtn` / `.badge` / `.badge-warn` / `.kbd` | `.achat-btn*` / `.achat-iconbtn` / `.achat-badge*` / `.achat-kbd` |

## 几何实测（构建产物，`chat-diff.json`）

证据目录：`.tmp/d2b-evidence/`（与 D1 的 `.tmp/d1-evidence/` 同一约定，`/.tmp/` 不入库）

| region | 原型 | app（1366×768） | Δ | app（1440×900） | Δ |
|---|---|---|---|---|---|
| threadInnerMaxWidth | 760 | 760 | 0 | 760 | 0 |
| threadInnerPaddingLeft / Right | 26 / 26 | 26 / 26 | 0 | 26 / 26 | 0 |
| threadInnerGap | 26 | 26 | 0 | 26 | 0 |
| threadPaddingTop / Bottom | 22 / 8 | 22 / 8 | 0 | 22 / 8 | 0 |
| msgGap（`.msg` 内间距） | 8 | 8 | 0 | 8 | 0 |
| msgGapMeasured（相邻两条消息实测距离） | 26 | 26 | 0 | 26 | 0 |
| composerMaxWidth | 760 | 760 | 0 | 760 | 0 |
| composerWrapPaddingTop / Left / Bottom | 10 / 26 / 14 | 10 / 26 / 14 | 0 | 10 / 26 / 14 | 0 |
| railWidth | 400 | 400 | 0 | 400 | 0 |
| railHeadHeight | 42 | 42 | 0 | 42 | 0 |
| bubbleRadius | 8 | 8 | 0 | 8 | 0 |
| bubblePaddingTop / Left | 9 / 13 | 9 / 13 | 0 | 9 / 13 | 0 |
| bubbleFontSize | 13.5 | 13.5 | 0 | 13.5 | 0 |

附：两侧 `grid-template-columns` 都是 `730px 400px`；app 的 `thread-inner` 实测宽 729（=730−右侧 1px 分隔线），
`composer` 实测宽 677（=729−2×26），用户气泡右缘与内容区右缘偏差 0（靠右对齐成立）。

证据怎么来的（可复核）：

1. `npm run build` 出 `frontend/dist`，脚本用静态服务器起在 `127.0.0.1:4190`；
2. 原型导出起在 `:4191`；
3. app 侧把 Wails runtime 的 HTTP 传输（`POST /wails/runtime`）挡成本脚本内的**只读 fixture**，
   再用 `window._wails.dispatchWailsEvent` 推一段后端形状的 run 事件——量的是产品自己的组件与 CSS；
4. 两个视口各截 app 与原型图，并写 `chat-diff.json`（任一 region 误差 >1px 时脚本退出码 1）。

## 命令与结果

| 命令 | 退出码 | 结果 |
|---|---|---|
| `node ./node_modules/typescript/bin/tsc --noEmit` | 0 | 无输出 |
| `node ./node_modules/vitest/vitest.mjs run` | 0 | 44 files / **403 tests** passed（D2b 前 392 → +11） |
| `node scripts/check-ui-contract.mjs` | 0 | `[PASS] UI 契约：catalog 之外的文案增量为 0`（新增文案全部进 catalog，两语言各 +27 键） |
| `node ./node_modules/vite/bin/vite.js build` | 0 | `dist` 构建成功（612 kB JS / 66 kB CSS，体积告警为既有） |
| `node frontend/src/features/chat/chat-diff.mjs` | 0 | 1366×768 18/18、1440×900 18/18 在 1px 内（实测 Δ=0） |
| `playwright test layouts.spec.ts` | 0 | 90 passed（含 5 个视口 × chat 页：无横向滚动 / 无裁剪） |

新增/改写的测试（都守行为，不是守像素）：

- `ChatPage.test.tsx` 新增「ChatPage 骨架（D2b）」8 条：容器链层级、消息直接挂在 `thread-inner`、
  用户气泡 vs 助手 prose、待办徽标计数（为 0 不出现，点击滚到对应卡）、输入区解剖与焦点顺序、
  发送禁用态与原因、右栏四 tab 切换不重建分区、窄窗抽屉开合与遮罩。
- `Composer.test.tsx` 新增「Composer 解剖（D2b）」3 条：`+` 抽屉的 `aria-expanded` 与节点、
  附件行有附件才出现且移除后消失、脚注快捷键与平台修饰键一致。
- 既有 6 条用例因解剖变化改为**先打开菜单/抽屉**（导出、重命名、附件入口）：点击路径变了，断言一个字未减。

## Concerns

1. **页头多出一条 52px 带**（内容比原型低约 52px）。原型把聊天标题放在自家 `.topbar` 里；本产品那条 52px
   顶栏属于 D1 的壳层（`app/Layout.tsx`，几何被 D1 证据锁定），对话页只能把标题/副行/动作放在它下面一条独立带里。
   布局内部的数字全部与原型一致，差的只是"这一块在页面里从第几像素开始"。
2. **右栏浏览器分区在网页证据里是空的**（`available:false` / `BROWSER_API_MISSING`）。原生子窗口只有真机才有，
   所以 `app-chat-*.png` 里那一块是"槽位 + 不可用说明"，而原型里是浏览器框。`features/browser/**` 不在本任务
   Touch 列表内，未改其布局。
3. **模型 / 思考级别 / 权限不是下拉框**。原型是三个真实 `<select>`；产品没有"按轮次传 model/thinking"的后端入口，
   所以这里显示启动快照与权限事实（读不到写"未知"）。不做一个点了没用的下拉。
   权限摘要很长时一行省略（完整文案在 `title` 里）。
4. **窄屏断点 1100px**（任务书要求）而原型是 900px；`useNarrowLayout` 的查询同步从 1023px 改成 1100px，
   保证 CSS 与 JS 判定同一口径。601–1100px 区间内 `thread-inner` 内边距仍是 26px（原型在 ≤900px 收到 16px），
   本任务未要求，未自行发明。
5. **导出 / 重命名 / 置顶 / 归档 / 重放 / 删除进了菜单**（原型：导出对话框 + 「更多」菜单）。功能与 CAS 行为未变。
6. 证据是**网页侧**：不含 Wails 宿主、WebView2、DPI 缩放；真机几何仍需 `tests/native` 的门禁套件。
