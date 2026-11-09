# FULLACCESS（后端）报告

状态：PASS（后端范围）；UI 开关留给后续前端任务。
Worktree：`D:/project/anvil-wt/fullaccess`，分支 `feat/fullaccess`。
规格：`docs/specs/2026-11-08-permission-mode-full-access.md`。
任务书：`C:/Users/Administrator/AppData/Local/Temp/anvil-sdd/FULLACCESS-brief.md`。

本轮只做后端：设置档位、策略判定、受理时钉住、无人值守强制 ask、逐条留痕事件 +
测试。编辑框下方的选择器/警示条/页脚文案属于前端任务，未动 `frontend/**`。

## 1. 档位在哪、怎么存

- 常量：`internal/config/permission_mode.go` —— `PermissionModeAsk = "ask"`（默认）、
  `PermissionModeFullAccess = "full_access"`，`NormalizePermissionMode`（空 → ask，
  认不出的值返回 false，调用方必须显式决定，绝不能当成 full_access）。
- 持久化：`Settings.PermissionMode`（`json:"permissionMode"`，无 omitempty——
  DTO 形状恒定，`TestSaveSettingsWithRevisionRejectsStaleRevision` 的“文件里不得出现
  Settings 之外的键”断言仍然成立）。
  - `saveSettings` 是唯一写点：校验档位（认不出 → `INVALID_INPUT`，不落盘）。
  - 加载：`resolveSettings` 归一；磁盘上写了认不出的值 → 退回 ask + `setLastErr` 留痕
    （方向只能更严）。
  - 新入口 `Desktop.SetPermissionMode(mode) SettingsResult`：读磁盘字节取 CAS 凭据 →
    只改这一个字段 → 带凭据写回。**不调用 resetRuntime()**（见第 3 节）。

## 2. 策略侧：只跳过人工审批

`internal/policy/service.go` + `internal/policy/permission_mode.go`：

- `Options.AutoAllow`（服务级默认，默认 false）。
- per-run 档位登记：`PinRunPermissionMode(runID, mode)` / `ReleaseRunPermissionMode(runID)` /
  `RunPermissionMode(runID)`。**登记值压过服务级默认**（无人值守钉 ask 就压得过默认 full_access）。
- `Authorize` 判定顺序不变：资源范围归一 → 项目信任 → 权限硬边界（shell/network/
  filesystem/未知通道）→ 会话级许可 → **第 4 步：档位放行** → 第 5 步：签发审批。
  也就是说越权调用在档位判断之前就 deny，`full_access` 只替换第 5 步。
- 放行结论 `Decision=allow` + `Reason=ReasonAutoAllowed`（可识别、稳定），且不签发审批 ID。
- 留痕：`SetAutoAllowObserver(fn)`，在**释放锁之后**回调（不在持锁时调用 sink）。

## 3. 钉在哪（本任务要求写明）

三处，同一份事实：

1. **`internal/run/context.go` 的 `ExecutionBinding.PermissionMode`** —— 真正的钉子。
   在 `resolveBinding`（`run.Service.Start` 的受理阶段）写入，之后只读，随 run 冻结。
   读取入口：`run.Service.Binding(runID)`（只读，供回放/诊断/测试）。
2. `internal/run/service.go` 的 `launch()`：把 `binding.PermissionMode` 装进策略服务的
   per-run 登记（`PinRunPermissionMode`），在策略端点起来**之前**完成，`release()` 时摘掉
   （终止的 run 不再有权威授权请求，登记表不随 run 数量无界增长）。
3. `internal/app`：`permissionModeForAttendedRun()` 在每次 `Start` / `StartSessionRun`
   现读一次设置交给请求，不缓存。

因此：运行中切开关 → 设置立即变、`d.svc` 不变（不重建运行时，正在跑的 run 不被打断）、
正在跑的 run 仍按 binding 里的旧档位判定；下一条 run 用新档位。

## 4. 无人值守一律 ask

- `internal/scheduler/service.go` 的 `fire()`：提交时显式
  `PermissionMode: config.PermissionModeAsk`，定时触发与「立即运行」同一口径。
- RunService 对空/未知档位也钉成 ask（fail-closed），所以“忘了传”不会变成免审。

## 5. 事件（policy.auto_allowed）

- `internal/run/events.go`：`EventPolicyAutoAllowed = "policy.auto_allowed"` +
  `autoAllowedPayload{runId, sessionId, toolName, toolCallId?, source?, resourceScope?, permissionMode, reason}`。
- `internal/run/auto_allow.go`：`noteAutoAllowed` 用与其它 run 事件**同一个** `s.emit`，
  所以 Wails emitter、`diagnostics.Recorder`（run_events）、Replay 全部自动覆盖。
- 载荷里放归一后的 `resourceScope`（真实路径）与工具名，不放完整参数（事件会进诊断包）。

## 6. 改动清单（仅限任务书 Touch 列表）

新增：
- `internal/config/permission_mode.go`
- `internal/policy/permission_mode.go`、`internal/policy/permission_mode_test.go`
- `internal/run/auto_allow.go`、`internal/run/permission_mode_test.go`
- `internal/scheduler/permission_mode_test.go`
- `internal/app/permission_mode.go`、`internal/app/permission_mode_test.go`

