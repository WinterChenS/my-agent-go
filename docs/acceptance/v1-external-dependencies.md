# Anvil V1 外部依赖登记

日期：2026-11-05。用途：列出所有会让 case 变成 BLOCKED 的外部资源，以及需要用户拍板的事项。缺失项只把对应 case 记为 BLOCKED，不删 case、不换口径、不用 mock 或静态页面代替。

## 1. 机器与显示环境

| 依赖 | 影响 | 现状 | 由谁提供 |
|---|---|---|---|
| Windows 11 24H2 x64 验收机 | win-x64 上所有 case 的验收结论 | 本机是 Windows 10 22H2（build 19045），低于基线最低版本。本机证据只算开发证据；GOV05 实现后门禁会自动拒绝 | 用户：提供机器，或授权使用符合要求的 CI runner |
| win-arm64、mac-x64、mac-arm64、linux-x64、linux-arm64 五台目标机（或等价 CI runner） | 所有 scope=ALL/NATIVE 的 case；G0–G4 全矩阵 | 本机只有 win-x64。历史上 CI 跑过六目标（`docs/evidence/ci-verify-round3.md`），重跑需要推送/触发授权 | 用户：提供机器或授权触发 CI |
| Ubuntu 24.04 的 X11 与 Wayland 两种会话 | BR03 及 Linux 上所有 NATIVE case | 无 | 同上 |
| macOS 机器（Intel 与 Apple Silicon） | B02、BR02，以及 macOS 上的更新替换（UPD08/UPD13） | 无 | 同上 |
| 无 Go/Node/Pi 的干净安装环境（六目标各一） | PKG02、PKG04、PI_PKG02、INS07 | 无 | 同上 |

## 2. Pi 与模型

| 依赖 | 影响 | 现状 | 由谁提供 |
|---|---|---|---|
| 各目标的官方独立 Pi 包 | F06、PI_INS01–03、PI_UPG01–03、PI_PKG01–02、INS01–07 | **已核实**：v1.5.0 发布页六个目标都有，windows 为 `.zip`，darwin/linux 为 `.tar.gz`，附 `SHA256SUMS`。win-x64 已实测（PI04） | 已满足；其余目标的运行验证仍要目标机器 |
| Pi 上游发布签名 | F06 的信任模型 | **已核实**：没有独立签名文件，也没有 GitHub artifact attestation。信任根改为嵌入已签名 Anvil 的 SHA-256 | 无需提供；上游以后提供签名再加校验 |
| 真实模型 API key | E2E04、MOD02（真实 provider 部分）、C08 的真实连接器旅程 | 本机有一组，C02 证据使用过 | 用户；会产生费用 |

## 3. 连接器账户与应用注册

均须使用专用测试账户，不使用个人或生产账户（R09）。fixture server 只覆盖 CONN02–CONN06、CONN08 的策略与副作用语义；CONN01 的真实认证和读取必须使用下列账户。不能把 OAuth client secret 编译进桌面二进制：优先使用平台明确支持的 device flow/public-client + PKCE 或细粒度 token；必须使用 client secret 的平台由用户提供应用注册，secret 只进入 OS 凭证库。平台不支持桌面 public client 且没有用户注册或安全 broker 时，对应连接器保持 `blocked`。对支持授权码 + PKCE 的平台，回调只用 `http://127.0.0.1:<随机端口>/callback` 这类 loopback 地址，注册应用时按平台规则登记；不能假设每个平台都支持 PKCE。

| 连接器 | 需要的注册 | 最小权限 | 影响 |
|---|---|---|---|
| 邮箱 | 专用测试邮箱，开启 IMAP/SMTP；应用专用密码，或提供方要求的 OAuth 客户端（XOAUTH2） | 只读收件箱 + 发信 | CONN01（mail）、E2E06 真实部分 |
| GitHub | 测试组织与仓库；device flow OAuth App 客户端 ID，或细粒度 PAT；只有上游明确支持时才用 loopback + PKCE | 只授予测试仓库：Metadata 读、Contents 读、Issues 读写、Pull requests 读写 | CONN01（GitHub） |
| Slack | 测试 workspace；用户提供 Slack App（client id/secret）。secret 只进 OS 凭证库，不随 Anvil 发布 | 读频道历史、发消息到指定频道，按 V1 动作最小化 | CONN01（Slack） |
| Notion | 测试 workspace；优先 internal integration token；public integration 需用户提供注册信息且不能依赖内置 secret | 只共享测试页面；读、插入、更新内容 | CONN01（Notion） |
| 钉钉 | 企业测试组织；企业自建应用（AppKey/AppSecret）与测试群 | 读允许范围、群消息发送 | CONN01（DingTalk） |
| 飞书 | 企业测试租户；企业自建应用（App ID/Secret） | 测试文档读写、发送消息 | CONN01（Feishu） |

