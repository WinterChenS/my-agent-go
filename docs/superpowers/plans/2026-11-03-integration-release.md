# Integration and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用真实Pi、真实原生网页、真实目标安装包验证V1，而非只交付开发模式演示。

**Architecture:** 本地受控fixture保证可重复；真实模型单独验收；平台runner采集原始证据；发布门禁汇总六目标而不隐去缺项。

**Tech Stack:** 基线锁定版本、各平台native SDK与打包/签名工具。

**Spec:** V1 执行基线、Anvil V1 产品/UI/生命周期设计、master plan、`docs/acceptance/v1-gates.md`。

## Global Constraints

必须区分 G4内部候选与G5公开正式发行；签名凭证和目标机器缺失为BLOCKED。用户未授权公开发布时只构建/验证产物，不上传发布。此计划要求的代码审查不授权给外部账号发送消息。

## Review Focus

开发目录能启动而安装包缺assets→X03；仅mock provider闭环→X02；应用升级覆盖用户Pi或记忆→X04；混用旧revision测试报告→X03/X05；退出残留子进程/浏览器视图泄漏→X02/X04。

## X01：三条 deterministic 完整使用旅程

**依赖：** C01–C09/B07/U00–U07、G2。**Files：** `tests/e2e/{filesystem,mcp,browser,connector,schedule}_journey.*`、`tests/e2e/fixtures/`、`internal/devtools/e2e.go`、`docs/evidence/e2e/`。

**Interfaces：** `devtools verify --suite=e2e --target=<targetId>` 从构建后Wails程序启动，传隔离appData、fixture provider/MCP URLs、fixture workspace；提供 test-only结构化状态探针，release必须移除/禁用探针。不能开放生产任意调试/写数据库接口。

- [ ] 编写三个初始失败旅程：本地文件读取→授权修改→产物核对；MCP发现→调用→结果→禁用再调用被拒；右侧网页观察→授权填写/提交→服务端counter→接管→恢复。
- [ ] 编写连接器旅程（E2E06）：真实 Wails + Pi 通过 fixture 连接器完成读取 → 审批 → 写入，fixture 写计数精确为 1；拒绝审批时为 0；重启后定义仍在，秘密只在 OS 凭证库。
- [ ] 编写调度旅程（E2E07）：真实 Wails 创建定时任务，可控时钟触发普通 RunService 会话并产出产物；重启后 next/last 与 occurrence 记录一致，没有重复执行。
- [ ] 为每条旅程建立独立oracle：文件hash/内容、MCP server调用记录、网页server数据库/计数；UI文本不是唯一证据。
- [ ] 连接真实Pi执行受控provider脚本，完成旅程；每条必须包含重启客户端后可查历史、配置revision和产物来源，不能依赖内存假数据。
- [ ] 三端目标跑built app，UI自动化可用原生test driver/辅助功能，native区域步骤允许人工录制但必须逐步记录；React组件测试不覆盖此项。
- [ ] 归档来源revision、版本/fixture hash、命令、计数、录屏、人工review，suite退出码一致后提交。

**验收：** E2E01–E2E03、E2E06–E2E07。

## X02：真实模型验收与故障演练

**依赖：** X01/F03/F04/F05。**Files：** `tests/integration/live_model_test.go`、`tests/e2e/failure_matrix.*`、`docs/evidence/live-model/`；默认本地unit不自动发起真实云调用。

**Interfaces：** `devtools verify --suite=live --target=<targetId> --credential-ref=<ref>`；F01 runner须为该suite明确注册，运行前显示将调用哪一个provider/model。凭证从OS库读取，命令行只传引用，报告不存secret。整个G3至少一个已支持provider的真实模型闭环，各平台均有模型调用与工具执行烟测；不强制同一模型多次重测以堆证据。

- [ ] 真实Pi+真实模型完成三条E2E旅程的最小版；浏览器写操作只访问本地fixture，不给真实网站发帖/购买。模型结果不确定时保留失败，不能换成回放文件声称通过。
- [ ] 故障演练：kill worker、MCP退出、模型401/429/超时、UI断线、应用异常退出、安装下载断网、数据库被锁、凭证库不可用；每项状态/恢复路径与基线一致。
- [ ] `TestInterruptedRunDoesNotReplayUnknownWrite`从fixture外部确认计数保持1；重试逻辑只针对明确幂等/未发送阶段，不能对所有tool error一律retry。
- [ ] 进程树退出后10秒内owned workers/MCP/fixture计数=0；外部用户Pi/浏览器保持存活。不能按进程名批量杀所有node/pi。
- [ ] 执行e2e+live suites、归档数据和费用usage可用性，提交。模型key/网络缺失记BLOCKED，不能改成本地fixture通过live。