修改：
- `internal/policy/service.go`：Options.AutoAllow、Service 字段（autoAllow/runModes/onAutoAllow）、
  New 初始化、Authorize 第 4 步（放行 + 留痕）。
- `internal/run/context.go`：ExecutionBinding.PermissionMode + AutoApproves()。
- `internal/run/service.go`：StartRunRequest.PermissionMode；resolveBinding 归一并写入；
  launch 钉档位；release 摘档位；New 装观察者；新增只读 Binding(runID)。
- `internal/run/events.go`：事件常量 + 载荷类型。
- `internal/app/desktop.go`：Settings.PermissionMode；saveSettings 校验；Start 传档位；
  permissionModeForAttendedRun。
- `internal/app/session_api.go`：StartSessionRun 传档位（与 Start 同一口径）。
- `internal/app/settings_env.go`：resolveSettings 归一档位（认不出 → ask + lastErr）。
- `internal/scheduler/service.go`：fire() 显式 ask。

未动：`internal/contracts/**`、`internal/devtools/**`、`schemas/**`、`frontend/**`、`tests/**`、`docs/**`。
（`internal/policybridge/**` 也未动 —— 档位判定在策略服务内，端点只报身份。）

## 7. 测试与证据

新增用例（全部 PASS，无 skip、无断言弱化）：

`internal/policy`
- `TestAutoAllowSkipsOnlyHumanApproval`：同一写动作/命令在 ask 下签发审批，在 AutoAllow 下
  allow + `ReasonAutoAllowed` 且**不签发审批 ID**。
- `TestAutoAllowKeepsPermissionBoundaries`：full_access 下仍 deny —— 工作区外文件、
  未授予 shell、未授予网络、未知工具通道、未受信任项目来源（5 条边界各一条断言）。
- `TestRunPermissionModeIsPinnedPerRun`：per-run 钉住互不影响；未登记 run 回落服务默认；
  服务级默认开着时钉成 ask 的 run 仍 ask；Release 后回到默认；未知档位拒绝钉住。
- `TestAutoAllowObserverSeesNormalizedCall`：每次放行通知一次、带生效档位与归一后的资源；
  ask 下不通知。

`internal/run`
- `TestPermissionModePinnedAtAdmissionOnlyAffectsNextRun`：第一条 run 钉 full_access 并仍在跑，
  第二条钉 ask；两条 binding 各自正确，判定各自按快照走（第一条 allow、第二条 ask）；
  未指定档位钉成 ask。
- `TestAutoAllowedCallEmitsEventOnRunStream`：`policy.auto_allowed` 出现在**同一事件流**，
  封套 run/session 正确，载荷含 toolName/toolCallId/runId/sessionId/permissionMode/reason。
- `TestPermissionModeReleasedWhenRunTerminates`：run 结算为 completed 后策略服务里不再留着
  这条 run 的档位登记。

`internal/scheduler`
- `TestScheduledDispatchAlwaysAsks`：定时触发派发的 `StartRunRequest.PermissionMode` 是 ask；
  「立即运行」也是 ask。
- `TestScheduledRunKeepsAskWhenPolicyDefaultAutoAllows`：执行侧策略服务默认
  `AutoAllow=true`，无人值守 run 的**绑定快照**仍是 ask，且同一份策略服务对该 run 的写动作
  仍返回 ask；对照：交互 run 显式要 full_access 时 binding 是 full_access。

`internal/app`
- `TestPermissionModeDefaultsToAskAndPersistsThroughCAS`：默认 ask；`SetPermissionMode`
  落盘（settings.json 里 `permissionMode: full_access`）、重启后仍是 full_access；未知档位
  `INVALID_INPUT` 且不改变生效值；关回 ask 立即生效。
- `TestUnreadablePermissionModeFallsBackToAskOnLoad`：磁盘上 `"permissionMode":"yolo"`
  → 加载为 ask 且 `LastError()` 有原因。
- `TestFullAccessPinsAtAdmissionAndOnlyAffectsNextRun`：设置 full_access → 起 run（binding
  full_access）→ 运行中 `SetPermissionMode(ask)` → **运行时不重建**（`mustRuntime` 同一对象）、
  第一条 run 仍 running 且 binding 仍 full_access、新 run 是 ask。
- `TestAutoAllowedCallLandsInRunEvents`：**端到端留痕** —— 用 run 的 worker 环境里真实的
  `MYAGENT_POLICY_URL`/`TOKEN`（+ 会话头）向策略端点 POST 一次 `write` 授权请求 → 响应
  `decision=allow` → 轮询诊断库里真实的 `run_events` 行，`type='policy.auto_allowed'`，
  载荷指明 run/session/工具/档位。
- `TestScheduledRunAsksEvenWhenSettingIsFullAccess`：设置 full_access，调度「立即运行」派发的
  真实 run 的 binding 是 ask；同一条运行时上交互提交仍按设置拿 full_access。

