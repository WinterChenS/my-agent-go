# P1 报告 — 产品自带 Pi 扩展随二进制分发

**Worktree**: `D:\project\anvil-wt\p1`（分支 `feat/p1`），基线提交 `4ed07b9`
**Task**: P1（打包后的 Anvil.exe 找不到 `pi-extensions/harness-policy.ts`，任何 run 都起不来）
**状态**: PASS（单元/静态/真实二进制端到端均通过）

## 1. 根因（与任务书一致）

`findProductExtension` 只找 `exe 旁 / 上一级 / cwd / 上一级` 的 `pi-extensions/`。打包目录里没有这个目录，
所以 `findPolicyExtension` 返回 `CONFIG_MISSING`，而它是工具调用执行前授权的唯一强制点 → 每一次 run 都拒绝启动。

## 2. 做了什么

| 位置 | 改动 |
|---|---|
| `assets.go`（新增，模块根 `package anvil`） | `//go:embed pi-extensions/*.ts`；只读访问器 `ProductExtension(name)` / `ProductExtensionNames()`。不接受带路径分隔符的名字。 |
| `internal/app/desktop.go` | ① `New()` 里把嵌入内容物化到 `<appRoot>/extensions/product/`；② 原子写（tmp → fsync → chmod 0644 → rename）；③ 逐字节比较：缺失或被改动才写，内容相同不写（mtime 稳定）；④ 解析顺序改为 **env 覆盖 → 物化副本（解析时也校正/重写）→ exe 旁/cwd 开发态**。 |
| `internal/app/product_extensions_test.go`（新增） | 7 条用例，见 §3。 |

### 与任务书的一处有意偏差（请复核）

任务书写 `<appRoot>/extensions/<name>`，实现落在 **`<appRoot>/extensions/product/<name>`**。

`<appRoot>/extensions` 不是空目录：它是**用户可以安装/启用/删除的第三方扩展目录**
（`resources.Layout.ExtensionsDir`，扩展页与 `Service.localExtensions()` 扫的就是这一层）。
把自带扩展直接放进去会有两个真实后果：

1. 扩展页会多出一条 id=`harness-policy` 的**「可关闭的第三方扩展」**（点下去什么也不会变，误导用户），
   澄清/附件扩展同样会以可删除的第三方身份出现；
2. `CompileExtensions` 会把它们当托管扩展再 `-e` 一次 → 澄清/附件扩展被同一进程加载两遍
   （策略扩展那一条靠 `resolveRuntimeResources` 的路径去重躲过了，可选扩展没有这层保护）。

放在子目录 `product/` 下：`localExtensions()` 按结构只认「单文件 .ts/.js」或「含 index.ts/index.js 的目录」，
`product/` 两者都不是，因此不会被当成用户扩展。语义仍是任务书要求的「`<appRoot>/extensions` 下、按内容哈希校验的物化副本」。
这条行为由 `TestMaterializedExtensionsAreNotUserExtensions` 从绑定层（`ListExtensions()`）钉住。

如果评审坚持字面路径，改动点只有 `productExtensionsDirFor()` 一个函数 + 那条测试，但上面的两个回归会立刻出现。

## 3. 测试（先红后绿）

`go test ./internal/app -run 'TestProductExtension|TestTampered|TestMaterializ|TestMissing' -count=1`

**红（实现前，用旧定位逻辑跑同一批用例）**：

```
--- FAIL: TestProductExtensionsMaterializeIntoAppRoot
    没有 pi-extensions 目录时也必须解析到策略扩展: CONFIG_MISSING: 找不到策略扩展 pi-extensions/harness-policy.ts；没有它工具调用没有执行前授权
--- FAIL: TestTamperedProductExtensionIsRewritten (同上 CONFIG_MISSING)
--- FAIL: TestMaterializedCopyBeatsDevLayout（解析到了 cwd 的开发态副本，而不是物化副本）
--- FAIL: TestMaterializationKeepsIdenticalFilesUntouched (CONFIG_MISSING)
--- FAIL: TestMissingOptionalExtensionIsReportedNotPassed（可选扩展缺失时仍被列出，实际 []）
```

（红态取证方法：把 `findProductExtension` 里的物化分支短路成旧行为后重跑，跑完立刻还原；
`git diff` 证明工作树里没有留下这个临时改动。）

