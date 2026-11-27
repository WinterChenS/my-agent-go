# PIB01 证据：固定版本并打进安装包

日期：2026-11-27。状态：**PIB01 PASS（win-x64 staging）**；其余五个目标的 staging 记为 `NOT_RUN`；
安装包级验收（NSIS/MSI 实际产物、安装/升级/卸载）属于 PIB03，本任务不宣称。

## 0. 执行信息

| 项 | 值 |
|---|---|
| Task ID | PIB01 |
| 实施提交 | `5bbe926`（ADR 0009 + 依赖输入 + `bundle-pi` + Tauri 资源映射 + release job） |
| 执行者 | 本会话主 Agent 直接实施（未再派发 subagent；本执行器无 `deepseek-2/deepseek-v4.1-flash` 具名 subagent） |
| 实际模型 | `remote-magpie/auto/deepseek-v4.1-flash`（`PI_MODEL`） |
| 会话 / 运行 ID | `anvil-1795746486696`（`PI_SESSION_ID`） |
| 系统 | Windows 10 专业版，build **19045**（22H2），`PROCESSOR_ARCHITECTURE=AMD64` |
| 目标 | `win-x64` |
| 工作树 | `D:\project\anvil-wt\anvil-rebuild`（分支 `feat/bundled-pi`） |
| 数据根 | 本任务不涉及运行时数据根（PIB02），staging 产物在 `src-tauri/bundle-runtime/pi/`（被 `.gitignore` 忽略） |

## 1. 产物

| 文件 | 内容 |
|---|---|
| `docs/adr/0009-bundle-pi-with-anvil.md` | 决定：安装包携带 Pi + 私有 Node；运行时只用包内版本；数据根跟随 `HostConfig.data_dir`；升级走 Anvil 更新器；卸载移除安装资源；打包失败即停止发布 |
| `src-tauri/runtime-pi/package.json` | 依赖输入：`@earendil-works/pi-coding-agent` **1.6.0**（精确版本），`engines.node` 24.16.0 |
| `src-tauri/runtime-pi/package-lock.json` | lockfileVersion 3，123 个包，全部 `resolved` 指向 `https://registry.npmjs.org`（不绑本机 npm 镜像，CI 可复现） |
| `tools/devtools/src/bundle_pi.rs` | `bundle-pi` 子命令：按目标架构取 Node（固定 SHA-256）、`npm ci --omit=dev --ignore-scripts`、产出 `manifest.json`、`--check-only` 复检 |
| `src-tauri/tauri.bundle.conf.json` | `bundle.resources` 把 `bundle-runtime/pi/` 映射为安装资源 `runtime/pi/`；基础 `tauri.conf.json` 不变，干净工作树的 `cargo test` 不受影响 |
| `.github/workflows/release.yml` | 每个矩阵项新增 `pi_target`；打包前 stage + `--check-only`；`tauri build` 增加 `--config tauri.bundle.conf.json` |
| `.gitignore` | `/src-tauri/bundle-runtime/` |

固定值（`bundle_pi.rs` 内）：Node **24.16.0**；六个目标的 Node 归档 SHA-256 取自
`https://nodejs.org/dist/v24.16.0/SHASUMS256.txt`。`win-x64` 归档
`node-v24.16.0-win-x64.zip` = `edaca9bd58ec8e92037dac4e877d52f6b8f430b81c18b57e264b4e2fb111cd56`。

## 2. Staging 与包内 CLI

```text
> anvil-devtools.exe bundle-pi --target=win-x64
bundle-pi: downloading https://nodejs.org/dist/v24.16.0/node-v24.16.0-win-x64.zip
bundle-pi: npm ci in ...\src-tauri/bundle-runtime/pi\pi
bundle-pi: staged ...\src-tauri/bundle-runtime/pi for win-x64 (node 24.16.0, pi 1.6.0)
  b3094d0b49f9ad602262a9921551737bb97637c05dd357a06ae98188d7290aa3  node/node.exe
  e79626f2dd6f94aa45d30f3fa63cd84319a6eefcd150b353cfaf274366926774  pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js
exit=0   （首轮含 Node 下载与 npm ci，约 30s）

> anvil-devtools.exe bundle-pi --target=win-x64 --check-only
bundle-pi check: ...\src-tauri\bundle-runtime/pi is complete (win-x64, pi 1.6.0)
exit=0
```

包内 `manifest.json`（节选字段，值即落盘内容）：

```json
{
  "schema": 1,
  "target": "win-x64", "platform": "windows", "arch": "x64",
  "nodeVersion": "24.16.0", "node": "node/node.exe",
  "piPackage": "@earendil-works/pi-coding-agent", "piVersion": "1.6.0",
  "cli": "pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js",
  "piAi": "pi/node_modules/@earendil-works/pi-ai/dist/providers/all.js",
  "subagentSource": "pi/node_modules/@earendil-works/pi-coding-agent/examples/extensions/subagent",
  "packageDir": "pi/node_modules/@earendil-works/pi-coding-agent",
  "licenses": ["node/LICENSE"],
  "sha256": { "node/node.exe": "b3094d0b…0aa3", "…/dist/bundle/cli.js": "e79626f2…6774" }
}
```

