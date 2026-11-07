# B04 报告 —— observe / refs / actions on the real Windows WebView2 + BR07–BR10 native runners

- 工作树：`D:\project\anvil-wt\brnative`（分支 `feat/brnative`）
- 提交：`75a2280`（B04 实现 + 测试 + runner 条目），本报告单独一个提交
- 结论：**BR07.native 在 win-x64/desktop 上 PASS**（真实 WebView2 156.0.4314.58）；
  BR08/BR09/BR10 的用例在 win-x64 上全部通过并有 artifacts，但它们在索引里属于 **G2**，
  而 `runNative` 目前只选 G1 必需位 → 这三条**条目已落地、门禁尚未计入**，
  状态记 **部分**（不是 PASS）。其它平台仍未覆盖。

---

## 1. 快照 / 引用 / 动作契约（B04 实际实现）

### 1.1 快照

`Observe(tab, ObserveOptions{MaxElements, MaxTextChars, IncludeText})` 返回 `browser.Snapshot`：

| 字段 | 语义 |
|---|---|
| `id` | 快照 id（`snap-N`），一份快照是不可变的一帧 |
| `navigationId` | 文档身份（`nav-N`，每次导航完成变化；SPA 内 DOM 替换不变） |
| `viewport` | CSS px + `devicePixelRatio` |
| `elements[]` | `ref`/`frameId`/`role`/`name`/`tag`/`inputType`/`disabled`/`bounds`（CSS px） |
| `text` | `document.body.innerText`，按 Unicode 字符裁剪 |
| `unsupportedFrames[]` | 看不见的区域 + 具体原因（见 §3） |
| `Truncated`（Go 字段，不进 JSON） | 本次观察是否被限额裁剪过（见 §2） |

`name` 的取值顺序：`aria-label` → `placeholder` → `title` →（表单控件）值或 `name`/`id` →
（其余）`innerText`。**密码字段永不使用值**。

元素集合：`a[href],button,input,select,textarea,[role],[contenteditable=true],h1,h2,h3,p,strong,code,output,label`
（B01a 的可交互集合 + 文本承载元素；后者是为了让 `element_text` 后置条件有可引用的目标）。

### 1.2 引用（BR08 的核心）

- ref = 元素上的 `data-anvil-uid` 属性值 = `"<tab nonce>-e<seq>"`，由观察到该节点时**一次性**分配，
  后续观察复用；序号写在 `<html data-anvil-uid-seq>` 上，同一文档内单调递增。
- 因此 **ref 不是序号、不会被重新编号**：SPA 替换掉旧节点后，新节点拿到新的 uid，
  旧 ref 在页面上不再存在。
- `internal/browser.RefRegistry`（每 tab 一份）保存 `(snapshotID, navigationID) → ref → (Element, identity)`，
  `MaxRetainedSnapshots = 32`（超出淘汰最旧，被淘汰的快照返回 `STALE_SNAPSHOT`，不会“换成新的”）。
- 动作执行前的四道校验，任一失败都返回 `STALE_SNAPSHOT`（`*contracts.AppError`，可按 Code 分支）：
  1. `(snapshotID, navigationID)` 匹配当前文档（同 URL 重新导航会失败）；
  2. ref 在该快照的引用表里（不存在的 ref 不会被“就近解析”）；
  3. 页面上按 uid 找回的元素**身份一致**（`data-anvil-uid` 与快照记录相同）；
  4. 标签/`inputType` 与快照一致。
- 校验全部发生在派发任何输入之前：旧 ref 被拒绝时服务端 counter 恒为 0（证据见 §6）。

### 1.3 动作

| 动作 | 输入通道 | 后置校验 | 备注 |
|---|---|---|---|
| `click` | `Input.dispatchMouseEvent`（press+release，可信） | `elementFromPoint` 命中测试：落点不是目标/其后代 → `TARGET_OBSCURED`，**不派发** | 不靠“把坐标挪一点”绕过遮挡 |
| `fill`（input text/password/email…） | `Input.insertText`（可信） | 读回值比对（密码在页面侧比较，只回布尔） | 不一致 → `VERIFICATION_FAILED` |
| `fill`（textarea / contenteditable） | 同上 | 同上（contenteditable 读 `innerText`） | |
| `fill`（select） | DOM 赋值 `value` + `input`/`change` 事件 | 读回值与 option 匹配 | **不是可信输入**，返回路径名为 `dom-select` |
| `fill`（checkbox/radio） | DOM 赋值 `checked` + `input`/`change` | 读回勾选状态 | 非真/假词（`maybe`）→ `UNSUPPORTED` |
| `key` | `Input.dispatchKeyEvent` | 无内置条件（`Verified=false/unknown`） | 只接受单字符与 `Enter/Tab/Escape/Backspace/Delete/方向键/Home/End/PageUp/PageDown/Space` |
| `scroll` | `Input.dispatchMouseEvent` `mouseWheel`（视口中心） | 无内置条件 | |

