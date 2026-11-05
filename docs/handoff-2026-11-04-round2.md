# MyAgent V1 交接（2026-11-04 第二轮，接 `handoff-2026-11-04.md`）

从本机 `D:\project\my-agent-go` 接手。本轮把交接文档点名的偏移逐条收口，
并把原生浏览器从"只有 B00 契约"推进到"Windows 主窗口右栏真的能挂 WebView2"。
本轮**没有**推送、没有发版、没有改用户凭证；六目标门禁仍未闭合。

## 交接坐标

| 项 | 实测 |
|---|---|
| 分支 | `feat/v1-foundation` |
| HEAD | `9464b5f`（本文件与计划文档的提交在其后，只改文档） |
| 工作区 | 干净（除非你正在读的这一次提交还没落地） |
| 证据根 | `docs/evidence/runs/<rev>/<gate>/<target>[/<display>]/<case>/`（gitignore） |
| 本机能力 | win-x64 真机；WebView2 156.0.4314.58；MinGW gcc 16.2.0；无 mac/linux/ARM64 机器 |

## 本轮提交（按时间）

| commit | 内容 |
|---|---|
| `a329d81` | **F01 门禁按 case×target×display 聚合**：修「同一 case 单份 PASS 就满足全部目标」；拒绝重复证据、串用 gate、dirty 工作区证据；报告加按目标汇总 |
| `9dd1eca` | **C03 收口**：技能导入/编辑/启停 + 自有扩展；真实 Pi 1.5.0 正反两向测出项目/祖先技能被 `-na` 切断、同名自动发现副本胜出；ADR 0003；运行快照拆成 `managedSkills` + `skillAutoDiscovery` |
| `c77b950` | **F04 证据不可变**：F02/F04 测试不再改写 `docs/evidence/raw/*.json`；`verify` 检测"运行期工作区变脏"直接 exit 2 |
| `514f4e8` | G0 Windows 单目标证据文档（win-x64 24/24，五目标 BLOCKED） |
| `1be30ea` | go.sum 补 `gopkg.in/check.v1` 校验和（yaml.v3 转直接依赖后 `go list -m` 需要） |
| `cd0db64` | **GOV04**：机器索引 ↔ 验收矩阵一致性测试（矩阵里的 case 必须在索引里；owner/runner/scope 可判定） |
| `f9229cf` | **`verify --suite=native` 落地** + BR00 接入；`cmd/devtools` 自动定位仓库根 |
| `c49a93b` | **B00**：浏览器契约 + schema + 双 origin fixture + 最小 Wails spike（记录"右栏没有原生视图"，截图两张） |
| `fe603e0` | integration 证据按 gate 分目录 + `--gate` 参数 |
| `9058654` | **C04**：MCP 配置/诊断/统一工具目录（stdio + Streamable HTTP 真实 Pi 闭环；禁用后工具声明为空、计数不变） |
| `73d6f01` | **B01a**：Windows 原生 WebView2 子视图（同一主窗口右栏）+ ADR 0004 + 4 条真实原生用例 |
| `59b3aac` | native suite 接入 BR01 |
| `9464b5f` | 修 integration 证据根漏 gate 段（`TestEvidenceRootLayout` 钉住布局） |

## 各任务判定（win-x64 实测；其余五目标无机器）

| 任务 | 判定 | 证据 |
|---|---|---|
| F01 门禁 | **PASS（win-x64 维度）** | `a329d81`、`9464b5f`；12+ 条回归用例；`TestEvidenceRootLayout` |
| C03 | **PASS（win-x64）** | `docs/evidence/c03-skills.md` §7/§8；`docs/adr/0003`；真实 Pi 4 子测试 |
| C04 | **PASS（win-x64）** | `docs/evidence/c04-mcp.md`；`MCP01–MCP05` 真实 stdio/HTTP/禁用链路 |
| B00 | **PASS（聚焦）** | `docs/evidence/b00-browser-fixtures.md`；截图 + 14 条探针 |
| B01a | **PASS（核心链路）** | `docs/evidence/b01-native-webview2.md`；`tests/native` 4 条；右栏网页截图 |
| F06（Pi 安装器） | **延后** | ADR 0002；归 G4 |
| B01b / B02 / B03 / B04–B07 | **TODO / BLOCKED** | 见下 |

## 门禁实际结论（在 `9464b5f`、干净工作区）

