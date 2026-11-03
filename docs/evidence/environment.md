# F00 环境证据 — Toolchain / Version / Build-Environment Freeze

- 任务：F00（工具链、版本与构建环境冻结）
- 日期：2026-11-03
- 唯一可用宿主机：`Windows 10 Pro 22H2 (Build 19045) / amd64 / Intel N100`，仅此一台。
- 模块名：`my-agent-go`，产品名：`MyAgent`（按基线 F00 明确要求，不猜远程组织/发布账户）。
- 所有命令均在本机实测，原始输出见 `raw/`（同目录下的 `*.log` / `*.txt`）。

## 0. 本轮小实现选择（一行说明）

为让 `go.sum` 合法生成且 `go mod tidy` 不删除尚未被产品代码 import 的钉版库，本轮新增 `tools/tools.go`（`//go:build tools` + blank import `modernc.org/sqlite`、`github.com/modelcontextprotocol/go-sdk/mcp`、`github.com/wailsapp/wails/v3/pkg/application`）；这是本轮临时钉依赖手段，后续 F01+ 正式 import 后可保留或移除该文件。

## 1. 环境事实表（命令 / 期望 / 实际 / 解析路径 / 结论）

| 项 | 期望 | 实际 | 实际解析路径 | 结论 |
|---|---|---|---|---|
| Go | 1.27.3, GOROOT `D:\Program Files\Go` | 1.27.3 windows/amd64 | `D:\Program Files\Go\bin\go.exe` | OK（已冻结） |
| Node（基线） | 24.16.0 | 24.16.0 | `D:\Program Files\nodejs\node.exe` | OK（基线） |
| Node（PATH 首顺位） | 应为基线 | **22.22.2** | `C:\Users\Administrator\.workbuddy\binaries\node\versions\22.22.2-3\node.exe` | **未达标**——默认 PATH 抢到 WorkBuddy 托管 v22，需用基线 node 显式调用 |
| npm | 11.13.0 | 11.13.0 | `D:\Program Files\nodejs\npm.cmd` | OK（一律用 npm.cmd，npm.ps1 被执行策略禁止） |
| Wails CLI | v3.0.0 | v3.0.0 | `D:\project\my-agent-go\.tools\bin\wails3.exe` | OK（装到 `.tools/bin`，非全局 GOPATH/bin） |
| Pi（PATH） | 1.5.0（基线认证） | **1.4.0** | `C:\Users\Administrator\AppData\Local\pi-node\current`（包 `@earendil-works/pi-coding-agent@1.4.0`） | **不匹配**——如实记 Not Ready，绝不标 Ready |
| WebView2 运行时 | 存在 | 156.0.4314.58 | `C:\Program Files (x86)\Microsoft\EdgeWebView\Application` | OK（PRESENT） |
| 原生编译器 gcc/cc/clang/mingw | — | 全部缺失 | — | 缺失；本轮以 `CGO_ENABLED=0` 构建，纯 Go + WebView2 可用 |
| CGO_ENABLED | 0 | 0 | — | OK |
| D 盘剩余 | — | ~47.9 GB | — | OK |
| GOPROXY | 可达 | 200 | `https://proxy.golang.org` | OK |
| npm registry | 可达 | 200 | `https://registry.npmjs.org`（官方源） | OK |
| sum.golang.org | 可达 | 200（`/lookup` 返回 371 字节记录） | `https://sum.golang.org` | OK（复核阶段确认可达，哈希已与 sumdb 对齐，见 §6） |

> 微信开发者工具 node 也在 PATH 第二顺位（v16 同类），正是 `ToolchainRejectsWechatNode` 要拦的对象；脚本通过校验 node 的**实际解析路径 + 版本**拦截。

## 2. 直接依赖锁定（versions.lock.json → directDependencies）