- 后置条件只允许 `url_equals` / `element_text` / `field_value` / `element_present`（契约未改）。
- `Delivered` 只表示输入已交给引擎：没有后置条件时 `verified=false`、`status=unknown`。
- 后置条件观测失败 → `VerificationFailed` + 可读 `Error`；输入已发出但**核验通道**一直失败 →
  `browser.EscalateUnverifiable` 升级成 **`needs_human`**（不让“核验不了”既不算成功也不算失败）。
- 密码字段的 `field_value` 核验在**页面侧比较**，`detail` 只写“一致/不一致（值不回显）”。

## 2. 限额（BR09）

| 上限 | 常量 | 实测 | 触发后的行为 |
|---|---|---|---|
| 元素 300 | `MaxSnapshotElements` | 403 个可交互元素的页 → 恰好 300 条 | 截断并标 `Truncated` |
| 文本 40000 字符 | `MaxSnapshotTextChars` | 50000 字符页 → 恰好 40000 rune | 截断并标 `Truncated` |
| JSON 256 KiB | `MaxSnapshotJSONBytes` | 300 元素 + 40000 CJK → 219 682 字节 | 先砍文本（保底 1000 字符）再从尾部丢元素（保底 20 条），仍超限则 `INTERNAL_ERROR`（拒绝返回超限 JSON）；JSON 始终合法 |

裁剪在**登记引用之前**完成：被丢掉的元素不会留下可用的 ref。

**关于 `truncated` 的落点（需要 review 注意）**：`Snapshot.Truncated` 用 `json:"-"`。
`schemas/browser.schema.json` 是串行维护的跨边界契约，本任务被明确禁止改动，而
`TestSchemaMatchesGoTypes` 要求 Go 字段与 schema 逐字段一致——所以线上 JSON 里**没有**
`truncated` 字段。裁剪事实通过两条通道表达：Go 字段（B05 的 MCP 层可以直接读给模型）与
证据 artifact `limits.json`（逐项写明哪条上限被触发）。若要把它放进线上 JSON，
需要一次受控的 schema 变更（`schemas/browser.schema.json` + `contracts_test.go`），
建议在 B05 的 MCP 输出定型时一起做。

## 3. Unsupported 表面与原因（BR10）

| 表面 | 原因文案（`browser.Reason*`） | 判定方式 |
|---|---|---|
| cross-origin iframe | `cross-origin frame：同源策略禁止读取其 DOM…` | 反复读取 `iframe.contentDocument` 抛异常/null |
| same-origin iframe（未遍历） | `same-origin frame：B04 只观察顶层文档…（不是空页面）` | JS 能读到 `contentDocument` 但本轮不递归 |
| closed shadow root | `closed shadow root：attachShadow({mode:'closed'}) 后宿主脚本读不到内部 DOM…` | CDP `DOM.getDocument{pierce:true}` 里的 `shadowRootType === "closed"` |
| 探测通道不可用 | `无法确认 closed shadow root：DOM.getDocument(pierce) 探测通道不可用: <原因>` | CDP 调用失败时如实声明（不假装没有 shadow root） |

声明进 `Snapshot.UnsupportedFrames`（`frameId`/`url`/`reason`，reason 必填）。这三个表面里的元素
**不会**出现在 `elements[]`，也不存在任何“按坐标点过去”的后门：对它们的 ref 一律 `STALE_SNAPSHOT`，
服务端 counter 保持 0（cross-origin frame 内 counter=0、closed shadow 内 counter=0 均有断言）。

## 4. 截图

- 通道：CDP `Page.captureScreenshot`（`format=png`，先 `surface` 再 `renderer` 回退），不是整屏截屏。
- 元数据：`PixelWidth/Height` 取自 PNG 实际尺寸，`Scale = PixelWidth / CSSWidth`（`DerivedScale`），
  `NavigationID` 与快照对齐（坐标可回溯到某一次文档）。
- 4 MiB：超过时**重新截图**并在 `clip.scale` 上受控缩小，`scale` 随之更新
  （实测：1600×2400 随机像素页 → 原尺寸约 11 MB → 0.5 档 → 800×1200 / 2 610 616 字节）。
  所有档位都超限 → `UNSUPPORTED` 且**不返回任何被截断的图片**。