各平台的权限名称在注册时以平台当前文档为准，并记录进 C08 证据。

## 4. 打包、签名、更新与发布

| 依赖 | 影响 | 现状 | 由谁提供 |
|---|---|---|---|
| 打包工具：go-task（`task`）、NSIS（`makensis`）、Docker（Wails 跨平台构建任务使用） | X03 产出安装包 | 本机未安装（`build/platforms.json` 已记录） | 执行 X03 时安装并锁定版本 |
| Windows 代码签名证书与时间戳服务 | REL01；Windows 上 updater helper 的签名要求（UPD06/UPD13） | 无 | 用户 |
| Apple Developer ID Application 证书、公证凭据、Team ID | REL02；macOS 更新替换的签名核对（UPD08/UPD13） | 无 | 用户 |
| Linux 发布签名密钥（校验和/仓库签名） | REL03 | 无 | 用户 |
| 应用更新签名密钥对（私钥只放发布 runner 的安全存储） | UPD01–UPD13 的真实包部分、X05 | 无。开发与测试使用临时生成的密钥，私钥不落盘 | 用户提供 runner 安全存储；X05 生成并托管 |
| 更新清单与安装包的 HTTPS 托管地址 | UPD03、UPD13，F09 的 allowlist | 无。未配置时应用更新保持 `blocked` | 用户 |
| 推送/触发 CI 的授权 | 多目标证据 | 需逐次授权 | 用户 |
| 公开发布授权 | G5、REL04 | 未授权 | 用户 |

## 5. 待用户决定的事项

| 事项 | 选项 | 当前计划采用 | 何时必须定 |
|---|---|---|---|
| F06 Pi 安装器的时序（ADR 0002 与已批准规格的冲突） | A：排在 C08→C09→F09 之后、UI 集成之前，PI_INS/PI_UPG 留在 G2。B：排到全部功能之后，PI_INS/PI_UPG 移到 G4，需要修改已批准规格的门禁归属 | A（等待确认） | F06 开工前 |
| 产品标识：`productIdentifier`（反向域名，例如 `com.<你控制的域名>.anvil`）与发行者名称 | 必须是你能证明归属的域名或组织名 | 未定；不能用占位值发布 | X03 开工前。发布后改动会破坏 macOS 更新（核对 bundle ID）和 Windows 卸载注册 |
| `anvil://` 的含义 | A：只作为应用内的逻辑地址（搜索结果、诊断、导航用），实际页面来源仍是 Wails 固定的 `wails://` / `http://wails.localhost`，也不向操作系统注册。B：作为真实 URL 协议，需要修改 Wails 源码（按 AGENTS.md 先写 ADR），若同时注册到操作系统，还会多出一个外部启动入口 | A | U00 开工前 |

两种 F06 方案在开发机上都只写临时托管根，不碰 `.tools/pi/`、`~/.pi` 和真实数据目录。

## 6. 核实记录

| 事实 | 依据 |
|---|---|
| Pi v1.5.0 发布资产与 `SHA256SUMS` 内容 | GitHub Releases API `repos/earendil-works/pi/releases/tags/v1.5.0`；`SHA256SUMS` 下载内容与 `.tools/pi/standalone/SHA256SUMS-1.5.0.txt` 逐字节一致 |
| 无 artifact attestation | `repos/earendil-works/pi/attestations/sha256:12d58a70…` 返回 404 |
| Wails v3 打包元数据位置与模板 | 模块缓存 `wailsapp/wails/v3@v3.0.0/internal/commands/build_assets`：`config.yml` 与 windows（nsis、msix）、darwin、linux（appimage、nfpm）模板；`wails3 generate build-assets` 默认输出到 `build/` |
| Wails v3 内部页面协议不可配置 | 同一模块 `pkg/application` 中 Linux/macOS 写死 `wails://`，Windows 写死 `http://wails.localhost` |
| 本机系统版本 | `Win32_OperatingSystem`：10.0.19045 |
