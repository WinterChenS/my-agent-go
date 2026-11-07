# B06 报告 —— Profile/持久登录、window.open 受控 tab、下载（BR15–BR17）

- 工作树：`D:\project\anvil-wt\b06`（分支 `feat/b06`，未碰 `D:\project\my-agent-go`）
- 基线 HEAD：`0a9a554`（B05 合并后的 G1/G2 欠账清单）
- 提交：`62655c5`（实现 + 测试 + 迁移 + runner 条目）、`c4b2aa8`（红证据）、
  `fdb7c93`（证据分阶段留档）、`4a1b18e`（BR 证据的任务归属）、`bc1477e`（证据 limitations）；本报告单独一个提交
- 结论（一句话）：**BR15、BR16、BR17 在服务边界上成立并在 win-x64/G2 产出外部观察证据**；
  真实 WebView2 的 `NewWindowRequested` / `DownloadStarting` 接线、真实 OAuth/SSO 登录流
  **未取证**，写进证据 `limitations`，不算通过。G2 门禁仍为 exit 2（缺的是别人的 case，不是这三条）。

---

## 1. 做了什么

### 1.1 `internal/browser/profiles.go`（BR15）

| 接口 | 行为 |
|---|---|
| `NewProfileStore(ProfileOptions{Root, DB, Now})` | 校验并创建受管 profile 根（0700）。**先拒绝**「用户个人浏览器配置目录」再建目录 |
| `ProfileForWorkspace(ctx, ws) (BrowserProfile, error)` | `BrowserProfile{ID, WorkspaceID, Path, Ephemeral}`；id/路径由 workspace id 哈希派生，重启后不变 |
| `Profiles(ctx)` | 列举（workspace 维度） |
| `ClearProfile(ctx, ws) error` | 递归删目录 → 再 `Stat` 确认不存在 → 删应用侧登录态行 → 记 `cleared_at`；任一步失败返回 `CLEANUP_FAILED` |
| `SaveCookies/Cookies/CookieHeader` | 应用侧登录态记录（`browser_session_cookies`），`CookieHeader` 供**非引擎 HTTP**（下载）使用，过期记录跳过 |

- 个人浏览器目录拒绝：构造时把 `LOCALAPPDATA/APPDATA/HOME/USERPROFILE` 推出的
  Chrome/Edge/Brave/Chromium/Vivaldi/Firefox/Opera user data 目录（Windows、macOS、Linux 三套写法）
  与目标根做「互相包含」判定，命中即 `UNAUTHORIZED`。
- 两处 cookie 存储的关系（写进代码注释，避免后来者以为是重复记账）：引擎自己的 cookie 库在 profile
  目录里（WebView2 的 user data folder），`browser_session_cookies` 是应用侧记录，两者由同一个
  `profile_id` 绑定，`ClearProfile` 同时清。

### 1.2 `internal/browser/popup.go`（BR16）

| 接口 | 行为 |
|---|---|
| `HandleNewWindow(ctx, exec, NewWindowRequest{ParentTab, URL, WindowFeatures})` | `window.open` → **同 session 的受控 tab**；走**同一个** `OpenTab` 路径 |
| `Popups(ctx, exec, parent)` / `PopupParent(ctx, exec, tab)` | 父子关系可查（面板收起子 tab / 证据核对） |
| 新时间线事件 `popup_opened` / `popup_denied` | 进 JSONL 证据；被拒的弹窗也有记录（否则「没创建实例」无从核对） |

- 只接受 `http(s)`；`javascript:` / `file:` / 空 URL 明确 `UNSUPPORTED` 且**不创建任何实例**。
- 限额在 `CreateTab` **之前**判定（复用 B05 的 `SessionTabs`，没有第二套口径）：第 5 个弹窗被
  `TAB_LIMIT_EXCEEDED` 拒绝，backend `CreateCalls`/`Instances` 前后相等。
- 继承父 tab 的 `owner` 与 `profileID`；跨 session 的 window.open（B 用 A 的父 tab）→ `UNAUTHORIZED`。
- 父 tab 在创建期间关闭 → 关掉新 tab 并撤销，不留孤儿窗口。
- `tabState` 新增 `profileID`/`popups`，`Service.popupParent` 记录反向关系；`CloseTab` 两侧一起清理。

### 1.3 `internal/browser/downloads.go`（BR17）