- 传输层：`ICoreWebView2` 的结果字符串读取上限 32 Mi 单元，超出时返回 `resultTooLargeError`
  （明确“结果被截断”），截图路径据此走受控缩小，其它路径当错误上报——不再出现“半截 JSON 被当结果”。

## 5. 原生 runner 条目（`internal/devtools/native.go`）

| case | target/display | 测试 | `observationSpec` artifacts |
|---|---|---|---|
| `BR07.native` | win-x64 / desktop | `TestObserveReturnsStableRefsAndHonoursLimits`, `TestFillAndClickReachTheFixtureServer` | `snapshots/br07-form.json`, `snapshots/br07-form-stable-refs.json`, `actions.jsonl`, `form-last.json`, `counters.json` |
| `BR08` | win-x64 / desktop | `TestStaleRefAfterNavigationIsRejected` | `stale-rejections.jsonl`, `snapshots/br08-spa-after-replace.json`, `counters.json` |
| `BR09` | win-x64 / desktop | `TestObserveReturnsStableRefsAndHonoursLimits`, `TestPasswordNeverLeaks`, `TestScreenshotIsCompleteAndBounded` | `limits.json`, `snapshots/br09-login.json`, `password-scan.json`, `screenshots/br09-screen.png`, `screenshot-sha256.json`, `screenshot-outcome.json` |
| `BR10` | win-x64 / desktop | `TestClosedShadowAndCrossOriginAreUnsupported`, `TestTrustedOnlyPostConditionFailure` | `snapshots/br10-cross-origin.json`, `snapshots/br10-closed-shadow.json`, `unsupported-surfaces.json`, `trusted-only.json`, `counters.json` |

- 全部 `Source: "filesystem"`；artifacts 由测试写进 `$ANVIL_OBSERVATION_DIR`，`observation.json`
  由 finalize 从这些文件**重新读出来算**（不拿内存变量充当观察值）。
- 两个 note：
  1. `BR07` 是复合 case，这里只能覆盖 `BR07.native`（G1）；`BR07.tool`（G3，经 MCP）属于 B05。
  2. `BR08/BR09/BR10` 在 `tests/acceptance/cases.json` 里是 **G2**，而 `RunNative` 只选 G1
     必需位。条目与产物都验证过（手工按 runner 的 env/artifact 规则跑过：三份 case 的
     artifacts + `observation.json` 齐全），但**门禁目前不会计入它们**；接入 G2 选择需要
     动 `internal/devtools/{native,integration}.go` 的选位逻辑，不在本任务授权范围。

## 6. BR07–BR10 覆盖度

| 行 | 覆盖 | 结论 |
|---|---|---|
| BR07.native | 真实 WebView2 上 form input/select/textarea/checkbox 观察+填写+点击；服务端 counter 恰好 +1；服务端收到的载荷 name/plan/note/confirm 全对 | **PASS（win-x64/desktop）**，见 `verify --suite=native` 输出 |
| BR07（复合） | 缺 `BR07.tool`（G3/MCP，B05） | 部分 |
| BR08 | 同 URL 重新导航、SPA 断开节点两种场景：旧 ref → `STALE_SNAPSHOT`，counter Δ=0；新 ref 指向新元素且可用 | 用例通过、artifacts 齐全；**门禁未计入（G2）** |
| BR09 | 密码值 0 次出现在快照/正文/标签/核验结论/事件日志（且先证明真的填进去了）；三条限额；截图完整 + ≤4 MiB + scale 正确 | 同上 |
| BR10 | cross-origin / closed shadow 明确 Unsupported 且内部 counter=0；trusted-only 后置条件不成立 → `Delivered=true, Verified=false, status=failed`，可满足条件则 `verified` | 同上 |

## 7. 红证据（`scratch/red/`，本地目录，未入库）

| 文件 | 探针 | 结果 |
|---|---|---|
| `scratch/red/b04-stale-ref-renumbering.txt` | 把 uid 换回 B01a 的“按序号编号”（`'e' + els.length`） | `TestStaleRefAfterNavigationIsRejected` 失败：`替换后的新节点复用了旧 ref "e1"：这正是「重新编号后误点」的入口` |
| `scratch/red/b04-password-leak.txt` | 关掉页面侧脱敏（`secrets` 不再收集） | `TestPasswordNeverLeaks` 失败：`密码字段的标签里出现了密码值: "fixture-pass"` |