**验收：** E2E04/E2E05。RUN05 与 RPC05 的服务语义归 F05/F03；X02 在真实模型下复核它们，结果计入 E2E05。

## X03：六目标安装包与干净机器

**依赖：** G3。**Files：** `build/config.yml` 与 `build/{windows,darwin,linux}/`（由 `wails3 generate build-assets` 生成后提交，是 Wails v3 的打包元数据位置；修改 `info` 后用 `wails3 update build-assets` 重新生成）、`.github/workflows/{verify,package}.yml` 或等价所选CI、`internal/devtools/{package,packageverify}.go`、`tests/package/`、`docs/evidence/packages/`。

**打包约束（已核实 Wails v3.0.0 模板）：**
- `build/config.yml` 的 `info.productName=Anvil`；`productIdentifier` 与 `companyName` 由用户定（见外部依赖登记 §5）。发布后不得再改：macOS 更新核对 bundle ID，Windows 卸载注册也依赖它。
- Windows 用 NSIS 且 `INSTALL_SCOPE=user`（当前用户安装，不需要管理员，F09 更新依赖这一点）；macOS 用 `wails3 tool package --format dmg`；Linux 只产 deb 与 AppImage，关闭 Taskfile 默认的 rpm 与 aur（不在 V1 矩阵）。
- V1 不向操作系统注册 `anvil://` 协议：`config.yml` 的 protocols 保持为空。外部注册会给其它程序一个启动 Anvil 并传入参数的入口，需要 ADR 和输入校验。
- 打包工具：`task`（go-task，Wails Taskfile 使用）、NSIS `makensis`、跨平台构建任务使用的 Docker；版本写入 `build/versions.lock.json`。
- 打包前确认 `tests/acceptance/oracle-allowlist.json` 为空（F01 未闭合项），否则 G4 返回 2。

**Interfaces：** master package/verify 命令；产物命名 `Anvil-<version>-<targetId>.<ext>`，manifest 字段 version/sourceRevision/versionsLockHash/target/hash/sizeBytes/files/runtimeDependencies/channel。版本初始 0.1.0-internal.1，后续按实际 release 记录递增；不可上传未经用户授权的公开 release。

- [ ] 编写 `MissingNativeEvidenceFailsGate`、`PackageRuntimeScanFindsPiNode`：缺目标报告或包里含Pi/Node/runtime必须失败。扫描解包内容/文件清单，不只文件名搜字面字符串；验证构建脚本未下载并捆绑这些runtime。
- [ ] 原生runner生成win两架构NSIS、mac两架构DMG、Linux两架构deb/AppImage；保留构建工具版本与依赖清单。交叉编译允许作为构建方式，运行验收仍在对应目标上执行。
- [ ] 在无Go/Node/Pi开发工具的干净环境安装并启动；无Pi页面能一键安装到用户目录、probe通过，然后最小模型/本地文件/MCP/browser旅程可用。安装后不从开发目录读assets或绝对源码路径。
- [ ] 中文用户名/空格workspace、离线启动、WebView缺失、下载受限、默认非管理员用户分别检查；缺Pi离线时仍能管理设置/历史，不能卡空白窗口。
- [ ] 包体预算：Windows/macOS压缩产物≤60MiB，Linux deb≤80MiB、AppImage≤220MiB；实际解包与安装后字节另报，Pi后下载占用另报。超预算写体积归因与ADR，不删除required资源或临时抬预算。
- [ ] 执行package suite、六目标manifest及hash汇总；缺任何安装运行证据G4=BLOCKED，不能把“已生成文件”当安装通过。提交。

**验收：** PKG01–PKG05；CI账号未配置可先产本地证据，不虚构绿色CI链接。

## X04：Anvil 品牌迁移、应用更新、数据保留、回滚与性能

**依赖：** X03/F06/F07/F09。**Files：** `tests/package/{brand_migration,update,upgrade,rollback,uninstall,performance}.*`、`docs/evidence/performance/`、`docs/evidence/update/`、必要迁移修复。