**绿（当前实现）**：同一命令 `ok my-agent-go/internal/app 0.59s`。用例清单：

1. `TestProductExtensionsMaterializeIntoAppRoot` — 空 cwd + 临时 appRoot：三条扩展都物化、字节等于嵌入内容、策略扩展解析到物化副本；
2. `TestTamperedProductExtensionIsRewritten` — 篡改策略/澄清副本 → 解析时被重写回嵌入内容；
3. `TestProductExtensionEnvOverrideWins` — `ANVIL_POLICY_EXT` 压过物化副本；
4. `TestMaterializedCopyBeatsDevLayout` — 顺序：物化副本优先于 cwd 的 `pi-extensions/`；
5. `TestMaterializationKeepsIdenticalFilesUntouched` — 内容一致时 mtime 不变（用 `os.Chtimes` 把 mtime 拉旧 24h 再解析）；
6. `TestMissingEmbeddedPolicyExtensionIsConfigMissing` — 测试钩子 `productExtensionAsset` 摘掉策略扩展 → `CONFIG_MISSING`，文案含「策略扩展」与 `harness-policy.ts`；
7. `TestMissingOptionalExtensionIsReportedNotPassed` — 可选扩展缺失 → 不传给 Pi、`ProductExtensions()`/`clarificationOptions()`/`Status().Isolation` 都不宣称加载了它；
8. `TestMaterializedExtensionsAreNotUserExtensions` — 物化副本不出现在用户可关闭的第三方扩展列表里。

## 4. 验证命令与退出码

| 命令 | 结果 |
|---|---|
| `go test ./internal/app -count=1` | `ok my-agent-go/internal/app 23.442s`（0） |
| `go test ./... -count=1` | 全部 `ok`，无 FAIL（0） |
| `go vet ./...` | 无输出（0） |
| `gofmt -l assets.go internal/app/desktop.go internal/app/product_extensions_test.go` | 无输出（0） |
| `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static` | `[PASS] gofmt` / `[PASS] go vet ./...` / `[PASS] 前端 typecheck` / `static 退出码: 0`（0） |

本机隔离前置（都是 gitignore 的本地工具链，不进提交）：`.tools/pi/standalone/`（官方 Pi 1.5.0 包，从本机已有目录只读复制）、
`frontend/node_modules` + `frontend/dist`（`D:\Program Files\nodejs\npm.cmd --prefix frontend run build` 真实构建，vite 6.4.5）。
不补这些的话 `go test ./...` 在 `cmd/myagent`/`frontend`（缺 `dist`）与 `internal/piinstall`（缺官方包）上会 setup 失败——
与本 Task 无关，属于本机工具链缺失。

## 5. 真实二进制端到端（关键证据）

### 5.1 构建与摆放

```bash
cd D:/project/anvil-wt/p1
go build -o D:/tmp/p1-bin/Anvil.exe ./cmd/myagent      # 32,069,632 字节，只有这一个文件
ls D:/tmp/p1-bin                                       # Anvil.exe —— 没有任何 pi-extensions/
```

### 5.2 受控模型端点（fixture provider，不是真实云模型）

```bash
go run ./tests/fixtures/provider -addr 127.0.0.1:0 -addr-out D:/tmp/p1-run/addr.txt -record-dir D:/tmp/p1-run/rec
# fixture provider listening on http://127.0.0.1:55607
```

### 5.3 从临时目录启动（应用数据根、工作区、exe 全在仓库外）

```bash
cd D:/tmp/p1-bin
ANVIL_HOME=D:/tmp/p1-run/home ANVIL_PI_PATH=D:/project/anvil-wt/p1/.tools/pi/standalone/v1.5.0-win-x64/pi.exe \
ANVIL_BASE_URL=http://127.0.0.1:55607/v1 ANVIL_MODEL=fixture-model \
ANVIL_WORKSPACE=D:/tmp/p1-run/workspace ANVIL_NODE_PATH="D:/Program Files/nodejs/node.exe" \
ANVIL_UI_CDP_PORT=9411 ./Anvil.exe
```

启动后 `D:/tmp/p1-run/home/extensions/product/` 出现三条扩展，`sha256sum` 与仓库源逐一相同：

