# PIB03 证据：界面状态与安装 / 升级 / 卸载验收

日期：2026-11-27。状态：**PIB03 实现 PASS（win-x64）**；
安装包级用例见下表，未执行项按 `NOT_RUN` 记录，不宣称。

## 0. 执行信息

| 项 | 值 |
|---|---|
| Task ID | PIB03 |
| 分支 | `feat/pib03-bundled-pi`（从 `feat/bundled-pi` 的 `61e5f28` 新建） |
| 实施提交 | `8156a39`（后端状态/安装拒绝/导入命令）、`fabea6e`（前端状态与导入 UI）、`d59d615`（验收发现的两处修复）、`6d5ad16`（英文文案） |
| 执行者 | 主 Agent 直接实施。派发 subagent 时执行器返回 `Model "magpie/remote-magpie/auto/deepseek-v4.1-flash" not found`，无法确认具名 subagent 的实际模型，按规范记为派发 `BLOCKED`；主 Agent 实际模型即 `PI_MODEL`（见下），与要求一致 |
| 实际模型 | `remote-magpie/auto/deepseek-v4.1-flash` |
| 会话 / 运行 ID | `01a21b5c-d314-733f-8f37-210a4a0847a6` |
| 系统 | Windows 10 专业版 22H2，build **19045**，x86_64 |
| 目标 | `win-x64`（其余五个目标 `NOT_RUN`，ADR 0005 / 0008 不变） |
| 工作树 | `D:\project\anvil-wt\anvil-rebuild` |
| 验收安装位置 | `D:\AnvilAccept\{nsis5,msi5}`；数据根 `D:\AnvilAccept\{dataA,dataB,...}`；均实测在 D 盘 |

## 1. 安装包（build5，含全部 PIB03 代码）

```text
> node .tmp\tauri-cli\node_modules\@tauri-apps\cli\tauri.js build \
    --config src-tauri\tauri.bundle.conf.json --bundles nsis,msi
Compiling anvil v0.1.0 ... Finished `release` profile
Finished 2 bundles at:
  D:\anvil-target\release\bundle\nsis\Anvil_0.1.22_x64-setup.exe (41.97 MiB)
  D:\anvil-target\release\bundle\msi\Anvil_0.1.22_x64_en-US.msi  (73.50 MiB)
TAURI_EXIT=1   ← 仅因为本机没有 TAURI_SIGNING_PRIVATE_KEY（更新器签名），安装包已产出
```

| 产物 | SHA-256 |
|---|---|
| `Anvil_0.1.22_x64-setup.exe`（NSIS） | `54bfc1e5d765869a1058100f18a200a4c1dbdb28599759c5b4147fe3fb24e0aa` |
| `Anvil_0.1.22_x64_en-US.msi`（MSI） | `54dc98c1ef01353f1bff163e1d4ae143709e0a929727130a01ba34602cba8afc` |

包内运行时（安装后实测，NSIS `D:\AnvilAccept\nsis5\runtime\pi`，239 MB，文件数 1,032）：

```text
> D:\AnvilAccept\...\runtime\pi\node\node.exe --version                    → v24.16.0   (exit 0)
> D:\AnvilAccept\...\runtime\pi\node\node.exe ...\dist\bundle\cli.js --version → 1.6.0  (exit 0)
```

`manifest.json` 的 `node/node.exe` SHA-256 `b3094d0b…0aa3` 与 staging 一致（PIB01 记录）。

## 2. 用例