| name | version | source | hash (go.sum h1) | license |
|---|---|---|---|---|
| modernc.org/sqlite | v1.61.1 | proxy.golang.org/modernc.org/sqlite/@v/v1.61.1.zip | `h1:TBEE49wz7VlxS+qXRXMQiPUZYOXHJ4ku83daYv1fe04=` | BSD-3-Clause |
| github.com/modelcontextprotocol/go-sdk | v1.8.0 | proxy.golang.org/github.com/modelcontextprotocol/go-sdk/@v/v1.8.0.zip | `h1:KIvahhYqwtbeniWVPs3TcXEA7b8jEtwfBpOTAI+Urx4=` | Apache-2.0（正由 MIT 迁移中，见模块 LICENSE） |
| github.com/wailsapp/wails/v3 | v3.0.0 | proxy.golang.org/github.com/wailsapp/wails/v3/@v/v3.0.0.zip | `h1:lBe89kFu23bVz1dc1H9v0srkHQgWsy7BAKpH4sLQuDw=` | MIT |

- 版本选择：Wails 仅锁 `v3.0.0`（proxy 列表中存在，禁止 nightly/3.1.0）；go-sdk 锁最新稳定 `v1.8.0`；modernc.org/sqlite 锁 `v1.61.1`。
- **FTS5 trigram 已实测**：用独立临时模块 `D:\tmp\fts5check` 对 v1.61.1 建 `fts5(content, tokenize='trigram')` 并中文检索，返回 `TRIGRAM_OK rows=1`、`EXIT=0`（见 `raw/fts5check.log`）——满足基线 §3 硬要求。
- 未使用任何 `@latest` / nightly / 浮动依赖。

## 3. 平台矩阵（build/platforms.json）

状态分两栏，因为"能在该目标上跑 Go 代码"和"能在该目标上跑 GUI"是两件事，
混成一个 PASS 会把 GUI 的未知说成已知。

| targetId | goos/goarch | 构建/测试/竞态 | GUI 运行 | 说明 |
|---|---|---|---|---|
| win-x64 | windows/amd64 | **PASS** | BLOCKED | 本机即此环境 + CI `windows-2025`；OS 为 Win10（基线最低 Win11 24H2），打包工具未安装 |
| win-arm64 | windows/arm64 | **PASS**（race 目标级 BLOCKED） | BLOCKED | CI `windows-11-arm`；Go 工具链不支持 `windows/arm64` 的 `-race` |
| mac-x64 | darwin/amd64 | **PASS** | BLOCKED | CI `macos-15-intel`（`macos-13` 已退役） |
| mac-arm64 | darwin/arm64 | **PASS** | BLOCKED | CI `macos-15` |
| linux-x64 | linux/amd64 | **PASS** | BLOCKED | CI `ubuntu-24.04`；无 display server |
| linux-arm64 | linux/arm64 | **PASS** | BLOCKED | CI `ubuntu-24.04-arm`；无 display server |

> 构建/测试一栏的依据是 GitHub Actions 六个真实 runner（各自断言 `go env GOOS/GOARCH`
> 与矩阵一致，不一致直接 exit 2），逐目标的 `build → vet → test → race` 结果见
> `docs/evidence/ci-verify-round1.md` 与每次 run 的 job summary。
> **这不是"本地编译过"**：测试真的在该架构上执行，包括 Unix 进程组回收、
> Keychain / Secret Service 读写等平台特有分支。
>
> GUI 一栏仍 `BLOCKED`：runner 没有 display（Linux 无 X11/Wayland，macOS/Windows 无交互桌面），
> WebView2 / WKWebView / WebKitGTK 的真实窗口行为无法在 CI 上验证，
> 且本机 Win10 低于基线要求的 Win11 24H2。这一栏要等 C01（应用壳）落地后单独立项。

## 4. 验证命令与真实退出码