```
e7ea769f…  home/extensions/product/harness-policy.ts        仓库 pi-extensions/harness-policy.ts  e7ea769f…
c4c0966e…  home/extensions/product/anvil-clarification.ts   …  c4c0966e…
94c14dd2…  home/extensions/product/anvil-attachment.ts      …  94c14dd2…
```

同一应用根**重启一次**后 `stat` 的 mtime 三条全部不变（内容一致不重写，已在上表外单独 diff 验证）。

### 5.4 发一条消息（通过应用自身的 CDP 端口驱动真实 UI：信任工作区 → 新对话 → 填 textarea → 点「发送」）

界面最终状态（`document.body.innerText` 摘录）：

```
PI 状态 已完成
你    打包验证：请只回答一句「P1 已就绪」。
AGENT fixture 收到请求。这是脚本化回放，未请求任何真实模型。
本轮结论 · 已完成
```

`rec/request-001.json` 是 fixture provider 收到的真实请求体（证明这一轮真的走到了模型调用，而不是 UI 假状态）。

### 5.5 对照：同一布局 + 修复前二进制（HEAD `4ed07b9`）

```bash
git worktree add D:/project/anvil-wt/p0-before 4ed07b9
cd D:/project/anvil-wt/p0-before && go build -o D:/tmp/p0-bin/Anvil.exe ./cmd/myagent
# 同样的 env（ANVIL_HOME=D:/tmp/p0-run/home、同一个 fixture 端点、同样信任工作区、同样点「发送」）
```

```
这一轮出错了
CONFIG_MISSING: 找不到策略扩展 pi-extensions/harness-policy.ts；没有它工具调用没有执行前授权
```

修复前：一模一样的报错；修复后：同一包、同样布局、同一个操作 → 正常完成一轮。
（`p0-before` worktree 已 `git worktree remove --force`，`git worktree list` 确认无残留。）

## 6. 限制与遗留

1. **本机是 Windows 10 22H2（19045）**，不是基线验收环境 Win11 24H2；上面 5.4/5.5 是开发证据，不能当 win-x64 验收结论。
2. **没有真实模型**：端到端用的是 fixture provider（脚本化回放）。它证明的是「策略扩展被加载、run 能起、审批链路在」，
   不证明真实模型行为。
3. **Pi 用的是官方 1.5.0 独立包**（`ANVIL_PI_PATH` 显式指过去）；应用自己仍显示「未检测到 Pi」（F08 托管安装/探测是另一条路径，
   本 Task 未触碰）。这不是本改动引入的，但现场容易被误读。
4. **`<appRoot>/extensions/product/` 的偏差**见 §2，需要评审确认。
5. **解析入口是包级函数**（`findProductExtension`/`productExtensionPaths`），所以 `New()` 把物化目录记在包级变量里：
   同一进程内两个不同 appRoot 的 Desktop 会互相覆盖这个目录。生产是单实例，`internal/app` 的测试也没有并行，
   但这个假设由结构而非锁保证；要消除它得把解析改成 `Desktop` 方法（会牵动
   `session_api.go`/`model_api.go`/`skill_api.go`，超出本 Task 的 Touch 列表）。
6. 物化失败（只读 appRoot 等）不会阻断启动：`New()` 写 `LastError`（随后可能被调度器错误覆盖），
   真正不可恢复时以 run 入口的 `CONFIG_MISSING` 暴露；不会静默用一份缺失的策略扩展跑工具。
7. `go test ./tests/native` 在一次全量并行跑里失败过一次（`TestUI04NativeSlotOcclusionAndStopButton`，
   OS 命中测试被判「槽位中心不在原生子窗口树里」），单独重跑与随后两次全量重跑都通过 ——
   该用例依赖真实桌面会话的窗口命中，属于环境敏感，与 P1 无关（P1 不碰 UI 几何）。

## 7. 未改动的范围

`internal/devtools/**`、`tests/**`、`docs/**`、`schemas/**`、`frontend/**`、`internal/run/**`、`internal/runtime/pi/**` 均未修改
（`git status --short` 只有 `assets.go`、`internal/app/desktop.go`、`internal/app/product_extensions_test.go`）。
没有新增 `t.Skip`，没有弱化任何既有断言。