| 用例 | 结果 | 实际证据 |
|---|---|---|
| **PIB03-A 干净安装** | `NOT_RUN`（离线部分）／安装与启动已验 | 本机不是干净环境：系统已装外部 Pi 1.6.0 与 Node，且未断网。在**全新数据根**上从 NSIS 安装包安装并启动成功，探测到包内 1.6.0（见 B 的同一组证据）。断网、无 Node/Pi/npm 的干净机安装未执行 |
| **PIB03-B 外部共存** | **PASS** | PATH 首位放外部 `pi.cmd`（执行即写标记），并设外部 `PI_CODING_AGENT_DIR=D:\AnvilAccept\external-agent`。启动后 catalog 行为 `pi / 1.6.0 / bundled`，`anvil.log` 记录 `pi: bundled 1.6.0 (node 24.16.0) at D:\AnvilAccept\nsis5\runtime\pi`，标记文件**不存在**（外部 shim 未被执行），外部 agent 目录未写入。截图 `pib03/settings-runtime-bundled.png`、`pib03/onboarding-bundled-pi.png` |
| **PIB03-C 正常升级** | `PARTIAL` | 旧版导入已实测：`~/.pi/agent` + `~/.pi/skills` → `dataA`，UI 报告「已复制 15 项，跳过 2 项」，`models.json`/`mcp.json`/`prompts`/`extensions`/`skill/*` 落盘，`auth.json` 未复制，`settings.json`/`agents` 保留原样不覆盖；再次导入全部「已存在，跳过」（幂等）。截图 `pib03/settings-import-plan.png`、`pib03/settings-import-done.png`。**未执行**：从旧 Anvil 版本安装包升级、以及走已签名整包更新（本机无签名私钥） |
| **PIB03-D 卸载** | **PASS**（NSIS + MSI） | NSIS `uninstall.exe /S` → exit 0，安装目录内仅剩验收时人为放入的 `cli.js.bak`（破坏用例的测试文件），node/CLI/插件资源全部移除；数据根 `dataA` 保留；系统外部 Pi 1.6.0 与 `~/.pi` 完好。MSI `msiexec /x {693E2908-…} /qn` → exit 0，安装目录剩余文件数 **0**；数据根保留 |
| **PIB03-E 损坏包** | **PASS** | 安装副本中移走包内 CLI 后启动（外部 `pi` 仍在 PATH 首位）：catalog 行为 `pi / — / bundled / adapter_failed`，detail 为「the bundled Pi runtime is incomplete: …cli.js is missing; reinstall Anvil」；首启向导显示同一错误 + 「请重新安装 Anvil…本版本不会通过 npm 安装 Pi」，Pi 不可勾选、`完成设置` 禁用；外部 shim 未执行。截图 `pib03/onboarding-damaged-bundle.png` |

## 3. 界面状态（截图，均为 build5 实际安装版）

| 文件 | SHA-256 | 内容 |
|---|---|---|
| `docs/evidence/raw/pib03/settings-runtime-bundled.png` | `32c2defd…56aa6` | 设置 → 运行时：`已内置（随 Anvil 一起安装）`、版本 `1.6.0`、包内 CLI 路径、数据根 `D:\AnvilAccept\dataA`、「检查更新」（Anvil 更新）、「从旧版 Pi 导入…」；无 npm 更新/重新安装按钮 |
| `docs/evidence/raw/pib03/onboarding-bundled-pi.png` | `76b84d77…2ffa0` | 首启第 2 步：`Pi Agent v1.6.0` 已勾选、无「一键安装」按钮 |
| `docs/evidence/raw/pib03/settings-import-plan.png` | `4df4e640…c42ec` | 导入计划：新增 / 已存在跳过 分项列出；明示不复制凭证与会话 |
| `docs/evidence/raw/pib03/settings-import-done.png` | `dbbd0aa6…9bf59` | 导入结果「已复制 15 项，跳过 2 项」，列表刷新为全部已存在 |
| `docs/evidence/raw/pib03/onboarding-damaged-bundle.png` | `bb609857…e62d6` | 损坏包：错误原文 + 重装提示，无 npm 安装入口 |

## 4. 验收中发现并修复的缺陷（`d59d615`）

1. **包内版本被显示成 Node 版本。** Tauri `resource_dir()` 返回 `\\?\D:\…` verbatim 路径；
   `node <cli> --version` 在该拼写下 Pi 无法解析自身包路径，输出了 Node 的 `24.16.0`，
   catalog 与界面照此显示。`BundledPi::from_dir` 现在剥离 verbatim/UNC 前缀，并加回归测试。
