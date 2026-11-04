# B00 浏览器契约、双 origin fixture、最小 Wails spike

日期：2026-11-04。宿主机：Windows 10 Pro 22H2（19045），Go 1.27.3，Wails v3.0.0，
WebView2 Runtime 156.0.4314.58。分支 `feat/v1-foundation`，工作区含未提交改动（由复核者提交）。

任务书：`docs/superpowers/plans/2026-11-03-native-browser.md` B00；基线 §4/§7；
门禁用例 BR00（scope HOST）。

## 1. 本轮结论

| 任务 | 判定 | 依据 |
|---|---|---|
| T1 契约 `internal/browser` + `schemas/browser.schema.json` | PASS（focused） | 16 条测试，含 schema↔Go 字段/required/类型/枚举/上限交叉校验；`go test` 退出 0 |
| T2 双 origin fixture `tests/fixtures/browser` | PASS（focused） | 10 条测试，oracle 全部读服务端 counter；trusted-only 负例实测 `trusted-only=0 / rejected=1` |
| T3 最小 Wails spike `cmd/browser-spike` | PASS（记录的是"初始失败"） | 真实窗口启动、截图两张、14 条探针（`Attach` 等 10 个占位调用）全部返回 `NOT_IMPLEMENTED`，verdict=AS_EXPECTED，退出码 0 |
| T4 证据与文档 | PASS | 本文件 + `docs/evidence/raw/b00-browser/` |

**这不是 BR00 或 G1 的通过证据。** 本机只有 win-x64，没有生成
`artifacts/evidence/<rev>/<gate>/<target>/<case>/result.json` 证据树，也没有跑
`cmd/devtools gate`。BR00 的行为断言在这里被 focused 测试覆盖，但门禁判定要由
F01 的 runner 用正式证据格式产出一遍。

右栏**仍然没有原生网页视图**。这是本轮的预期结果，不是待修 bug：B00 的交付物是
"契约 + 可重复站点 + 一段可复现的失败记录"，不是 WebView2 实现。

## 2. T1 契约

### 文件

| 文件 | 内容 |
|---|---|
| `internal/browser/types.go` | 基线 §7 常量、DTO、枚举、校验（`CheckLimits` / `Validate` / `CheckTabLimit` / `SessionTabs`）、错误码 |
| `internal/browser/backend.go` | `BrowserBackend` 接口、`TabOptions`/`ObserveOptions`、`UnimplementedBackend` |
| `internal/browser/surface.go` | `BrowserSurface` 接口、`NativeParent`、`UnimplementedSurface` |
| `internal/browser/contracts_test.go` | 契约、上限、不变量、schema 交叉校验 |
| `schemas/browser.schema.json` | 上述 DTO 的 JSON Schema（draft 2020-12，`$defs` + `anyOf`） |

`TabID` 用 `type TabID = contracts.TabID` 别名，不另立一套 tab 标识。

### 固定值与不变量

常量与基线 §7 一一对应，`TestBaselineLimits` 逐条锁死（改数字即失败）：
`MaxSnapshotElements=300`、`MaxSnapshotTextChars=40000`（按 Unicode 字符，不是字节）、
`MaxSnapshotJSONBytes=256KiB`、`MaxScreenshotBytes=4MiB`、`MaxTabsPerSession=4`、
`MaxTabsTotal=12`、`ActionTimeout=15s`。

三条可执行的不变量：

1. `Snapshot.CheckLimits()` 超限返回错误（元素数/字符数/序列化字节）。上游只能
   截断并如实标注，不能把超限数据当正常结果交出去。
2. `ActionResult.Validate()` 拒绝 `Verified=true` 而没有后置条件、或状态不是
   `verified` 的结果；没有后置条件时状态只能是 `unknown`。
   `DeliveredResult(nav)` 是唯一推荐的"已发出、结果未知"构造。
3. `ExpectedCondition.Validate()` 只接受
   `url_equals` / `element_text` / `field_value` / `element_present`；
   `evaluate` / `script` / `js` / `wait_for_function` 显式返回 `UNSUPPORTED`，
   不是静默降级成"没条件"。

### NativeParent 如何保证不进 JSON

```go
type NativeParent struct {
	handle any    // 未导出：外部包读不到也说不出
	label  string // 只用于日志，例如 "wails-main-window"
}
func (p NativeParent) MarshalJSON() ([]byte, error) {
	return nil, ErrNativeParentNotSerializable
}
```

