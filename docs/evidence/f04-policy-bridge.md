# F04 执行前授权：策略服务、策略端点、Pi 侧强制点、项目资源隔离

- 代码：`internal/policy/{service,approval,pathscope}.go`、`internal/policybridge/{server,auth,client}.go`、`pi-extensions/harness-policy.ts`、`internal/runtime/pi/config_isolation.go`
- 原始证据：`docs/evidence/raw/f04-policy-integration.json`（由真实 Pi 集成用例写出）

## 1. 结构：为什么需要四层

Pi 的工具执行发生在它自己的进程里。任何"只在 Go 侧检查"的设计都拦不住内置工具，
任何"只在提示词里要求模型先征求批准"的设计都可以被说服。所以强制点必须落在
Pi 的 `tool_call` 钩子上，并且这个钩子必须去问一个不可伪造的权威：

| 层 | 职责 | 不做 |
|---|---|---|
| `policy.Service` | 判定 allow/deny/ask；审批绑定参数指纹、session、run、configRevision、controlGeneration；60s 过期 | 不做 OS 沙箱，不解析 shell 命令 |
| `policy.PathGuard` | 把文件类工具的目标路径解析成真实路径，判是否在授权根内 | 不跟随 `\\?\` 之类让规范化失效的前缀 |
| `policybridge.Server` | 仅回环 + 随机端口 + 每 worker 随机令牌 + Host/Origin/session 校验；人工确认在此阻塞完成 | 不把令牌放进任何 DTO |
| `pi-extensions/harness-policy.ts` | 唯一的执行前强制点：`tool_call` 与 `user_bash` 都先问端点，非 allow 一律 block | 不缓存"上次允许过" |
| `pi.IsolationSpec` | 用参数切断项目级可执行资源（`.pi/*`、`.agents/skills`、AGENTS.md、内置 MCP） | 不试图在加载后"清理" |

## 2. 用例状态（只写实际跑过的）

| Case | 状态 | 实际证据 |
|---|---|---|
| SEC01 | **PASS**（单测） | 参数变更、跨会话、交替换算、二次答复、60s 过期、接管/配置变更后失效，均有断言；审批绑定的是**解析后的真实路径** |
| SEC02 | **PARTIAL** | 真实 Pi：拒绝 bash → 工作区无 `tool-ran.txt`；批准 → 有（正向对照）。`write`/`edit` 未在真实 Pi 上逐条跑，仅有 require-scope 单测 |
| SEC03.runtime | **PARTIAL** | 真实 Pi：builtin 与 external-MCP 两条通道均"拒绝 → handler 未到达 / 批准 → 到达"。自有 harness MCP、扩展工具、codemode 嵌套调用未逐条真实验证 |
| SEC04.runtime | **PASS**（通道层） | 无令牌/错令牌/长度不同令牌/query 令牌/错 Host/网页 Origin/错会话/未知字段/超大请求/未知通道 → 全部在触达策略前拒绝，副作用计数 0 |
| SEC05 | **PASS**（Windows） | symlink（目录/文件/悬空）、junction、UNC、`\\?\`、`\\.\`、`C:foo`、ADS、`..`、同前缀兄弟目录全部拒；大小写变体解析到同一文件 |
| SEC06 | **PASS**（通道层） | 审批超时、排队中撤销、端点关闭、请求取消 → deny 且副作用 0；撤销后再批准也被拒 |
| SEC08 | **PARTIAL** | 真实 Pi：项目 `.pi/extensions`（加载即写 marker）未执行、`.pi/settings.json` 未生效（工具列表无 codemode）、`AGENTS.md` 未进入模型请求。项目 `.pi/mcp.json`、`.agents/skills`、祖先目录发现未用真实 marker 单独验证 |
| CFG03.isolation | **PARTIAL** | 隔离参数覆盖表 12 条通道全部有负责参数，且"删参数 → 判不合格 + 通道变未切断"可测；resources 子项属 C03/G2 |

G0 仍不解锁：SEC02/03/08 只到 PARTIAL，且缺 mac/linux-arm64 实机证据。

## 3. 测试抓到的真问题（不是补测试，是改实现）

1. **悬空链接越界被放行**。首版 `resolveReal` 在目标不存在时回退到"最近的已存在祖先"，
   于是根内一个指向根外的悬空链接会被当成普通文件名返回——写进去实际落在根外。
   改为逐段 `Lstat`/`Readlink` 解析，目标不存在时把剩余段接在**已解析的真实祖先**后面。
2. **junction 在 Go 里不被报成 `ModeSymlink`**。实测 `os.Lstat` 对目录联接返回普通目录
   （mode 无 `ModeSymlink`），但内核照样穿透。只信 `Mode` 会整类漏判。
   改为对非 symlink 条目也试探 `Readlink`：链接必成功，普通目录必失败。
3. **审批签发与校验用的路径不一致**。`ResolveApproval`/`ValidateBinding` 直接拿字面路径
   算指纹，而 `Authorize` 记的是真实路径——同一文件换个拼写就"对不上自己的审批"。
   三处统一走 `normalizeScope`。
4. **"已消费"语义堵死了批准后的执行前复查**。原来一次 `ResolveApproval` 就把审批标记消费，
   导致批准后无法再做执行前复核（复核被拒 = 批准无效；放开 = 可重放）。
   拆成 `Resolved`（用户已答复）与 `Executed`（已被一次执行领取），`once` 在复查时原子领取。
5. **junction 的 reparse 缓冲区**：`SubstituteNameLength`/`PrintNameLength` 必须**不含**结尾 NUL，
   含进去内核直接回 `ERROR_INVALID_REPARSE_DATA`。文档查不到，靠逐变体实测得出。

## 4. 对抗性验证（改坏 → 变红 → 恢复）

| 改坏什么 | 结果 |
|---|---|
| 关掉桥的令牌校验与 Host 校验 | 5 个子用例变红（无令牌/错令牌/短令牌/query 令牌/伪造 Host 全部返回 200） |
| 真实 Pi 用例里去掉 `-e harness-policy.ts` | 变红："策略端点没有收到任何待审批请求"，拒绝场景下文件也会被写出 |
| 逐项删除隔离参数 | `ValidateArgs` 判不合格，且覆盖表对应通道变为未切断 |
| 把 `Put` 写成未加密 / 短路 checksum | （F07 已有）对应用例变红 |

## 5. 已知暴露面与未做

- **自有 harness MCP server 未实现**。当前 MCP 通道走的是 Pi 自带 MCP 客户端，
  已被策略扩展覆盖；`StartHarnessServer(binding) (Endpoint, error)` 及其 loopback/令牌
  设计尚未落地。因此 SEC03 里"自有 MCP"这一条没有对应实现，不能算通过。
- **令牌通过环境变量交给 Pi 子进程**。同用户下的其它进程可以读取该进程的环境块。
  缓解手段是每 worker 随机 + 绑定 session + Host/Origin 限制；真正的隔离要等容器/独立用户，
  这一点在 G0 之后的沙箱设计中处理，不在这里假装解决。
- **项目 `.pi/mcp.json` 的隔离只是参数保证**（`-na` + `--no-mcp`），没做"放一个会写 marker
  的 MCP server 到项目里再确认没启动"的实测。SEC08 因此记 PARTIAL。
- **`--no-extensions` 会连带关掉内置 MCP**，保留 MCP 必须显式 `-e builtin:mcp`。
  这条已写进 `IsolationSpec` 与集成用例，避免以后重构时静默丢掉 MCP。
- 调度上仍未接线：`policybridge.Start` 尚未接入 `RunService`（F05），审批请求还没有走 UI
  事件流。目前 `OnPending` 是回调，接口形态已定。