| 接口 | 行为 |
|---|---|
| `NewDownloadManager(DownloadOptions{Root, DB, Cookies, Artifacts, WorkspaceRoot, HTTPClient, MaxBytes, Now})` | 缺 DB/Artifacts/WorkspaceRoot 直接判配置错误（BR17 要求「保存即登记产物」） |
| `Start(ctx, exec, DownloadRequest{TabID, URL, SuggestedName})` | 抓取到**受管 staging**（`<root>/staging/<id>.part`，0600），状态 `awaiting_destination`，记 size/sha256 |
| `SaveDownload(ctx, exec, id, destination)` | 目标必须在工作区根内 → 核对 staging hash → 同目录临时文件 + rename → **重新读盘**核对 → `RegisterFile` 登记产物 → `saved` |
| `CancelDownload(ctx, exec, id)` | 删 staging → `canceled`；`saved` 之后不再允许「取消」（那已经是一个用户可见文件） |
| `Job/Jobs` | 按会话隔离（跨 session 一律 `UNAUTHORIZED`，不区分「不存在」与「不是你的」） |
| `SanitizeSuggestedName(raw)` | 去控制字符 → 统一分隔符取 basename → 去前导点 → 非法字符替换 → 去尾随点/空格 → Windows 设备名前缀 `_` → 长度上限 120（保留扩展名）→ 空则 `download` |

- 未选目标前只写受管 staging：用例用「工作区树 + 家目录树前后完全一致」证明，
  证据里同时留「刚下载完那一刻的 staging 树」。
- 两道独立 hash 核对（保存前核对 staging、写完后重新读盘核对最终文件），任一道都能抓住篡改；
  证据里把两道一起拆掉的探针（见 `scratch/red/b06-mutation-red.txt` probe 6）证明它们是承重的。
- 目标路径逐段查链接；越界（例如 `~/Downloads`）→ `UNAUTHORIZED`，且**一个字节都不写**。
- 产物登记失败 → 删掉最终文件并失败（不留「有文件但列不出来」的产物）。
- `ArtifactRegistrar` 是下载域自己的最小接口（`RegisterFile(ArtifactRequest) (ArtifactRef, error)`），
  由装配层适配到 `internal/artifact`（用例里的 `artifactBridge` 就是这十行），
  **`internal/browser` 不依赖 `internal/artifact`**，也没有给 artifact 加新 API。

### 1.4 迁移 `internal/storage/migrations/008_browser.sql`

三张表：`browser_profiles`、`browser_session_cookies`、`browser_downloads`。不引用其它迁移的表、无外键
（runner 按版本集合补跑，不能假设 001–007 都执行过）。

**编号偏离任务书（必须复核）**：任务书写 `002_browser.sql`，但 `002` 已被 `002_memory.sql` 占用，
而 `schema_migrations.version` 是主键、`applyAll` 只看「已应用版本集合」——再放一个 `version=2`
的脚本会在「002 已应用」的库上被**静默跳过**。因此用下一个空闲编号 `008`，理由写在脚本头部。

### 1.5 runner 条目（`internal/devtools/integration.go`）

| case | 命令 | `observationSpec` |
|---|---|---|
| BR15 | `go test ./internal/browser -run 'TestWorkspaceCookiesIsolatedAndPersisted\|TestProfileStoreRefusesPersonalBrowserRoots'` | `filesystem`：`profiles.json`、`cookies.json`、`preservation.json` |
| BR16 | `... -run 'TestPopupInheritsSessionAndTabLimit'` | `filesystem`：`popups.jsonl`、`tabs.json`、`counters-popups.json` |
| BR17 | `... -run 'TestDownloadRequiresDestination\|TestSuggestedNamesSanitized'` | `database`：`downloads.jsonl`、`download-files.json`、`artifact-row.json`、`no-writes-before-destination.json` |

另外两处 runner 增量（同文件）：`taskForCase` 补 `BR → B00-B07`（此前 BR 证据的 `taskId` 是
`unknown`）；`integrationCase` 新增 `Limitation` 并写进证据 `limitations` 字段（与 `nativeCase`
同一口径）。三条 case 的 limitation 明确写了「只覆盖服务边界」以及「真实 WebView2 回调 / OAuth 未取证」。

## 2. 证据（win-x64 / G2 / desktop，rev `bc1477e`，worktreeDirty=false）

> 证据根按「生成时的 HEAD」命名（`docs/evidence/runs/<rev>/…`，gitignore）。本报告是 docs-only 提交，
> 所以它自己会把 HEAD 再往前挪一次；父任务在合并后的干净树上重跑下面同一条命令即可得到以新 HEAD
> 命名的证据，内容与本节一致（代码在 `bc1477e` 之后没有变化）。

```
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2 --case=BR15,BR16,BR17
  → 证据根: docs\evidence\runs\bc1477e\G2\win-x64
  → [PASS] BR15 / [PASS] BR16 / [PASS] BR17；汇总: 可跑 3 / 必需 3，失败 0，阻塞 0；退出码 0
```