2. **子代理扩展把包内示例当成“没有”。** 状态与安装仍从 PATH 上的 `pi` 推导
   `examples/extensions/subagent`，对包内运行时误报“当前 pi 包没有该示例”，且无法安装。
   现优先使用 bundle 的 `subagent_source`（PATH 仅作为无 bundle 的开发/测试回退）；
   实测安装后的 `extensions/subagent/index.ts` 与包内示例逐字节相同。
3. **未登录提示指向旧位置。** 文案仍叫用户 `pi login` 并改 `~/.pi/agent/models.json`；
   改为「设置 → 模型」与 Anvil 数据根（英文侧同步修正，`6d5ad16`）。

## 5. 测试

```text
powershell -NoProfile -ExecutionPolicy Bypass -Command "& .\scripts\env-rs.ps1; Remove-Item Env:PI_CODING_AGENT_DIR; cargo test --locked --workspace"
→ CARGO_EXIT=0，24 个 suite `test result: ok`，0 FAILED

npm --prefix frontend test   → Test Files 59 passed (59)，Tests 474 passed (474)
npm --prefix frontend run build → tsc -b && vite build 成功
```

新增/改写的关键测试：`a_verbatim_install_path_is_spelled_plainly`、
`a_bundled_runtime_is_the_subagent_source`、`a_loaded_bundle_reports_bundled_with_its_own_paths`、
`a_bundle_without_its_cli_reports_damaged_with_the_loaders_words`、
`installing_pi_is_refused_for_a_bundled_install`、`the_pi_update_check_follows_the_anvil_installer`、
`a_damaged_bundle_never_falls_back_to_an_external_pi`，前端 4 个 bundled/import 用例与 3 个改写的向导用例。

## 6. 限制与未执行

- 六个目标只有 `win-x64` 在本机取证；其余 `NOT_RUN`（ADR 0005 / 0008）。
- 已签名更新链路（`TAURI_SIGNING_PRIVATE_KEY`）缺失，整包更新用例 `NOT_RUN`。
- 「干净离线机」未执行：本机已有外部 Pi/Node，且未断网；PIB03-A 的离线声明保持 `NOT_RUN`。
- 真实模型对话未在本轮执行（未登录状态，且登录会写入验收数据根之外的状态）；沿用既有 Pi 登录约定，联网对话作为后续真实验收项。
- `SettingsPage` 的 `sample` 运行时卡片是 `/__sample/settings` 设计预览，只弹提示、不触发 npm 或安装动作，保持原样。

## 7. Linux 试用（Box 云桌面，2026-11-27）

目的是回答“Linux 能不能装、能不能跑”，**不是**正式跨平台验收：ADR 0005 / 0008 的平台矩阵不变，
此处只记录真实结果。

环境：Ubuntu 24.04（Box 云桌面，x86_64，2 vCPU / 3.9 GB，XFCE，DISPLAY=:0），
rustup + rustc 1.95.0，webkit2gtk-4.1 2.52.6，系统 Node 24.21 仅用于构建前端。

### 7.1 构建前修掉的三处跨平台缺陷

| 提交 | 缺陷 | 后果 |
|---|---|---|
| `ba635d3` | `anvil-devtools` 无条件链接 Windows 注册表 API | Linux 上 `bundle-pi` 无法编译/运行；现按 Windows 门控，并在其他平台读 `/etc/os-release` 与内核版本 |
| `8aea02e` | `bundle-pi` 把 Node `LICENSE` 复制到二进制旁边 | Unix 上落在 `node/bin/LICENSE`，清单检查要求 `node/LICENSE`，staging 自检失败 |
| `529e1bf`、`9b16b6e` | 包内运行时扫描只“标记”旧 runtime 行 | 从旧版升级后出现两行 Pi Agent（一行仍是 `pi login` 旧提示）；改为包内运行时只保留本次扫描的那一行，升级与损坏两种情况都覆盖 |

### 7.2 构建与安装