字段全未导出 ⇒ 只能由 `NewNativeParent` 构造、由 native 适配层用 `Handle()` 取出。
`MarshalJSON` 返回错误 ⇒ 任何把 `NativeParent` 放进 DTO、事件、日志结构或模型上下文
的地方在序列化瞬间失败，而不是写出一串指针地址。`TestNativeParentNeverSerializes`
同时验证"直接序列化"和"嵌进结构体序列化"两条路径都失败，并断言 `String()` 不泄漏句柄值。

### schema 与 Go 的一致性怎么保证

`TestSchemaMatchesGoTypes` 用反射逐字段比对：字段名集合、`required`
（= 没有 `omitempty`/`omitzero` 的字段）、JSON 类型（`int`↔`integer`、`[]byte`↔base64
`string`、结构体↔`$ref`）、枚举取值。`TestSchemaCoversEveryExportedType` 再用
`go/parser` 扫本包源码：任何新增导出 struct/具名 string 类型，要么进 schema，
要么进测试里的 `nonDTOTypes`（每条必须写不放进 schema 的理由）。
`TestSchemaLimitsMatchConstants` 把 schema 里的 `maxItems`/`maxLength`/`x-maxBytes`/
`maximum` 绑到同一批 Go 常量上。

反向验证（证明不是空转）：把 schema 的 `Snapshot.navigationId` 改名成
`navigationID`，测试立刻失败并打印两侧字段列表，改动已还原。

## 3. T2 双 origin fixture

`tests/fixtures/browser`（包 `browserfixture`）+ 可运行入口
`go run ./tests/fixtures/browser/cmd/fixtured`。验证过这个入口：打印
`{"primary":…,"secondary":…}`，`GET /` 200，`GET /__whoami` 带外部 `Origin` 头回来
`sameOrigin=false`，`GET /__counters` 返回 `{}`（见
`raw/b00-browser/fixtured-smoke.log`），随后 `Stop-Process` 收尾，没有残留进程。

### 两个 origin

两个 `net.Listen("tcp","127.0.0.1:0")`，端口由内核分配，因此并行测试不会互撞。
端口每次运行都不同；下面的数字取自 `raw/b00-browser/fixture-tests.log` 那一次运行
（该日志是权威，本段只是摘录）：

```
origin A(primary)=http://127.0.0.1:52437
origin B(secondary)=http://127.0.0.1:52438
```

"确实不同"有三层证据：端口不同（且不为 0）、`/__whoami` 回显的 `Origin` 头与自身
origin 比较结果为 `sameOrigin=false`、页面 HTML 里注入的 `data-origin` 是各自 origin
（A 的首页甚至把 B 标成 `data-other`，但绝不自称属于 B）。

### counter 是唯一 oracle

所有写操作都落到该 origin 自己的内存计数器；两个 origin 的计数器互相独立。
测试只读 `Counter()/Counters()`，不读页面上的数字，也不读任何 toast。
页面上的数字同样来自服务端，只是给人看的。

页面覆盖：普通表单（text/select/textarea/checkbox/button/disabled）、SPA 节点替换、
同 URL 导航、cross-origin iframe、closed shadow root、trusted-only button、
登录 cookie、下载、`window.open`、慢加载（1–3s 可配，`ms` 被夹进区间）。

关键设计：**每个难点都有一个"错误实现会涨的计数器"**。

- SPA：旧按钮计 `spa-target`，替换后的新节点计 `spa-replaced`。旧 ref 正确失效时
  两个都不涨；"按位置重新解析"会涨 `spa-replaced`——一个计数器分不出这两种情况。
- closed shadow：`closed-shadow-action` 与 `closed-shadow-light` 分开。顶层脚本读不到
  shadow 内部，任何 `closed-shadow-action` 非零都说明有人用坐标之类的手段绕过了。
- cross-origin：frame 里的点击计数记在 B，外层按钮记在 A，"穿透"和"没穿透"可区分。

### 实测数字（`docs/evidence/raw/b00-browser/fixture-tests.log`）

```
提交前        A.form-submit=0  B.form-submit=0
提交一次后    A.form-submit=1  B.form-submit=0     ← 写 A 不动 B
缺 X-Event-Trusted: status=403  trusted-only=0  trusted-rejected=1
带 X-Event-Trusted=true: status=200  trusted-only=1  trusted-rejected=2
```

