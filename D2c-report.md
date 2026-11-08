# D2c — 页头搬进壳层顶栏（删掉第二条 52px 带子）

**任务**：把对话页的页头放进壳层 52px 顶栏里，删掉页面里那条 `.achat-head` 带子。
**Worktree**：`D:\project\anvil-wt\d2c`（分支 `feat/d2c`）。未触碰 `D:\project\my-agent-go`。
**Task**: D2c

## 状态

| 项 | 结论 | 证据 |
|---|---|---|
| chat 路由只有一个 52px 页头 | **PASS** | `header=1`（`anvil-shell-topbar`），`thread.top=52`（原型 52），两视口 |
| 旧带类名 `.achat-head` 消失 | **PASS** | `ChatPage.test.tsx`「旧的页头带不再存在」 |
| 全量前端测试 | **PASS** | 44 文件 / **405** 用例全绿（改前基线 403，新增 2 条 D2c 用例） |
| `tsc --noEmit` | **PASS** | 退出码 0 |
| `check-ui-contract.mjs` | **PASS** | 退出码 0，catalog 之外文案增量为 0 |
| `chat-diff.mjs`（几何 + 结构） | **PASS** | 1366×768 / 1440×900 各 **20/20** 项误差 **0px**；`failedRegions: []` |
| 真机（Wails / WebView2 / DPI） | **NOT_RUN** | 本任务只做网页侧；真机属 `tests/native` |

## 缺陷事实（改前实测，不是估算）

改前把同一份 fixture 跑一遍 `chat-diff.mjs`（`--out=.tmp/d2c-evidence/before`）：

```
[chat-diff] 1366x768: 19/20 项在 1px 内
  Δ threadTop: 原型 52 / app 104（52）
[chat-diff] 结构: header=2（anvil-shell-topbar + achat-head） head 钩子=1 thread.top=104 顶栏=52 → FAIL
```

原型 `chat.html` 只有**一个** `<header class="topbar">`（`.chat-head-main` 标题/副行 +
`.chat-head-actions` 徽标/语言/图标按钮），原型渲染出来 `thread.top = 52`。

## 做法

1. **壳层开插槽**（`frontend/src/app/topbarSlot.tsx` 新增）
   - `Layout` 在 `.anvil-shell-topbar` 里渲染 `<div class="anvil-topbar-slot" data-topbar-slot>`
     并把节点放进 `TopbarSlotProvider`（只包 `<main>` 里的路由，插槽节点仍在顶栏里）。
   - `useTopbarSlot()` 返回 `{ element, claim(token, claimed) }`。
     `claim` 带 token：路由切换时"新页面占用 → 旧页面清理"的顺序不会把占用抹掉。
     缺 Provider（页面在壳层外渲染）时返回空插槽而**不抛错**——但没有 element，
     于是页面不渲染页头，绝不退回"页内再长一条带子"。
2. **占用期间壳层让位**：`data-topbar-claimed` 同时挂在 `.anvil-shell` 与顶栏上；
   占用时不再渲染 crumb / 搜索 / 相位与平台状态（一条带子不能装两套页头）。
   唯一保留的是**已打开**的详情栏开关——那是壳层唯一能收起右栏的控件，藏起来等于关不掉。
3. **对话页把页头 portal 进去**（`ChatPage.tsx`）
   - `createPortal(head, topbar.element)`，页头容器由 `<header class="achat-head">` 改成
     `<div class="achat-topbar-head" data-chat-head>`（**不再是 header，也不再是带子**）。
   - 保留 `data-chat-head` / `data-session-title` / `data-chat-project-mark` 钩子。
   - 标题、状态徽标、副行、待澄清/待授权徽标、语言选择、导出、右栏开关、更多菜单全部保留。
   - 重命名/导出结果/动作错误那条 `.achat-head-notice` 改名 `.achat-notice`（它本来就不是页头）。