```text
> cargo run --locked -p anvil-devtools -- bundle-pi --target=linux-x64
bundle-pi: staged …/src-tauri/bundle-runtime/pi for linux-x64 (node 24.16.0, pi 1.6.0)
node/bin/node → v24.16.0；cli.js --version → 1.6.0

> npx @tauri-apps/cli@2 build --config src-tauri/tauri.bundle.conf.json --bundles deb -- --locked
Finished 1 bundle at: …/Anvil_0.1.22_amd64.deb (79.17 MiB)
TAURI_EXIT=1   ← 仅缺 TAURI_SIGNING_PRIVATE_KEY
sha256 aecb4118ba1b28053575585230af0333b44884c8104edbc0b860bb62b9390a6c

> sudo apt-get install -y ./Anvil_0.1.22_amd64.deb
Unpacking anvil (0.1.22) over (0.1.17)     ← Box 上原有旧版，升级路径顺带被验证
INSTALL_EXIT=0
/usr/lib/Anvil/runtime/pi/node/bin/node --version      → v24.16.0
/usr/lib/Anvil/runtime/pi/…/dist/bundle/cli.js --version → 1.6.0
```

启动（`DISPLAY=:0 /usr/bin/anvil`）后日志：

```text
pi: bundled 1.6.0 (node 24.16.0) at /usr/lib/Anvil/runtime/pi
pi: agent_dir=/home/user/.anvil skill_root=/home/user/.anvil/skill
catalog: pi is unauthenticated at /usr/lib/Anvil/runtime/pi/…/dist/bundle/cli.js — no credential found yet: sign in from 设置 → 模型 …
```

catalog 数据库只剩一行：`('pi', '1.6.0', 'bundled', 'unauthenticated')`（旧 0.1.17 留下的
`known_dir` 行已被清掉）。Box 上没有外部 `pi`。

界面：首启直接进入工作台（PIB04 已去掉引导）；设置 → 运行时显示单行
“Bundled with Anvil / 1.6.0 / /usr/lib/Anvil/runtime/pi / Data root /home/user/.anvil”，
并给出“Import from the old Pi…”入口（截图 `linux-runtime-bundled.webp`）。

### 7.3 损坏与卸载

```text
> sudo mv /usr/lib/Anvil/runtime/pi/…/dist/bundle/cli.js{,.bak}; 重新启动
catalog: ('pi:unavailable', None, 'bundled', 'adapter_failed',
          detail='the bundled Pi runtime is incomplete: …cli.js is missing; reinstall Anvil')
```

界面单行显示 “Bundled runtime damaged” + 加载器原文 +“Pi is never (and no longer needs to be)
installed through npm.”（截图 `linux-runtime-damaged.webp`），没有第二个 Pi 行、没有 npm 入口。

```text
> sudo apt-get remove -y anvil
REMOVE=0
/usr/bin/anvil 已移除；/usr/lib/Anvil 仅剩损坏用例自己放的 cli.js.bak（已手动清理）
/home/user/.anvil 完整保留；/home/user/.pi 未被修改
```

### 7.4 截图

| 文件 | SHA-256 | 内容 |
|---|---|---|
| `docs/evidence/raw/pib03/linux-first-launch.webp` | `1f880944…3d032` | 首启直接工作台，无引导 |
| `docs/evidence/raw/pib03/linux-runtime-bundled.webp` | `636c4028…3678c` | 运行时页：单行、已内置、1.6.0、数据根 |
| `docs/evidence/raw/pib03/linux-runtime-damaged.webp` | `e70a0c8a…c987` | 损坏包：单行、错误原文、重装提示 |

### 7.5 本节的限制

- 只构建并安装了 `.deb`；AppImage、其他架构、签名整包更新、离线机均 `NOT_RUN`。
- Box 有网络、有系统 Node 与 WebKitGTK，因此这不是“干净离线安装”的证明。
- 结论仅限“Linux x64 这个环境里，安装、启动、使用包内 Pi、损坏提示、卸载都能走通”。