### 命令与退出码

```
go test ./internal/policy ./internal/app ./internal/run ./internal/scheduler -count=1
  internal/policy   ok   1.186s
  internal/app      FAIL 24.542s   ← TestDiscoverPiAcceptsRealPiThroughProductionProbe（环境）
  internal/run      FAIL  7.228s   ← TestRealPiClarificationEndToEnd（环境）
  internal/scheduler ok  1.684s
  退出码 1
```

两个 FAIL **都是环境缺件，不是本任务的断言**，且已证明与本任务无关：
在本 worktree 上 `git stash -u` 后（即 FULLACCESS 改动全部移除）重跑，这两个用例
**同样 FAIL、失败信息完全相同**：

```
--- FAIL: TestDiscoverPiAcceptsRealPiThroughProductionProbe
    pi_lifecycle_api_test.go:455: 本机没有官方独立包（…\.tools\pi\standalone\v1.5.0-win-x64\pi.exe）
--- FAIL: TestRealPiClarificationEndToEnd
    clarification_realpi_test.go:106: 本机没有参考 Pi 独立包（…\.tools\pi\standalone\v1.5.0-win-x64\pi.exe）
```

加上 `-run` 只跑本任务用例时，四个包全绿（13 个新用例，见上）：
`go test ./internal/policy ./internal/run ./internal/scheduler ./internal/app -run '…' -count=1` → 0。

```
gofmt -l internal/ cmd/                       → 无输出（退出码 0）
go vet ./internal/...                         → 无输出（退出码 0）
go vet ./...                                  → 退出码 1：
    frontend\embed.go:11:12: pattern all:dist: no matching files found
scripts/devtools.ps1 verify --suite=static    → 退出码 1：
    [PASS] gofmt 无未格式化文件
    [FAIL] go vet（同上 frontend/dist 缺件）
    [FAIL] 前端 typecheck：'tsc' 不是内部或外部命令（frontend/node_modules 未安装）
```

静态门禁的两条 FAIL 同样是**环境/前置条件**，与本次改动无关：本 worktree 没有
`frontend/node_modules` 与 `frontend/dist`（未执行前端安装/构建）。已用 `git stash -u`
在改动全部移除的树上重跑 `verify --suite=static`，**输出逐行相同、退出码同为 1**。

`go test ./internal/... -count=1`（全树）：失败集合 = `internal/app` ×1、
`internal/piinstall` ×2、`internal/run` ×1、`internal/runtime/pi` ×1，全部是
“本机没有 `.tools/pi/standalone/…`”这一类环境缺件（逐条信息均指向该路径）。

## 8. 限制与 concern

1. **浏览器写动作与连接器动作不在这条链路上**：它们各自有独立的 `policy.Service`
   （`internal/app/browser_api.go:812`、`internal/app/connector_api.go:99`），没有 run 档位
   登记，所以 `full_access` 下它们的审批**仍然会弹**。方向是安全的（不会意外放宽），但
   与产品文案“不再逐次弹审批”不完全一致。要覆盖需要给这两处各自的授权权威接同一份
   per-run 档位，属于另一条任务（涉及 browser/connector 包，本次 Touch 列表不含）。
2. **智能体侧留痕的粒度**：`policy.auto_allowed` 每次自动放行一条（载荷含工具名、调用 ID、
   归一后的资源范围），但**不含完整参数**（事件会进诊断包）。若诊断页需要“完整命令文本”，
   要在诊断展示侧另接参数来源（例如已有 `tool.start` 事件），不要往这条事件里塞。
2b. **诊断页的“结构化展示”还没接这条事件**：事件确实在 `run_events`（本任务的端到端用例
   直接读库证明），诊断包导出（`internal/diagnostics/export.go`）也会把它整行带走；但
   `internal/diagnostics/run_details.go` 目前只为 tool/approval/compaction/diagnostic/error
   建了分区，没有 `policy.auto_allowed` 的专门分区（未知类型不进任何分区）。
   要让“诊断页突出显示被自动放行的调用”还需要改 `internal/diagnostics/**`，
   这不在本任务 Touch 列表内，留给诊断页任务。
3. **档位登记的清理边界**：`release()` 摘登记，`Close()` 不释放仍在跑的 run（沿用旧语义），
   其登记随运行时被丢弃的 `policy.Service` 一起回收；同一进程内不会跨运行时泄漏。
4. **`SetPermissionMode` 的 CAS 语义**：读磁盘取凭据后带凭据写回，两次读之间若有别的写者
   改文件，会得到 `CONFIG_CONFLICT`（不静默覆盖）。它与 `SaveSettings` 的区别只有一条：
   不 `resetRuntime()`。
5. 前端输入区（选择器/警示条/页脚）**未实现**，按任务书留给后续前端任务；后端已提供
   `Settings.PermissionMode`（读）与 `SetPermissionMode`（写，返回 `SettingsResult`）。
6. 本机是 Windows 10 22H2，`win-x64` 证据按项目规则只算开发证据；本轮无 UI 断言，
   真机验收（agent-desktop）留给前端任务。

Task: FULLACCESS