trusted-only 负例说明：页面侧闸门是 `event.isTrusted`（合成事件连请求都不发），
服务端侧再要求 `X-Event-Trusted: true` 证明。本轮没有浏览器，所以 Go 测试断言的是
**服务端闸门**：缺证明（或值是 `1` 这类伪证明）返回 403 且 `trusted-only` 保持 0。
页面侧的 `isTrusted` 行为断言留给 B01–B03 的原生用例——这一条已经在文档里写明，
没有把"静态存在 `isTrusted` 字样"当成行为通过。

另有 `TestEveryPageIsServedWithItsOwnOrigin`：12 个页面逐个 200 且响应体带自己的
origin，`/download` 的链接指向真实端点。它挡的是"页面链接 404，B01 却把夹具缺页
误读成原生视图打不开"。

其余断言：慢加载 `ms=1` 被夹到 1 秒（实测 ≥1s 才返回，并在真正返回时计数）、
登录正确凭据种 `fixture_session`（HttpOnly）且 `/private` 200 / 无 cookie 401、
错误凭据不种 cookie 不计数、下载响应带 `Content-Disposition: attachment`、
`window.open` 的目标页加载计 `popup-target`、`Close()` 后端口不可再访问（用例隔离）。

## 4. T3 最小 Wails spike

### 怎么启动

```powershell
go build -o .tmp\browser-spike.exe ./cmd/browser-spike
# 人工观察（窗口一直留着）：
.\.tmp\browser-spike.exe
# 自动化证据（跑完自退出，退出码可判定）：
$env:MYAGENT_SPIKE_AUTOQUIT_SECONDS = '16'
.\.tmp\browser-spike.exe
```

窗口是真实 Wails v3 窗口（`application.New` + `Window.NewWithOptions`），**URL 指向
spike 自己起的壳页面**（`http://127.0.0.1:<port>/`），不依赖前端构建、不引入 React。
壳页面里是：左侧占位 + 中间聊天占位/探针结果 + **右侧 BrowserSlot 占位（红色虚线框）** +
一个应用内 `<dialog>`。右侧没有 iframe、没有浮窗、没有截图流——就是空的。

spike 同时启动 T2 的 fixture（两个 origin），壳页面用 `fetch` 从两个 origin 读
`/__counters` 并显示，证明"真实 WebView 能连到 fixture"（截图里显示 `{}` 是因为 spike
没做任何写操作，而这正是 counter 的初始状态；读取失败会显示"读取失败"）。

### 初始失败原文（`docs/evidence/raw/b00-browser/spike-slot-empty.stdout.log`）

```
B00|slot|rev=1 slotId="browser-slot" left=722.0 top=37.0 width=432.0 height=734.0 visible=true zoom=1.00 dpr=1.00 viewport=1164x781
B00|probe|AS_EXPECTED|surface.Attach(parent)|got=NOT_IMPLEMENTED: browser surface 未实现（B00）：右侧没有原生子视图，Attach(parent=wails-main-window) 无法挂载；B00 只做契约：没有 WebView2/WKWebView/WebKitGTK 子视图
B00|probe|AS_EXPECTED|surface.Present(tab, slot)|got=NOT_IMPLEMENTED: …没有原生视图可以摆到 slot "browser-slot"…
B00|probe|AS_EXPECTED|surface.SetVisible(true)|got=NOT_IMPLEMENTED: …没有原生视图可显示/隐藏…
B00|probe|AS_EXPECTED|surface.Detach()|got=NOT_IMPLEMENTED: …没有挂载过的容器可拆卸…
B00|probe|AS_EXPECTED|backend.Capabilities()|got=navigate=false observe=false act=false screenshot=false unsupported=9 项
B00|probe|AS_EXPECTED|backend.CreateTab()|got=NOT_IMPLEMENTED: browser backend 未实现（B00）…
… Navigate / Observe / Act / Screenshot / CloseTab 同上
B00|probe|done|verdict=AS_EXPECTED
B00|summary|verdict=AS_EXPECTED
```

`AS_EXPECTED` 的含义是"该失败的地方确实明确失败了"。任何 `UNEXPECTED_SUCCESS`
（占位实现静默返回 nil）都会把 verdict 打成 `FAIL` 并让进程退出码为 1。
所以退出码 0 只表示"B00 的初始失败被如实记录"，**不表示右侧有网页**。

### 截图（证据目录 `docs/evidence/raw/b00-browser/`）

