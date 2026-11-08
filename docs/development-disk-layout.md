# 开发机磁盘占用与目录约定

2026-11-08：C 盘一度只剩 **1 GB**。原因是本项目的开发/取证副作用都默认落在 `%TEMP%` 与用户缓存目录，
而 C 盘没有余量。这份文档记录原因、已经做的迁移，以及后续新增工具时该怎么放。

## 当时占用（C 盘）

| 位置 | 大小 | 说明 |
|---|---|---|
| `%LOCALAPPDATA%\npm-cache` | 8.9 GB | npm 下载缓存（可清、可迁） |
| `%LOCALAPPDATA%\go-build` | 7.0 GB | Go 构建缓存（可清、可迁） |
| `%TEMP%` | 8.4 GB | 其中 `anvil-u07-build-*` 97 个 × 31 MB ≈ 2.9 GB、`policy-executor-*` 173 个 ≈ 684 MB，其余是各测试的临时目录 |
| `%USERPROFILE%\go\pkg\mod` | 2.1 GB | Go 模块缓存（可迁） |

处理：`npm cache clean --force`、`go clean -cache`、删除上述泄漏目录；C 盘从 **1 GB → 13.8 GB** 可用。

## 现在的目录约定（都已生效）

| 内容 | 位置 | 怎么生效 |
|---|---|---|
| Go 构建缓存 | `D:\caches\go-build` | `go env -w GOCACHE=...`（用户级） |
| Go 模块缓存 | `D:\caches\go-mod` | `go env -w GOMODCACHE=...`（用户级） |
| npm 缓存 | `D:\caches\npm-cache` | `npm config set cache ...`（用户级 `.npmrc`） |
| Playwright 浏览器 | `D:\caches\ms-playwright` | 用户环境变量 `PLAYWRIGHT_BROWSERS_PATH` |
| 测试/构建临时目录 | `D:\caches\tmp` | `scripts/devtools.ps1` 覆盖子进程 `TEMP`/`TMP`；可用 `ANVIL_CACHE_ROOT` 改根 |
| git worktree | `D:\project\anvil-wt\*` | 并行任务各用一个 worktree |
| 证据 | `docs/evidence/runs/...`（仓库内，D 盘） | 已 gitignore |
| 本地预览包 | `D:\project\anvil-preview\`、`.tmp/portable/` | 已 gitignore |

## 已修的泄漏（这才是根因，不只是"缓存大"）

1. `tests/native/uidriver_windows_test.go`：每个 UI case 建一个 `anvil-u07-build-*`（内含 31 MB exe），
   从不清理。现在**每次运行前扫掉 1 小时前的遗留目录**（并发进程的目录不动）。
2. `internal/policy/observation_test.go`：每次跑都建 `policy-executor-*` 目录。同样加了 1 小时窗口的清理。
3. `scripts/devtools.ps1`：所有 runner 派生的子进程改用 `D:\caches\tmp`，新泄漏也只占 D 盘。

## 新增工具时的规则

- 任何下载型工具（浏览器、SDK、模拟器、包管理器缓存）**必须**先设到 `D:\caches\<tool>`，不允许默认落 C 盘。
- 测试里的临时目录用 `t.TempDir()`（自动清理）；进程级构建产物必须有"下次运行清理 + 过期窗口"。
- 不要在仓库里放大文件；`docs/evidence` 只放文本/小 JSON，截图录屏走 gitignore 的 `.tmp` 或外部目录。
- 交付给用户的包放在 `D:\project\anvil-preview\`，不要放 C 盘用户目录。
