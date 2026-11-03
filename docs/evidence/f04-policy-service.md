# F04（部分）：执行前授权服务的实现证据

- 代码：`internal/policy/{service,approval}.go`
- 测试：`internal/policy/policy_test.go`

## 已完成 vs 未完成

| F04 组成部分 | 状态 | 说明 |
|---|---|---|
| `Authorize` / `ResolveApproval` / `Decision(allow/deny/ask)` | 完成 | 见下方规则 |
| 审批绑定（参数/会话/run/configuration/controlGeneration）与过期 | 完成 | `TestApprovalBindsArgumentsAndExpires` 等 |
| 项目来源信任门（未信任项目一律不执行） | 策略层完成 | `TestUntrustedProjectDoesNotExecuteMarker` |
| Harness MCP server（loopback + 每 worker 随机令牌 + Host/Origin 校验） | **未做** | `internal/harnessmcp/` 尚不存在 |
| policy bridge（`internal/policybridge/`）与 `pi-extensions/harness-policy.ts` | **未做** | 真实 Pi 侧的 hook 覆盖表还没有 |
| `TestAllToolPathsUsePolicy`（真实 Pi + 受控 provider 逐通道验证） | **未做** | 依赖上面两项 |
| UNC/junction/symlink 路径校验 | **未做** | `pathscope` 未实现 |

因此 **SEC01–SEC06、SEC08 不能判 PASS，G0 仍 BLOCKED**。本轮只交付"决策逻辑 + 绑定语义"这一层，且它在单测层面成立。

## 判定规则

1. **项目来源先判信任**：`Origin = project:<root>` 且 root 不在 TrustedOrigins → `deny`，
   连审批机会都不给（项目自动发现的扩展/MCP/`!command` 不得靠"用户点同意"变成可执行）。
2. **权限硬边界**：shell 类工具需 `permissions.shell`；网络类需 `permissions.network`；
   写类需 `filesystem ∈ {workspace, unrestricted}`。不满足 → `deny`。
3. **未知通道拒绝**：未登记的工具来源一律 deny（默认拒绝，不默认放行）。
4. **其它 → ask**：签发一次性审批，绑定 toolCallID/工具名/参数指纹/session/run/
   configRevision/controlGeneration，默认 60 秒过期。
5. **执行前再校验**：`ValidateBinding` 必须在使用许可前再查一次——排队期间可能被接管
   （controlGeneration 变化）或配置已改（configRevision 变化）。

`Guard` 是唯一执行入口：只有 `allow` 才调用 effect，测试用副作用计数器证明
"ask/deny 阶段计数恒为 0"。

## 参数指纹

`sha256(source + name + resourceScope + 规范化 JSON 参数)`。规范化用 unmarshal→marshal，
所以 `{"a":1}` 与 `{ "a": 1 }` 视为同一调用，而键序变化也不改变指纹——这是有意的：
空白/键序不是"参数变更"。

## 对抗性验证

| 破坏点 | 期望 | 实际 |
|---|---|---|
| 项目信任判断短路为永假 | 未信任项目应 deny | 红：`未信任项目应 deny，实际 ask`（说明该断言确实在验证信任门，而不是恒真） |

## 本地验证

```text
go test ./internal/policy/ -count=1    ok
gofmt -l internal/policy               无输出
```

## 限制

- 策略层**不是 OS 沙箱**：它只决定"允不允许执行"，不限制已放行的工具能做什么。
- `session_scope` 许可按"工具名 + 参数指纹"落在内存，进程重启即失效（可接受，但需知道）。
- 没有真实 Pi 的 hook 覆盖验证，所以"所有工具通道都过策略"目前只是代码意图，不是证据。