| 文件 | 内容 | 大小 | sha256 |
|---|---|---|---|
| `b00-spike-slot-empty.png` | 主窗口：左侧占位 / 中间探针结果 verdict=AS_EXPECTED / 右栏红框"这里没有原生网页视图" | 104484 B | `84c1a2de0b5c72dc8f5135b4a6a73f8228b3e05990cff5425cbd3d95eb7c729a` |
| `b00-spike-dialog.png` | 同上 + 应用内 dialog 打开（`MYAGENT_SPIKE_OPEN_DIALOG=1` 等价于脚本点一次按钮） | 98442 B | `bf3352afce355df059cbfc74e484fc97fd784e440fd4caebccd75949639e2bf5` |

截图为窗口级截图（PowerShell + `System.Drawing.CopyFromScreen`，按标题 `EnumWindows`
定位主窗口，非整屏）。两次运行都 `spikeExitCode=0`、`screenshotExitCode=0`。
`slot` 几何是 WebView 里真实量出来的（`getBoundingClientRect` + `devicePixelRatio`），
并经 `POST /slot` 回到 Go——B07 要的几何上报链路在本轮就通了。

本次运行的两个 fixture origin 是 `http://127.0.0.1:51804`（primary）与
`http://127.0.0.1:51805`（secondary），壳页面在 `http://127.0.0.1:51806`；端口每次启动
由内核重新分配，不作为固定地址。

环境（同一次运行输出）：`goos=windows goarch=amd64 webview2="156.0.4314.58"`，
Wails 构建信息 `Wails=v3.0.0 Compiler=go1.27.3 CGO_ENABLED=1`，
`Platform=Windows 10 Pro 2009 (Build: 19045)`。

## 5. 实际命令与退出码

| 命令 | 退出码 | 说明 |
|---|---|---|
| `go test ./internal/browser ./tests/fixtures/browser -count=1` | 0 | 16 + 10 条用例，日志 `raw/b00-browser/go-test.log` |
| `go test ./internal/browser -count=1 -v` | 0 | `raw/b00-browser/contract-tests.log` |
| `go test ./tests/fixtures/browser -count=1 -v` | 0 | `raw/b00-browser/fixture-tests.log`（含上面那组 counter 数字） |
| `go test -race ./tests/fixtures/browser -count=1` | 0 | `raw/b00-browser/fixture-tests-race.log` |
| `go vet ./internal/browser ./tests/fixtures/browser/... ./cmd/browser-spike` | 0 | 无输出 |
| `gofmt -l internal/browser tests/fixtures/browser cmd/browser-spike` | 0 | 无输出（已格式化） |
| `go build ./...` | 0 | 含 C04 工作区改动一起编译 |
| `go build -o .tmp\browser-spike.exe ./cmd/browser-spike` | 0 | |
| `.tmp\browser-spike.exe`（`MYAGENT_SPIKE_AUTOQUIT_SECONDS=16`） | 0 | verdict=AS_EXPECTED；退出码经 cmd 包装脚本保真采集 |

注意：GUI 的退出码用 `cmd /c ... & echo %ERRORLEVEL%` 包装采集。`Start-Process -PassThru`
的 `$p.ExitCode` 在这台机器上读出来是空值，不能用它判断"跑成功了"。

驱动与截图脚本放在 `.tmp\capture-window.ps1` 与 `.tmp\run-spike.ps1`（`.tmp/` 按仓库
约定不入库，需在本机重建；两者都是本机一次性工具，不是产品代码）。

## 6. 与计划 B00 的差异 / 缺项

1. **目录形状**：计划写 `tests/fixtures/browser/{server.go,pages/}`。实际是
   `server.go` + `pages.go`（页面作为 Go 常量模板）+ `cmd/fixtured`（可 `go run` 的入口）。
   原因是 fixture 要能被 `cmd/browser-spike` import；`package main` 不可 import。
   测试文件仍是计划要求的 `fixture_test.go`。
2. **契约多出几个小类型**：`Rect`（`Element.Bounds` 的载体）、`Viewport`、
   `Verification`、`UnsupportedFrame`、`UnsupportedCapability`、`TabOptions`、
   `ObserveOptions`。计划只列了 DTO 的名字，没给这些字段的载体；它们全部进了 schema
   并有交叉校验，不属于"自创字段名"。
3. **两个错误码在 `internal/browser` 本地定义**：`NOT_IMPLEMENTED`、
   `TAB_LIMIT_EXCEEDED`。`internal/contracts` 已有 `UNSUPPORTED`/`STALE_SNAPSHOT`/
   `VERIFICATION_FAILED`/`NEEDS_HUMAN`/`OUTCOME_UNKNOWN`/`CONTROL_UNCERTAIN`，本包直接复用；
   缺的这两个按所有权规则没有去改 `contracts`。B05 若要收进 contracts，字符串值必须不变。