4. **几何**：`.achat-topbar-head` 只保留 `flex:1 / align-items:center / gap:12px / min-width:0`，
   **删掉** `min-height:52px`、`border-bottom`、`background`、`padding`——52px 与分隔线属于顶栏。
   插槽规则集中在新增的 `frontend/src/app/topbarSlot.css`（随插槽模块 import），
   不往 D1 的 `index.css` 里塞壳层之外的改动。
5. **卡片标题不再用 `<header>`**：`ReadinessPanel` / `BrowserApprovalCard` 的卡片标题行
   从 `<header>` 改成 `<div>`。它们不是文档级 banner，但会让"chat 路由只有一个 header"
   在"有阻塞项/有浏览器审批"的真实状态下不成立；不改文案、不改布局。

## 证据

### `chat-head-diff.json`（`.tmp/d2c-evidence/after/chat-head-diff.json`）

由 `chat-diff.mjs --head-diff-before=<改前目录>` 生成：改前数字是真跑出来的（当时的
build + 当时的组件），不是手写推演。

| 视口 | header 数（前→后） | `thread.top`（前→后） | 原型 | Δ（后−原型） | 顶栏高 |
|---|---|---|---|---|---|
| 1366×768 | 2 → **1** | 104 → **52** | 52 | **0** | 52 |
| 1440×900 | 2 → **1** | 104 → **52** | 52 | **0** | 52 |

`pass: true`；`headHookCount` 前后都是 1（页头是搬走，不是复制）。

### 几何回归（`chat-diff.json`，同目录）

- 1366×768：**20/20** 项误差 0px；1440×900：**20/20** 项误差 0px（容差 1px）。
- 新增两项判据：`threadTop`、`topbarHeight`（都进 `REGIONS`，与原型逐项对比），
  外加结构判据（`oneHeader` / `oneHeadHook` / `threadRightBelowTopbar`），任一不成立脚本非零退出。
- 截图：`app-chat-1366x768.png`、`app-chat-1440x900.png`（对话页现在只有一个头）。

## 测试

改动的用例（没有 `skip`，没有放宽阈值）：

| 文件 | 用例 | 说明 |
|---|---|---|
| `ChatPage.test.tsx` | 「chat 路由只有一个 header：就是壳层 52px 顶栏」 | 新增：`querySelectorAll("header").length === 1`、等于 `[data-shell-region="topbar"]`、`data-chat-head` 在其内、`data-topbar-claimed="true"`、页内无 header、令牌 `--anvil-topbar-h: 52px` 且被 `height:` 消费 |
| `ChatPage.test.tsx` | 「旧的页头带不再存在」 | 新增：`.achat-head` 为 null、`[data-chat-head]` 只有 1 个、页面区域 header 数为 0 |
| `ChatPage.test.tsx` | 「容器链：页头在壳层顶栏插槽里 → layout …」 | **原用例改口径**：原来断言 `page.querySelector("[data-chat-head]")`（页内页头），现在断言页头在顶栏插槽里且仍在布局之前 |
| `ChatPage.test.tsx` | `renderPage()` helper | 从"只渲染 ChatPage"改成"Shell + Layout + ChatPage"：不这样量不到整个路由的 header 数（单独渲染时插槽不存在，页头本来就不该出现） |
| `Layout.test.tsx` | 「品牌头、两组导航…」 | 追加：`[data-topbar-slot]` 存在、未占用时 `data-topbar-claimed="false"` 且壳层自己的标题照旧 |

其余 403 条用例原样通过（未改断言内容）。

## 命令与退出码

