# Anvil V1 外部依赖登记

日期：2026-11-05。用途：列出所有会让 case 变成 BLOCKED 的外部资源。缺失项只把对应 case 记为 BLOCKED，不删 case、不换口径、不用 mock 或静态页面代替。

## 1. 机器与显示环境

| 依赖 | 影响 | 现状 | 由谁提供 |
|---|---|---|---|
| win-arm64、mac-x64、mac-arm64、linux-x64、linux-arm64 五台目标机（或等价 CI runner） | 所有 scope=ALL/NATIVE 的 case；G0–G4 全矩阵 | 本机只有 win-x64。历史上 CI 跑过六目标（`docs/evidence/ci-verify-round3.md`），重跑需要推送/触发授权 | 用户：提供机器或授权触发 CI |
| Ubuntu 24.04 的 X11 与 Wayland 两种会话 | BR03 及 Linux 上所有 NATIVE case | 无 | 同上 |
| macOS 机器（Intel 与 Apple Silicon） | B02、BR02，以及 macOS 上的更新替换（UPD08/UPD13） | 无 | 同上 |
| 无 Go/Node/Pi 的干净安装环境（六目标各一） | PKG02、PKG04、PI_PKG02、INS07 | 无 | 同上 |

## 2. Pi 与模型

| 依赖 | 影响 | 现状 | 由谁提供 |
|---|---|---|---|
| 各目标的官方独立 Pi 发行包 | F06、PI_INS01–03、PI_UPG01–03、PI_PKG01–02、INS01–07 | win-x64 已实测（PI04）；其它五个目标是否都有独立包未核实 | F06 开始时核实；缺失目标记 BLOCKED |
| Pi 上游发布签名 | F06 的签名校验 | 未核实是否提供。未提供时以嵌入 Anvil 的 SHA-256 为信任根 | F06 开始时核实 |
| 真实模型 API key | E2E04、MOD02（真实 provider 部分）、C08 的真实连接器旅程 | 本机有一组，C02 证据使用过 | 用户；会产生费用 |

## 3. 连接器账户与应用注册

均须使用专用测试账户，不使用个人或生产账户（R09）。fixture server 只覆盖 CONN02–CONN06、CONN08 的策略与副作用语义；CONN01 的真实认证和读取必须使用下列账户。

| 连接器 | 需要的注册 | 影响 |
|---|---|---|
| 邮箱 | 专用测试邮箱，开启 IMAP/SMTP；应用专用密码，或提供方要求的 OAuth 客户端（XOAUTH2） | CONN01（mail）、E2E06 真实部分 |
| GitHub | 测试组织/仓库；OAuth App 客户端 ID，或细粒度 PAT | CONN01（GitHub） |
| Slack | 测试 workspace；Slack App（client id/secret，最小 bot scope） | CONN01（Slack） |
| Notion | 测试 workspace；public integration（OAuth）或 internal integration token | CONN01（Notion） |
| 钉钉 | 企业测试组织；企业自建应用（AppKey/AppSecret）与测试群 | CONN01（DingTalk） |
| 飞书 | 企业测试租户；企业自建应用（App ID/Secret）及文档、消息权限 | CONN01（Feishu） |

## 4. 签名、更新与发布

| 依赖 | 影响 | 现状 | 由谁提供 |
|---|---|---|---|
| Windows 代码签名证书与时间戳服务 | REL01；Windows 上 updater helper 的签名要求（UPD06/UPD13） | 无 | 用户 |
| Apple Developer ID Application 证书、公证凭据、Team ID | REL02；macOS 更新替换的签名核对（UPD08/UPD13） | 无 | 用户 |
| Linux 发布签名密钥（校验和/仓库签名） | REL03 | 无 | 用户 |
| 应用更新签名密钥对（私钥只放发布 runner 的安全存储） | UPD01–UPD13 的真实包部分、X05 | 无。开发与测试使用临时生成的密钥，私钥不落盘 | 用户提供 runner 安全存储；X05 生成并托管 |
| 更新清单与安装包的 HTTPS 托管地址 | UPD03、UPD13，F09 的 allowlist | 无。未配置时应用更新保持 `blocked` | 用户 |
| 推送/触发 CI 的授权 | 多目标证据 | 需逐次授权 | 用户 |
| 公开发布授权 | G5、REL04 | 未授权 | 用户 |

## 5. 待用户决定的事项

| 事项 | 选项 | 当前计划采用 |
|---|---|---|
| F06 Pi 安装器的时序（ADR 0002 与已批准规格的冲突） | A：排在 C08→C09→F09 之后、UI 集成之前，PI_INS/PI_UPG 留在 G2。B：排到全部功能之后，PI_INS/PI_UPG 移到 G4，需要修改已批准规格的门禁归属 | A（等待确认） |

两种方案在开发机上都只写临时托管根，不碰 `.tools/pi/`、`~/.pi` 和真实数据目录。