```text
verify --suite=integration --target=win-x64        → 0（24/24 PASS）
gate --id=G0 --evidence=.../9464b5f/G0/win-x64     → 2
  通过 24 / 不通过 70
  win-x64 14/14 ✓   (HOST) 10/10 ✓
  win-arm64 / mac-x64 / mac-arm64 / linux-x64 / linux-arm64   0/14

verify --suite=native --target=win-x64 --display=desktop → 2
  BR00 PASS、BR01 PASS；可跑 2/7
  缺 UI04.native、BR04、BR05、BR06、BR07.native
gate --id=G1 --evidence=.../9464b5f/G1/win-x64/desktop → 2
  通过 2 / 不通过 47（必需证据位 49）
```

**G0 不是实现失败，是六目标证据缺失。** 要闭合必须在这五个目标上各跑一次
`verify --suite=integration --target=<id>` 再合并证据树。历史 CI
（`docs/evidence/ci-verify-round3.md`）是更早源码的记录，不能当当前 revision 的证据。
推送/触发 CI 需要用户授权。

## 下一轮优先级

1. **B01b（Windows 原生宿主补全，G1 阻断项）**：150%/200% 缩放坐标误差 ≤2 CSS px、
   分栏拖动 20 轮不漂移、中文 IME/focus/clipboard/最小化恢复、创建销毁 50 轮 + RSS、
   多 tab（4/session、12/total）。补完后把 BR04/BR05/BR06 接进
   `internal/devtools/native.go` 的 `nativeCases()`（现在只接了 BR01）。
2. **B04（DOM 快照/引用/工具语义）**：把 B01a 的 `data-myagent-ref` 换成正式引用注册表；
   补 select/checkbox/scroll/key、STALE_SNAPSHOT、trusted-only 的
   `VerificationFailed`/`NeedsHuman`。它决定 BR07/BR10。
3. **B07（工作台集成）**：React slot 几何上报（B00 已通）→ `Present`；dialog 遮挡协调器
   （`BrowserOcclusionCoordinator`）——B01a 只证明了 `SetVisible` 有效，反向遮挡由它处理。
4. **B02/B03**：需要 macOS（Intel + Apple Silicon）与 Ubuntu（X11 + Wayland）机器。
   没有就记 BLOCKED，不要用交叉编译冒充。B03 的 Wails GTK 挂载方案可能需要 ADR。
5. **C05/C06/C07**：记忆（中文 FTS5 trigram）、聊天/审批 UI、产物/诊断。它们属于 G2/G3，
   但实现与单测可先做；不要再走"先做 UI 再补门禁"的老路。
6. **X01–X05**：E2E、打包、迁移、签名。G3 之前不打包正式包。

## 环境与踩坑（本轮新增，已验证）

| 事实 | 做法 |
|---|---|
| `Start-Process -PassThru` 的 `ExitCode` 读不到 | GUI 退出码用 `cmd //c "app & echo %ERRORLEVEL%"` |
| MSYS bash 会把 `cmd /c` 改写成 `C:/` | 用 `cmd //c`（AGENTS.md 已记录） |
| 本机有 MinGW gcc 16.2.0 | cgo 构建与 `-race` 可用；换机器仍需探测 |
| Wails `WebviewWindow` 是顶层窗口 | 右栏必须自建 WS_CHILD + WebView2 controller（ADR 0004） |
| 子窗口用 `SWP_NOZORDER` 会被主 WebView 盖住 | `Present` 用 `HWND_TOP`；窗口截图与 CDP 截图都要能拍到 |
| 窗口被遮挡时 CDP surface 截图会挂 | 8s 超时退 `fromSurface:false`，并发事件 |
| `edge.Chromium.Embed` 失败走 `log.Fatalf` | 拿不到错误值（进程退出 1），已在 ADR 0004 记为限制 |
| 测试写仓库证据会让工作区变脏 | `verify` 现在直接 exit 2；证据只落 `t.TempDir()` 或 `MYAGENT_EVIDENCE_DIR` |
| 证据与 revision 绑定 | 文档提交也会改 HEAD；要门禁结论就在最终 HEAD 重跑，不要手改证据 |
| `go mod tidy` 在有未提交改动时很危险 | 用 `go get <精确版本>` + `go build ./...`；tidy 只在干净树做 |

## 复现命令（Windows，仓库根）

```powershell
$env:Path = 'D:\Program Files\nodejs;' + $env:Path
go test ./... -count=1
npm.cmd --prefix frontend run typecheck
npm.cmd --prefix frontend test

# 真实 Pi（技能发现 / MCP）
$env:MYAGENT_PI_WORKER_INTEGRATION = '1'
go test ./internal/resources -run 'TestSkillDiscoveryLayersAgainstRealPi|TestRealPiStdioMCPDiscoveryAndCall|TestRealPiHTTPMCPDiscoveryAndCall|TestDisabledMCPToolCannotExecute' -count=1 -v
Remove-Item Env:MYAGENT_PI_WORKER_INTEGRATION

# 证据与门禁（用包装脚本保退出码）
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64
$rev = git rev-parse --short HEAD
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 gate --id=G0 --evidence="docs/evidence/runs/$rev/G0/win-x64"
powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=native --target=win-x64 --display=desktop

# 原生 spike（人工观察 / 自动退出）
go build -o .tmp\browser-spike.exe ./cmd/browser-spike
.\.tmp\browser-spike.exe -native
$env:MYAGENT_SPIKE_AUTOQUIT_SECONDS='75'; .\.tmp\browser-spike.exe -native
```