每条 case 的 `result.json` 都是 `status=PASS`、`taskId=B00-B07`、`sourceRevision=bc1477e`、
`environment={os=windows, version=10, build=19045, arch=amd64, display=desktop}`，
`oracle.json` 是 `kind=external-observation`（BR15/16 为 `filesystem`，BR17 为 `database`），
`observation.json` 由测试自己写出、由 runner 后封装（不是测试进程自证）。

事实抽样（从文件读回，不是断言变量）：

- `BR15/profiles.json` 三个阶段：`after-save`=A `[Network/Cookies]`、B `[]`；`after-restart` 同上；
  `after-clear`=A `[]`（目录被删后又被 `ProfileForWorkspace` 重建为空）、B 不变。
- `BR15/cookies.json`（第二句柄读库）：`after-save`/`after-restart` 各 A+B 两条；
  `after-clear` 只剩 B 一条。值只写 `valueSha256`，不写值本身。
- `BR15/preservation.json`：伪造的个人浏览器 profile 树 `Default/Cookies`（size 32，
  sha256 `8708ff67…`）在操作前后一致。
- `BR16/popups.jsonl`：6 次尝试逐条记 `createCallsBefore/After`、`instancesBefore/After`、
  `errorCode`；同 session 弹窗 `1→2`（成功），跨 session / `javascript:` / `file:` / 空 URL /
  第 5 个 tab 全部 `createCallsAfter == createCallsBefore`，限额那次 `errorCode=TAB_LIMIT_EXCEEDED`。
- `BR16/tabs.json`：`backendInstances=4`、`registeredTabs=4`（没有"服务之外创建的窗口"），
  时间线里 `popup_opened`/`popup_denied`/`tab_limit_exceeded` 齐全。
- `BR17/no-writes-before-destination.json`：`workspaceBefore=[]`、`homeBefore=[]`、
  `stagingAfterStart=[dl-….part size 28 sha fd6e017d…]`；收尾 `stagingAtEnd=[]`、
  `workspaceAtEnd=[downloads/report.csv size 28 sha fd6e017d…]`、`homeAtEnd=[]`。
- `BR17/download-files.json`：`report.csv` 存在且 sha256 = 服务端内容（测试自己算的
  `fd6e017d…`）；`cancelled.csv` / `tampered.csv` / `~/Downloads/outside.csv` 都不存在。
- `BR17/downloads.jsonl`（第二句柄）：`saved` 的作业带 `artifactId`；`canceled` 无最终文件；
  `failed` 的作业 `errorCode=VERIFICATION_FAILED`。
- `BR17/artifact-row.json`：`artifacts` 表里恰好一行，`sha256=fd6e017d…`、`size=28`、路径在工作区内。

门禁侧（同一证据根）：

```
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 gate --id=G2 --evidence=docs/evidence/runs
  → passed=false exitCode=2；必需证据位 69；deferred 255 条（ADR 0005 停用的五目标）
  → "[missing]" 清单里 **不再出现 BR15/BR16/BR17@win-x64**（其余缺失的是 BR08–BR14、CFG/CONN/DB/
    MEM/MOD/MCP/RES/SCH 等别的任务）
```

## 3. 红证据（`scratch/red/`，已入库）

- `b06-initial-red.txt`：先写测试、实现未落地 → 包编译失败（`undefined: browser.DownloadManager` 等）。
- `b06-mutation-red.txt`：9 个探针，每个只改 `internal/browser/{profiles,popup,downloads,service}.go`，
  跑 focused 用例后原样回退：

| 探针 | 探针内容 | 被哪条断言抓住 |
|---|---|---|
| 1 | `ClearProfile` 只删库里的行 | `目录还在: <nil>` |
| 2 | 不再拒绝个人浏览器 profile 作为根 | `把个人浏览器目录当 profile 根被接受了` |
| 3 | profile 路径每次启动都变 | `重启后 profile 身份变了` |
| 4 | tab 限额在 `CreateTab` 之后才判 | `第 5 个 tab 的 window.open 未被限额拒绝: scripted-tab-5` |
| 5 | 弹窗不继承 profile | `popup 没有继承父 tab 的 profile：CreateTab profile=""` |
| 6 | 保存时两道 hash 核对一起去掉 | `hash 不符时仍登记了产物` |
| 7 | 目标路径不再限定在工作区内 | `越界目标的错误码 "VERIFICATION_FAILED"，期望 UNAUTHORIZED` |
| 8 | 建议名原样返回 | 14 条 `SanitizeSuggestedName` 期望全部失败 |
| 9 | 取消时留着 staging | `取消后 staging 没有清理` |

文件末尾有「全部回退后复跑」的绿输出。探针 6 需要同时拆两道检查才变红（单拆一道会被另一道抓住），
这一点也写在探针脚本注释里，避免后来者以为「拆一道不红 = 那道没用」。