用**绝对 Node 路径**执行包内 CLI：

```text
> .\node\node.exe --version
v24.16.0                                          exit=0

> .\node\node.exe .\pi\node_modules\@earendil-works\pi-coding-agent\dist\bundle\cli.js --version
1.6.0                                             exit=0
```

| 项 | 值 |
|---|---|
| staging 根 | `D:\project\anvil-wt\anvil-rebuild\src-tauri\bundle-runtime\pi` |
| Node | `…\pi\node\node.exe`，92 279 112 字节，`v24.16.0` |
| Pi CLI | `…\pi\pi\node_modules\@earendil-works\pi-coding-agent\dist\bundle\cli.js`，160 字节（bundle 入口，实际代码在 `dist/bundle/chunks/`），`1.6.0` |
| pi-ai（OAuth 模块） | `…\pi\pi\node_modules\@earendil-works\pi-ai\dist\providers\all.js` |
| subagent 示例 | `…\examples\extensions\subagent\{index.ts,agents.ts,agents/,prompts/}` |
| 体积 | 239 MB（`node/` 89 MB + `pi/` 151 MB） |
| 许可证 | `node/LICENSE` 随包；Pi 的 npm tarball 不带 LICENSE 文件（包内 `package.json` 声明 MIT），manifest 记录实际随包文件，不虚构 |

## 3. 失败用例（打包准备检查必须失败）

三个负例都是"除该缺陷外一切正常"的 staging 树，用同一个命令复检：

```text
> anvil-devtools.exe bundle-pi --target=win-x64 --out=.tmp/pib01-negative/wrong-arch --check-only
bundle-pi: node\node.exe: built for arm64 but win-x64 needs x64
exit=1

> anvil-devtools.exe bundle-pi --target=win-x64 --out=.tmp/pib01-negative/missing-cli --check-only
bundle-pi: pi\node_modules\@earendil-works\pi-coding-agent\dist/bundle/cli.js: the pi CLI is missing
exit=1

> anvil-devtools.exe bundle-pi --target=win-x64 --out=.tmp/pib01-negative/missing-subagent --check-only
bundle-pi: pi\node_modules\@earendil-works\pi-coding-agent\examples/extensions/subagent/agents/: missing
bundle-pi: pi\node_modules\@earendil-works\pi-coding-agent\examples/extensions/subagent/prompts/: missing
exit=1
```

生成负例的脚本：`.tmp/pib01-negative/make.mjs`（本地临时，不入库；用例本身由
`tools/devtools/src/bundle_pi.rs` 的 `#[cfg(test)]` 固化，见下表）。

单元测试（`cargo test --locked -p anvil-devtools --lib bundle_pi`）：

```text
test bundle_pi::tests::every_release_target_has_a_pinned_archive_hash ... ok
test bundle_pi::tests::a_complete_tree_passes_and_records_paths_and_hashes ... ok
test bundle_pi::tests::a_node_for_the_wrong_architecture_is_rejected ... ok
test bundle_pi::tests::a_missing_cli_is_rejected ... ok
test bundle_pi::tests::a_missing_subagent_example_is_rejected ... ok
test bundle_pi::tests::a_pi_package_at_the_wrong_version_is_rejected ... ok
test bundle_pi::tests::headers_are_read_for_all_three_formats ... ok
test bundle_pi::tests::the_pinned_version_comes_from_the_lockfile ... ok
test bundle_pi::tests::the_build_stamp_is_not_a_difference ... ok
test result: ok. 9 passed; 0 failed
```

`cargo test --locked -p anvil-devtools` 全量：47 lib + 12 contract fixture 通过。
`cargo test --locked --workspace`：除 02d440c 已记录的**两个既有** `host_contract`
失败（`generate_handler!` 与 `APP_COMMANDS` 顺序漂移、capability 多出 window/updater 权限）
外全绿；这两个失败与本任务无关，`git diff 33f9883 -- src-tauri/src src-tauri/capabilities` 为空。

## 4. 未执行 / 阻塞

| 项 | 状态 | 原因 |
|---|---|---|
| 其余五个目标的 staging（win-arm64 / mac-x64 / mac-arm64 / linux-x64 / linux-arm64） | `NOT_RUN` | 本机只在 win-x64 上有 builder；`--target` 的归档与 SHA 已固化，需在对应 runner 上执行（release job 会做） |
| CI 实跑（release job） | `NOT_RUN` | 需要打 tag 发布流程；本次只改 job 定义，未触发 |
| NSIS / MSI 实际产物与安装后资源落盘 | `NOT_RUN` | 属于 PIB03；本机尚未安装 `cargo-tauri`，且安装器验收需要单独的执行用例 |
| `tauri build --config tauri.bundle.conf.json` 的资源映射实测 | `NOT_RUN` | 同上：本机没有 Tauri CLI。映射语义与 `tauri-action` 的 cwd（`projectPath=src-tauri`）已按其源码确认（`runner.execTauriCommand(command, rawArgs, projectPath, …)`），但未在本机跑通 |

## 5. 证书 / 凭证

本任务不涉及 OAuth 或 API Key，不产生凭证文件；证据中不含任何凭证值。