两处探针用完即回退，回退后重跑为绿。`scratch/` 通过仓库的 `.git/info/exclude`
（worktree 与主工作树共享的那个文件，路径 `D:\project\my-agent-go\.git\info\exclude`）
加了一行 `scratch/` 来忽略，因此工作树在提交前保持 `git status --short` 干净。
**这处改动在仓库 git metadata 里、不在受版本控制的文件里**，如果父任务不接受，
删掉那一行并把 `scratch/red/` 挪出工作树即可（红证据只是本地留档，报告里已记录内容）。

## 8. 验证命令与结果

```
go test ./internal/browser/... ./tests/fixtures/browser -count=1
  → ok internal/browser / internal/browser/native / tests/fixtures/browser
go test ./tests/native -run 'TestNative' -count=1
  → ok 83.9s（B01a/B01b 全部 11 条仍通过）
go test ./tests/native -run 'TestObserveReturns|TestStaleRef|TestFillAndClick|TestPasswordNeverLeaks|TestScreenshotIsComplete|TestClosedShadowAndCrossOrigin|TestTrustedOnly' -count=1
  → ok 18.9s（B04 的 7 条）
go vet ./... ; gofmt -l internal/ tests/
  → 干净
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static
  → static 退出码: 0
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=native --target=win-x64 --display=desktop
  → BR00/BR01/BR04/BR05/BR06/BR07.native PASS；仍然缺 UI04.native（B07 的任务）；
    退出码 2（缺 runner = 配置缺失，不是通过）
```

BR06 的一个环境敏感性（不是 B04 引入的回归）：第一次跑 native 套件时 BR06 判 FAIL，
原因是**前面连续跑 B04 用例留下的 msedgewebview2 进程**让 BR06 的“起点干净”检查失败
（`命令退出 1：…ebview2 进程（起点不干净）`）。清掉按 `myagent-native-profile*` 匹配的
测试进程后，同一提交上重跑 6/6 可跑 case 全 PASS。也就是说：native 套件要求**从干净进程
状态开始**，父任务合并后重跑取证时请先确认没有残留。

补充：`verify --suite=native` 在干净工作树上生成证据
（`docs/evidence/runs/ecefcea/G1/win-x64/desktop/BR07.native/`），该目录被 gitignore，
所以没有进本次提交；门禁证据由父任务按合并后的干净树重跑生成。

## 9. 需要 B05 / B06 / 后续任务接手的事

1. **`truncated` 上线**：把 `Snapshot.Truncated` 变成 schema 字段需要同时改
   `schemas/browser.schema.json` 与 `internal/browser/contracts_test.go`（串行维护）。
   建议与 B05 的 MCP 输出定型一起做。
2. **G2 的 native 选择**：BR08–BR10 的条目已就绪，需要把 `RunNative`/`RunIntegration` 的
   选位从“只 G1”扩到按 case 的 phase 选，否则这三条永远拿不到门禁证据。
3. **多 tab / profile**：现在每 tab 一个 nonce，但 `CreateTab` 仍只允许一个 tab（B01a 限制）；
   B05/B06 做 tab 限额与 profile 时需要把 `refNonce` 挪进 per-tab 状态（当前在 host 上）。
4. **控制权代数**：`ControlGeneration` 恒为 0（B05 才实现），快照已经带上该字段与校验点
   （`RefTable.ControlGeneration`），接 B05 时只需填值并加比较。
5. **同源 iframe 内动作**：本轮明确 Unsupported；若要支持，需要在 `UnsupportedFrames` 之外
   引入 frame 级快照（契约变更）。
6. **可信性分层**：select/checkbox 的写入不是可信输入（DOM + 事件），证据里以
   `inputPath=dom-select/dom-checkable` 标注；B05 的审批文案不要把它写成“模拟真人输入”。

## 10. Concerns

- 本机是 Windows 10 22H2（build 19045），低于基线验收系统 Windows 11 24H2：
  这里的 win-x64 结果只是**开发证据**，正式 win-x64 结论要在 24H2 上重跑。
- win-arm64 无机器；macOS/Linux 宿主（B02/B03）未实现 → 六目标矩阵仍缺。
- `frontend/dist` 与 `frontend/node_modules` 在本工作树里是**从主工作树复制/联接的本地产物**
  （两者都被 gitignore），`go vet ./...` / `verify --suite=static` 需要它们才能过；
  它们没有进版本库，也没有改动任何前端源码。
- 未提交任何 `docs/` 证据：`verify --suite=native` 的产物落在 `docs/evidence/runs/`（gitignored），
  门禁证据应由父任务在合并后按干净工作树重跑生成。