4. **`SessionTabs` 记账器**（4/session、12/total）已提前实现，但没有真实 tab 可分配，
   只被单元测试驱动。B05 接线时以它为 tab 限额入口。
5. **没有生成门禁证据树**：没有 `result.json`/`gate --id=G1`。BR00 仍是聚焦测试层面的
   通过，不是 gate 通过。
6. **没有真浏览器行为断言**：本轮不运行任何 WebView，所以 cross-origin frame 读不到、
   closed shadow 拿不到、`isTrusted` 拒绝合成事件这些**行为**断言都留给 B01–B03。
   本轮只断言了"fixture 真的提供了这些难点"（静态）与"服务端闸门确实挡住非 trusted 输入"。

## 7. B01 需要什么（本轮不实现）

本轮实测得到的、B01 直接要用的事实：

1. **`WebviewWindow` 是顶层窗口，不是子视图。** `win.NativeWindow()` 在窗口显示后返回
   非 nil 句柄（本轮已验证），但它指的是整个顶层窗口（Windows 上窗口类
   `WailsWebviewWindow`）。右侧那块区域必须自建 **WebView2 子 HWND + controller**，
   挂在这个顶层窗口之下；不能用第二个顶层窗口冒充（R06 与设计文档 §4 都禁止）。
2. **UI 线程。** Wails v3.0.0 导出 `application.InvokeSync` / `InvokeAsync` /
   `InvokeSyncWithResult`，controller 的创建、bounds 更新和销毁必须经它调度到主线程；
   Go goroutine 只排队等结果。
3. **WebView2 的 user data folder 必须与主 WebView 分开。** 同一个 folder 不能被两个
   进程/环境同时占用，否则 `CreateCoreWebView2EnvironmentWithOptions` 直接失败。
   workspace 级 profile 的落点应在这里定（B06 会用）。
4. **坐标换算的量级已知。** 本次 1180×820 窗口里，页面 viewport 是 1164×781
   （客户区被边框占掉），右侧 slot 在 CSS px 下是 `left=722 top=37 432×734`，
   `devicePixelRatio=1`。B01 要做的是 CSS px → 客户区 px → 子 HWND px，
   并处理非 100% 缩放；本轮数据只能覆盖 100%。
5. **遮挡问题是真的。** 应用 dialog 是 Wails 主 WebView 里的 `<dialog>`（本轮截图里它
   位于页面层）。原生子视图一旦叠在上面，dialog 会被盖住——所以 B07 的
   `BrowserOcclusionCoordinator` 不是可选项，B01 要先把"隐藏/恢复"的原生接口做出来
   供它调用（本轮 `BrowserSurface.SetVisible` 的形状就是为此定的）。
6. **capabilities 必须实测。** `UnimplementedBackend.Capabilities()` 现在全 false 且
   逐项给原因；B01 换成真实探测后，`trusted-only` 页面在 DOM 输入不满足后置条件时
   应返回 `VerificationFailed`/`NeedsHuman`，不能因为"CDP 点击成功了"就当 verified。

B01 的第一个失败用例建议就是本轮这条：把 `UnimplementedSurface` 换成 Windows host，
`NativeRightPaneWindows` 断言"同一主窗口右侧出现真实 WebView2，且窗口句柄的父子关系
可以在 `Win32Window` 层级里查到"——而不是断言截图里有个网页的样子。

## 8. 现场环境备注（与 `AGENTS.md` §4 的差异）

- `gcc` 实际存在：`mingw64\bin\gcc.exe`（MinGW-W64 x86_64-ucrt-posix-seh, 16.2.0），
  `CGO_ENABLED=1`，Wails 构建与 `go test -race` 都成功。`AGENTS.md` 记的"无 gcc"已不成立。
- 本会话里 Bash 工具可用（`head`/`grep`/`sed` 均在），没有出现 §4 描述的缺失。
- `Start-Process -PassThru` 的 `$p.ExitCode` 读不到（返回空），退出码必须用 cmd 包装脚本采集。
- PowerShell 5.1 的 here-string 与 `@"..."@` 要求 **CRLF** 行尾；LF 写的 `.ps1`
  会把 C# 代码当 PowerShell 解析。截图脚本因此改为 CRLF。