## 4. 验证命令与结果（提交后的干净树）

```
go test ./internal/browser/... ./internal/artifact -count=1      → ok browser / browser/native / artifact
go test ./internal/... -count=1                                  → 全绿
go vet ./...                                                     → 无输出（通过）
gofmt -l internal/ tests/                                        → 无输出
scripts/devtools.ps1 verify --suite=static                       → [PASS] gofmt / go vet / 前端 typecheck，退出码 0
scripts/devtools.ps1 verify --suite=unit                         → [PASS] go test ./internal/... / 前端测试，退出码 0
scripts/devtools.ps1 verify --suite=integration --target=win-x64 --gate=G2 --case=BR15,BR16,BR17
                                                                 → 3/3 PASS，退出码 0
```

一次性观察（非本任务引入）：并行跑两套 `go test ./internal/...` 时 `internal/runtime/pi` 的
`TestStdoutBackpressureDoesNotDeadlock` 偶发超时（21.66s）；单独复跑 PASS（3.38s），
`verify --suite=unit` 也 PASS。该包本任务未改动。

环境依赖（都被 gitignore，不入库）：`frontend/node_modules`（junction 到主工作树的安装）、
`frontend/dist`（copy，junction 无法 `go:embed`）、`.tools/pi/standalone/v1.5.0-win-x64`（真实 Pi，
否则 `internal/run`、`internal/runtime/pi` 的真实 Pi 用例会失败/阻塞）。

## 5. 偏离任务书 / 需要父任务确认

1. **迁移编号 008 而不是 002**（理由见 §1.4）。若坚持 002，需要先合并 `002_memory.sql` 的编号，
   否则第二个 `version=2` 脚本会被静默跳过——那是「迁移没执行却显示成功」的坑。
2. **`SaveDownload` 多一个 `exec` 参数**（`SaveDownload(ctx, exec, id, destination)`）：计划里的签名
   只有 `(ctx, id, destination)`，没有会话身份。少了它，任何会话只要猜到下载 id 就能把别人的字节
   写到工作区里。跨 session 拒绝与 B05 的 tab 归属用同一口径。
3. **下载产物登记用装配层适配器**：`internal/browser` 定义 `ArtifactRegistrar` + `ArtifactRequest`
   /`ArtifactRef`，由 `internal/app` 适配到 `internal/artifact`（本任务禁改 `internal/app`，
   生产接线还没做）。用例里用同一形状的 `artifactBridge` 走**真实** `artifact.Service` + 真实 SQLite。
4. **BR15–BR17 走 integration 套件而不是 native 套件**：与 B05 的 BR11–BR14 一致；三条 case 都是
   scope=NATIVE、phase=G2，integration runner 也按 target 本机执行并写同一套证据位。真实 WebView2
   回调接线不在这三条的覆盖里（见 §5 的 limitations 与 §6）。

## 6. Concerns / 后续

1. **真实 WebView2 未接**：B01 宿主目前只支持单 tab，也没有 `NewWindowRequested` /
   `DownloadStarting` 委托。`HandleNewWindow` / `DownloadManager` 是宿主该调的入口，但「浏览器里点
   `window.open`/下载链接会走到这里」**没有原生证据**。把这两条回调接进 `internal/browser/native`
   之后，建议用 `tests/native` 在真实页面上重跑一次（那时才能把 BR16/BR17 的 limitations 去掉）。
   依据：`B04-report.md` §9 第 3 条已记录「CreateTab 仍只允许一个 tab」。
2. **真实 OAuth/SSO 未测试**：本机没有 OAuth 客户端与连接器测试账号，BR15 只证明「profile 目录与
   应用侧登录态按 workspace 隔离/留存/清除」，不证明任何真实站点的登录态能跨重启保留。
3. **cookie 值落在应用侧 SQLite**（与 profile 目录同级的受管数据，0600/0700）。本机 v1 没有引入
   OS 级加密（Windows DPAPI）——需要的话属于 credentials 域（C02/SEC07）的受控变更，本轮不改。
   证据里只写 `valueSha256`，不写值。
4. **`browser_downloads` 的 `staged_path` 保留在库里但保存/取消后文件已删**：字段是审计定位信息，
   不代表文件还在；`final_path` 只有 `saved` 才有值。
5. **五目标仍 deferred**（ADR 0005）：BR15–BR17 的 win-arm64/mac/linux 证据位在 gate 报告里逐条
   `[deferred]`，不是通过。本机 Windows 10 22H2（build 19045）低于基线验收系统，本机结果只是开发证据。
6. **程序化上传明确未实现**：没有任何注入文件路径的入口，测试也没有任何「上传成功」的断言；
   file input 仍只能由用户在原生文件选择器里手动指定。