| 验证命令（节选） | 退出码 | 证据文件 |
|---|---|---|
| `go version` / `go env GOROOT GOPROXY CGO_ENABLED` | 0 | raw/env-probe.txt |
| `D:\Program Files\nodejs\node.exe -v`（基线 v24.16.0） | 0 | raw/env-probe.txt |
| `D:\Program Files\nodejs\npm.cmd -v`（11.13.0） | 0 | raw/env-probe.txt |
| `wails3 doctor`（Wails v3.0.0，WebView2 156.0.4314.58，CGO_ENABLED=0） | 0 | raw/wails-doctor.txt |
| `go build github.com/wailsapp/wails/v3/pkg/application`（CGO_ENABLED=0） | 0 | raw/wails-doctor.txt |
| `go mod tidy`（首次执行时本机 sumdb 请求曾返回 `unexpected EOF`，暂用 GOSUMDB=off） | 0 | raw/go-mod-tidy2.log |
| `go mod tidy`（**复核阶段**：`GOSUMDB` 恢复默认 sum.golang.org 后重跑） | 0（`go.sum` 与关闭 sumdb 时**零差异**，证明哈希与公共 sumdb 一致） | 复核运行，无改动 |
| `go mod verify` | 0（all modules verified） | raw/go-mod-verify.log |
| `npm.cmd --registry=https://registry.npmjs.org --prefix frontend install` | 0（131 包，0 漏洞） | raw/frontend-install2.log |
| `npm.cmd --prefix frontend run build`（vite v6.4.5，1580 模块，6.75s） | 0 | raw/frontend-build.log |
| `npm.cmd --registry=https://registry.npmjs.org --prefix frontend ci`（复现锁文件） | 0（131 包，0 漏洞） | raw/frontend-ci.log |

## 5. toolchain 检查脚本（scripts/check-toolchain.ps1 / .sh）

语义：退出 **0** = 环境满足基线；退出 **2** = 配置缺失 / 不满足。**必须校验命令的实际解析路径**，不能只用 `node -v`；且**不自行调用任意 npm**。

首个失败用例 `ToolchainRejectsWechatNode`：在子进程环境里把 PATH 首个 node 指向伪造的 v16（模拟微信开发者工具 node 抢先解析），脚本必须拒绝并打印实际路径。实现方式：构造 `build/fake-node/node.cmd`，仅对 `-v/--version` 返回 `v16.0.0`，其余委托给真 node（以免误伤 npm）；**不修改系统 PATH**，仅在子进程 env 构造。

实测（用 `powershell -ExecutionPolicy Bypass -File scripts/check-toolchain.ps1`，因本机 .ps1 被执行策略禁止，与 npm.ps1/pi.ps1 同理）：

- **正常环境**（PATH 前置 `D:\Program Files\nodejs`、`.tools\bin`、`D:\Program Files\Go\bin`）：go/node/npm/wails 全部 OK → **EXIT=0**。证据 raw/check-normal.log。
- **伪造 v16 环境**（PATH 首位置 `build/fake-node`，node 解析到伪 v16）：脚本拒绝并打印实际路径：
  `FAIL: node: resolved to '…\build\fake-node\node.cmd' (version 'v16.0.0'), expected v24.16.0` → **EXIT=2**。证据 raw/check-fakev16.log。

### 5.1 用 nvm 的真实多版本 node 复验（比伪造 shim 更强）

本机装有 nvm，`D:\Program Files\nvm` 下有 v13.13.0 / v16.20.0 / v18.0.0 / v22.0.0 / v24.0.0 / v24.16.0。
直接把某个版本的目录前置到 PATH，就能让脚本解析到**真实**的该版本 node，无需伪造：

| PATH 首位 | 实测 node / npm | 退出码 | 输出 |
|---|---|---|---|
| `nvm\v16.20.0` | v16.20.0 / 8.19.4 | **2** | `FAIL: node: resolved to 'd:\program files\nvm\v16.20.0\node.exe' (version 'v16.20.0'), expected v24.16.0` |
| `nvm\v22.0.0` | v22.0.0 / 10.5.1 | **2** | 同上形式，打印真实路径与版本 |
| `nvm\v24.16.0` | v24.16.0 / 11.13.0 | **0** | `OK: node … -> v24.16.0`、`OK: npm … -> 11.13.0` |
| 默认 PATH（首个 node 是 WorkBuddy v22.22.2） | v22.22.2 | **2** | 拒绝并打印 `…\.workbuddy\binaries\node\versions\22.22.2-3\node.exe` |

复现命令（不改系统 PATH，只在子进程环境里换 PATH）：

```powershell
$env:PATH = "D:\Program Files\nvm\v16.20.0;D:\project\my-agent-go\.tools\bin;" + $env:PATH
powershell -ExecutionPolicy Bypass -File scripts\check-toolchain.ps1   # → exit 2
```

### 5.2 复核中发现并修复的误判（真实缺陷）

首轮用 nvm 复验时，`nvm\v24.16.0` 也被判失败：