## 交接纪律（不变）

- `internal/contracts`、`schemas/`、`cmd/devtools` 的门禁判定串行维护；
- 每个任务：失败用例 → 最小实现 → focused 验证 → 原始证据 → 单独提交（提交信息带 Task ID）；
- 缺机器/凭证/签名/网络 → BLOCKED，不删目标、不改口径、不伪造证据；
- 历史证据不可变：新运行写新文件，`docs/evidence/raw/` 里已有文件不改写；
- 用户级技能同步不等于授权项目资源；`-na` 不能省（ADR 0003）。

---

## 追加：第三批（B01b + C05）与最终证据（2026-11-05）

### 新增提交

| commit | 内容 |
|---|---|
| `474562a` | 修 `--gate` 只改目录不改 case 列表（`TestIntegrationSelectsCasesByGate`） |
| `3d82992` | gofmt 只查仓库内未被忽略的 Go 文件（`.tmp/` 探针不再让 static 变红） |
| `607f384` | **C05**：长期记忆 + 中文 FTS5 trigram + 受控注入（MEM01–05） |
| `2e1bcca` | integration 支持多步骤 case；接入 MEM01–05 / RES01–04（G2 可跑 14/50） |
| `fa96b64` | **B01b**：缩放 100/150/200%、分栏 20 轮、焦点/最小化/剪贴板/IME、50 轮（BR04–06） |
| `112fcae` | 收录 UI 设计交接包（`docs/ui/`，**输入材料，未实现**） |

### 最终证据（HEAD `112fcae`，干净工作区）

```text
G0  verify --suite=integration --target=win-x64 → 0（24/24）
    gate → 2；win-x64 14/14 ✓、HOST 10/10 ✓、其余五目标 0/14
G1  verify --suite=native --target=win-x64 --display=desktop → 2
    BR00/BR01/BR04/BR05/BR06 PASS（可跑 5/7）；缺 BR07.native、UI04.native
    gate → 2；必需证据位 49，通过 5
G2  verify --suite=integration --gate=G2 → 2
    SEC01/02/05/06/08、RES01–04、MEM01–05 PASS（可跑 14/50）
    gate → 2；必需证据位 240，通过 14
```

证据树在 `docs/evidence/runs/112fcae/{G0,G1,G2}/win-x64`（gitignore）。
文档提交会改变 HEAD；要新的门禁结论就在新 HEAD 重跑，别手改证据里的 revision。

### 仍未闭合（按优先级）

1. **G2 还差 36 条 runner**：SEC03.browser、SEC04.browser、SEC07、CFG01/02/03.resources/04、
   DB01–03、RUN01–07、MOD01–04、MCP01–05、BR08–17。多数已有单元测试，
   需要逐条核对"测试真的断言了该 case 的通过条件"再接线——不要按名字硬塞。
2. **B04**（DOM 引用注册表、select/checkbox/scroll/key、STALE_SNAPSHOT、trusted-only 语义）
   → 解开 BR07.native/BR10。
3. **B07**（工作台集成 + 遮挡协调器）→ 解开 UI04.native、BR07。
4. **B02/B03**：需要 macOS（Intel/AS）与 Ubuntu（X11+Wayland）机器；没有就 BLOCKED。
5. **C06/C07、X01–X05**：G3 之后才打包；F06 安装器仍按 ADR 0002 延后。
6. **六目标 G0**：需要推送触发 CI，属于用户授权事项。

### 新输入：UI 设计交接包（`docs/ui/Agent-客户端产品原型/`）

2026-11-05 出现的 OpenDesign 导出（9 屏 HTML + 视觉契约）。已原样入库保存，
**没有**被当前 React 前端采用。若要按它重构 UI：

- 需要单独立项：它替换的是 C01–C05 的视觉系统与 9 个界面，现有 117 条前端用例要重写；
- 先确认它与 R01（React + shadcn 为执行默认）的关系，若改用原型的技术路线（HTML/CSS/
  localStorage）则属于"更换前端栈"，按基线要先写 ADR；
- 建议顺序：先做视觉 token/布局骨架（不动业务接线）→ 再逐屏替换 → 每屏保留现有的
  空/加载/失败三态与真实数据源，不允许把硬编码 mock 当实现。