```text
# 前端依赖（worktree 里原本没有 node_modules）
D:\Program Files\nodejs\npm.cmd ci --prefer-offline        → 0（258 包）

# 全量前端测试（PATH 上首个 node 是 v16/微信开发者工具 node，必须显式用 v24）
"D:/Program Files/nodejs/node.exe" node_modules/vitest/vitest.mjs run
  Test Files 44 passed / Tests 405 passed                  → 0

"D:/Program Files/nodejs/node.exe" node_modules/typescript/bin/tsc --noEmit → 0
"D:/Program Files/nodejs/node.exe" scripts/check-ui-contract.mjs            → 0

# 几何 + 结构证据（构建产物 + Chromium；浏览器用 d2b worktree 的 .tools/pw-browsers）
"D:/Program Files/nodejs/node.exe" node_modules/vite/bin/vite.js build      → 0
PLAYWRIGHT_BROWSERS_PATH=<d2b>/.tools/pw-browsers node frontend/src/features/chat/chat-diff.mjs \
  --out=.tmp/d2c-evidence/after --head-diff-before=.tmp/d2c-evidence/before → 0
  [chat-diff] 1366x768: 20/20 项在 1px 内 … thread.top=52 顶栏=52 → PASS
  [chat-diff] 1440x900: 20/20 项在 1px 内 … thread.top=52 顶栏=52 → PASS
  [chat-head-diff] PASS：thread.top 104 → 52（原型 52）×2
```

## 改动文件

| 文件 | 改了什么 |
|---|---|
| `frontend/src/app/topbarSlot.tsx` | 新增：插槽上下文 + `useTopbarSlot()` |
| `frontend/src/app/topbarSlot.css` | 新增：空槽不产生盒子、占用时吃满宽度、占用时把第一行钉在 `--anvil-topbar-h` |
| `frontend/src/app/Layout.tsx` | 顶栏开 `[data-topbar-slot]`、占用时让出 crumb/搜索/状态区、`data-topbar-claimed` |
| `frontend/src/app/Layout.test.tsx` | 追加插槽结构断言 |
| `frontend/src/features/chat/ChatPage.tsx` | 页头 portal 进插槽、删掉 `<header class="achat-head">`、声明占用、`.achat-notice` |
| `frontend/src/features/chat/chat.css` | `.achat-head` → `.achat-topbar-head`（去掉 52px/边框/背景/内边距）、`.achat-head-notice` → `.achat-notice` |
| `frontend/src/features/chat/ChatPage.test.tsx` | `renderPage` 走壳层、D2b 容器链用例改口径、新增 2 条 D2c 用例 |
| `frontend/src/features/chat/ReadinessPanel.tsx`、`BrowserApprovalCard.tsx` | 卡片标题行 `<header>` → `<div>`（见「做法」第 5 条） |
| `frontend/src/features/chat/chat-diff.mjs` | 新增 `threadTop` / `topbarHeight` / header 计数测量、结构判据、`--head-diff-before` 合成 `chat-head-diff.json` |

证据目录 `.tmp/d2c-evidence/{before,after}`（`/.tmp/` 不入库，与 D1/D2b 同一约定）。

## 限制与发现（不是本任务范围内修的）

1. **壳层网格行高的既有缺陷（D1 遗留，非 D2c 引入）**：`.anvil-shell` 的两行都是 auto 行，
   `align-content: normal` 会把剩余空间在行间**平分**。对话页内容比视口短时，第一行会比
   52px 高 6.8px（1440×900 实测 `thread.top=58.8`），页面不紧贴顶栏。D2c 在
   `topbarSlot.css` 里对**占用顶栏的路由**把第一行钉成 `var(--anvil-topbar-h)`。
   同样的现象在**未占用顶栏的短页面**上更明显：1440×900 的 `/`（工作台）实测
   `grid-template-rows: 397.2px 502.8px`，即顶栏那一行占了 397px、内容从 397px 处开始
   （截图核对过）。修法就是同一行 CSS 全局化，但它属于外壳几何（D1 / `index.css`），
   超出 D2c 的 Touch 列表，留给后续任务决定。
2. 本机为 Windows 10 22H2，网页侧证据（构建产物 + Chromium）不能替代 win-x64 真机验收。
3. 窄窗（≤620px）下页头不再自己加 `padding: 6px 12px`：内边距现在只属于顶栏（20px）。
   原型在窄窗没有单独规定，若后续要 12px 需要改的是顶栏，而不是页头。