```text
FAIL: node: resolved to 'd:\program files\nvm\v24.16.0\node.exe' but expected baseline 'd:\program files\nodejs\node.exe'
FAIL: npm: resolved to '…\nvm\v24.16.0\npm.cmd' but expected it under '…\nodejs' (wrong node/npm pairing)
```

原因是 `D:\Program Files\nodejs` 是 nvm 管的符号链接，指向 `nvm\v24.16.0`；脚本原先只做
`GetFullPath` 字符串比较，同一个文件的两条路径被判成不同。这是**对合法环境的误拒**。

修复：新增 `Resolve-RealPath`，对路径的**每一段**解析符号链接/目录联接后再比较。
修复后上表的 v24.16.0 → exit 0，而 v16/v22 仍被版本检查拒绝（判定力没有被削弱）。

> 注意：默认开发者 PATH 下 node 实际解析到 WorkBuddy v22.22.2（npm 10.9.7），该脚本同样会判失败——这正是正确的门禁行为，提醒需把基线 node24 前置。

## 6. 未解决项与限制

1. **sumdb 校验已闭环，但过程有波折**：首次 `go mod tidy` 时 sum.golang.org 请求返回 `unexpected EOF`，遂以 `GOSUMDB=off` 生成 `go.sum`。复核阶段重新探测 sumdb 返回 200，恢复默认 `GOSUMDB` 重跑 `go mod tidy`，`go.sum` **零差异**——即原哈希与公共 sumdb 一致，`GOSUMDB=off` 未引入未经验证的依赖。阻断性解除；`versions.lock.json` 的 `sumDbNote` 已同步更正。
2. **Pi 版本会变，必须每次重测**：F00 记录时 PATH 上 Pi 为 1.4.0（≠ 基线 1.5.0，判 Not Ready）；
   同日 14:50 该安装被用户升级到 1.5.0（目录自带 `node.exe` v22.23.2）。
   结论以 `docs/evidence/pi-compatibility.md` 的最新实测为准，不缓存旧结论。
3. **npm 默认源损坏**：用户级 `.npmrc` 把 registry 指向 `registry.npmmirror.com`，首次安装出现 lightningcss tarball “corrupted”。已改用官方 `https://registry.npmjs.org` 重装（无损坏告警）。
4. **宿主机 OS 偏差**：本机 Windows 10 Pro 22H2，基线最低验收系统为 Windows 11 24H2；打包工具（NSIS / MakeAppx / SignTool / MSIX）均未安装，最终安装包暂不可产。
5. **GUI 层仍无运行环境**：构建与测试已由 CI 六目标真实 runner 覆盖（见 §3），
   但 runner 无 display，WebView2 / WKWebView / WebKitGTK 的真实窗口行为仍 BLOCKED，不伪造通过。
6. **原生编译器缺失**：无 gcc/cc/clang/mingw；本轮以 `CGO_ENABLED=0` 完成纯 Go + WebView2 宿主编译验证，未做 cgo 路径验证（当前不需要）。
7. **"Wails shell 可编译"只证到库级，未证到应用壳**：本任务实际验证的是 `go build github.com/wailsapp/wails/v3/pkg/application`（库包编译通过）+ `wails3 doctor`，**没有**构建一个真实的 Wails v3 应用壳（`application.New` + 嵌入前端资源）。ENV01 的"支持该目标构建"因此只到库级，应用壳编译需在 C01 落地时补证。

## 7. 交付文件清单（绝对路径）

- `D:\project\my-agent-go\.nvmrc`
- `D:\project\my-agent-go\go.mod`
- `D:\project\my-agent-go\go.sum`
- `D:\project\my-agent-go\tools\tools.go`
- `D:\project\my-agent-go\frontend\package.json`
- `D:\project\my-agent-go\frontend\package-lock.json`
- `D:\project\my-agent-go\frontend\`（vite/ts/react/tailwind 配置 + `src/` 最小界面）
- `D:\project\my-agent-go\build\versions.lock.json`
- `D:\project\my-agent-go\build\platforms.json`
- `D:\project\my-agent-go\scripts\check-toolchain.ps1`
- `D:\project\my-agent-go\scripts\check-toolchain.sh`
- `D:\project\my-agent-go\docs\evidence\environment.md`（本文件）
- 证据原始日志：`D:\project\my-agent-go\docs\evidence\raw\*`