**Interfaces：** 更新验收使用两份真实 Anvil 安装包（baseline 与 candidate），其二进制 `--version` 与 SHA-256 必须不同；app data schema、Pi active 指针、memory/artifact/session/schedule/connector 索引 hash 作为前后对照。`ANVIL_*` 优先，`MYAGENT_*` 仅保留一个开发兼容周期。升级前 fixture 记录明确预期，不用空数据库验收迁移。

- [ ] 建立上一内部版本数据：两个 workspace、两个 Profile、active/candidate 记忆、会话、浏览器 cookie、连接器定义、调度任务、凭证引用和外部 Pi 路径。Anvil 首次启动读取并事务迁移旧 MyAgent 数据；保留来源/备份，不修改秘密或外部 Pi。
- [ ] 执行 F09 的真实更新流程：检查 → 后台下载 → 双层验签 → ready（当前 PID 不变）→ 正常退出 → 下次启动 helper 应用 → 新版本健康检查。证据同时记录旧/新进程版本和 hash。
- [ ] 模拟 DB 迁移中断、helper 交换中断、健康检查失败和 Pi 激活失败：数据库/程序备份恢复可用；任何阶段只留下一个完整 active 版本。降级到不兼容 schema 必须拒绝或走已验证恢复路径。
- [ ] 逐目标验证错误 channel/target/arch/source/redirect/schema/hash/signature/降级均被拒；断点续传只在 ETag/validator 不变时继续。应用更新前后外部和托管 Pi 的路径/hash 不变。
- [ ] 卸载默认保留用户数据/外部 Pi；选择清理托管 Pi 不删除外部安装或超出目标目录；所有递归删除先解析并验证预期 Anvil 根目录。
- [ ] 性能采用统一 fixture：至少4核/8GB/SSD，记录实际 CPU/RAM/OS/引擎和样本数；不含模型响应时间。冷启动到可交互10次 p95≤5秒；100事件/秒持续60秒，Go事件接收到UI显示 p95≤200ms；收集状态序号保证关键事件不丢。
- [ ] 预热5轮后开关 native tab 50次，关闭后等待30秒，产品拥有的进程总 RSS 相对预热基线增长≤100MiB；native tab/handle计数归零，10秒后 owned 子进程为0。RSS统计含浏览器子进程并说明共享页口径。
- [ ] package/update/performance suites 及迁移验证通过，产出 G4 原始证据并提交。缺签名、机器或基线包时对应 case 为 BLOCKED，不能用 fixture 更新冒充真实包升级。

**验收：** PKG06/PKG07、UPD01–UPD14、PERF01–PERF04。

## X05：正式发行门禁和最终交接

**依赖：** G4，签名/公证资源及公开发行授权。**Files：** `packaging/signing/`、`build/release-manifest.json`、`docs/release/{checklist,known-limitations,user-guide}.md`、`docs/evidence/release/`。

**Interfaces：** `devtools gate --id=G5 --evidence=<root>`；release manifest只引用同revision已验收的包；签名密码和私钥来自runner安全存储，不能写仓库/日志。

- [ ] Windows签名链/时间戳验证；macOS应用及内嵌native可执行签名、公证、staple与Gatekeeper验证；Linux发布校验和与签名清单并验证，实际发行渠道要求另记录。
- [ ] 应用更新信任根：生成并托管更新签名密钥（私钥只在发布 runner 安全存储中，不进仓库或日志），构建时把公钥注入 F09 的 `trustroot`；签署更新清单 `manifest.json.sig` 与完整包签名；记录密钥轮换与注销流程，轮换的新公钥必须先随一个已签名版本发出。
- [ ] 更新清单与包的托管地址写入 F09 的 HTTPS allowlist；托管不可用或未授权发布时，应用更新保持 `blocked`，UPD13 记 BLOCKED。
- [ ] 校验签名后包若发生变化，hash更新且重新执行package安装验证；不能给未测旧包换名称冒充当前release。
- [ ] 文档列出精确支持矩阵、Pi版本/安装方式、浏览器Unsupported场景、凭证归属、安装/恢复/卸载步骤；不能宣称任意网站或全平台通用。
- [ ] 独立reviewer核对G0–G5原始证据及已知限制；没有独立review时G5不能由实现者自己填PASS。该要求适用于开发执行，不要求本轮计划编写启动子agent。
- [ ] 只在用户已授权发行时上传；否则交付已验证候选及说明，公开发布状态保持pending。列出每个目标下载/本地路径、hash、测试结果和剩余外部依赖。

**验收：** REL01–REL04。到此才可称“公开发行完成”。
