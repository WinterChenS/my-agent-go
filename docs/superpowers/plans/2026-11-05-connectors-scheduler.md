# Connectors and Scheduler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Anvil V1 提供六类真实连接器和本地持久定时任务，同时复用现有 policy、RunService、凭证库和自有 MCP，不创建旁路执行通道。

**Architecture:** `ConnectorService` 通过统一 driver 接口调用各平台，并把工具注册到 app-owned MCP；`Scheduler` 只负责确定何时创建普通 RunService 会话。所有写入、外发、超时和未知结果沿用现有授权与恢复语义。

**Tech Stack:** Go 1.27.3、SQLite、OS credential store、MCP、HTTP/SMTP/IMAP fixture servers、IANA timezone database。

**Spec:** `docs/superpowers/specs/2026-11-05-anvil-v1-product-ui-lifecycle-design.md`。

## Global Constraints

- V1 连接器：邮箱、钉钉、飞书、Slack、Notion、GitHub。
- 凭证只进入 OS 凭证库；配置和日志只保存 credential reference。
- 对外发送和远端写入必须经过 F04 policy；拒绝或请求派发前失败时 fixture 副作用计数为 0。
- 请求派发后的超时或断线必须返回 `OutcomeUnknown`；外部 oracle 可以观察到 0 或 1 次副作用，非幂等动作不得自动重放。
- 写请求返回未知结果时标记 `OutcomeUnknown`，除非平台幂等键证明安全，否则不自动重放。
- 调度器全局同时运行上限为 2；无人值守运行不能绕过审批。
- 任务时区使用 IANA 名称，测试必须覆盖 DST 和系统时钟跳变。
- 本计划的 case 声明 `oracleKind=external`：runner 的 `oracle.json` 必须是 `external-observation`，`source` 取 `fixture-counter`/`database`/`filesystem`，并附目录内非空 artifact（fixture 计数快照、occurrence 行导出、adapter 服务端日志）。`test-process` 自证会被门禁判死。

---

### Task C08: Typed Connector Framework and Six V1 Adapters

**Dependencies:** F08、F04、F07、U00、U01。U01 先提供壳层/路由注册边界，F08 先完成 desktop 装配，C08 再接入生产页面；不得与 F08/F09/F06/C09 并行修改 `internal/app/desktop.go`。C08 不依赖 F06，也不应被 G0 过闸阻塞开发。

**Acceptance cases:** CONN01–CONN06、CONN08（G2）、CONN07（G3）；与 X01 共享 E2E06。各平台所需账户与应用注册见 `docs/acceptance/v1-external-dependencies.md`。

**Files:**
- Create: `internal/connectors/types.go`
- Create: `internal/connectors/service.go`
- Create: `internal/connectors/service_test.go`
- Create: `internal/connectors/auth/oauth.go`
- Create: `internal/connectors/auth/loopback.go`
- Create: `internal/connectors/auth/tokens.go`
- Create: `internal/connectors/auth/auth_test.go`
- Create: `internal/connectors/mail/driver.go`
- Create: `internal/connectors/dingtalk/driver.go`
- Create: `internal/connectors/feishu/driver.go`
- Create: `internal/connectors/slack/driver.go`
- Create: `internal/connectors/notion/driver.go`
- Create: `internal/connectors/github/driver.go`
- Create: `internal/connectors/fixtures/servers.go`
- Create: `internal/harnessmcp/connector_tools.go`
- Create: `internal/harnessmcp/connector_tools_test.go`
- Create: `internal/app/connector_api.go`
- Create: `internal/app/connector_api_test.go`
- Create: `frontend/src/features/connectors/ConnectorsPage.tsx`
- Create: `frontend/src/features/connectors/ConnectorEditor.tsx`
- Create: `frontend/src/features/connectors/ConnectorsPage.test.tsx`
- Modify: `frontend/src/app/Router.tsx`
- Modify: `frontend/src/app/navigation.ts`
- Create: `internal/storage/migrations/003_connectors.sql`
- Modify: `internal/app/desktop.go`

**Interfaces:**
- Consumes: `policy.Service`, `CredentialStore`, `ExecutionContext`, app-owned MCP registration.
- Produces: `SaveDefinition`, `Diagnose`, `ListActions`, and `Invoke`; each `ActionDescriptor` declares risk, scope, schema, idempotency, and capability.

```go
type Driver interface {
    Diagnose(context.Context, Credential) (Health, error)
    Actions(context.Context, Credential) ([]ActionDescriptor, error)
    Invoke(context.Context, Invocation) (Result, error)
}

type Invocation struct {
    Execution contracts.ExecutionContext
    ConnectorID string
    ActionID string
    Arguments json.RawMessage
    IdempotencyKey string
}
```

- [ ] **Step 1: Write failing credential, schema, and policy-boundary tests**

```go
func TestConnectorDefinitionStoresOnlyCredentialRef(t *testing.T) { /* sentinel absent from JSON/DB/log */ }
func TestDeniedExternalSendNeverReachesDriver(t *testing.T) { /* handler counter remains zero */ }
func TestConnectorCannotChooseAnotherSession(t *testing.T) { /* execution context is server-bound */ }
```

- [ ] **Step 2: Run the failing framework tests**

Run: `go test ./internal/connectors ./internal/harnessmcp -run 'TestConnector|TestDeniedExternal' -count=1 -v`

Expected: FAIL because connector definitions and tools are not registered.

- [ ] **Step 3: Implement definition storage, driver registry, and MCP policy gate**

```go
type Definition struct {
    ID string
    Kind Kind
    DisplayName string
    CredentialRef string
    Enabled bool
    AllowedResources []string
    Revision uint64
}
```

Validate each action input against its schema before authorization. Bind workspace/session/run on the server. Do not trust a driver-reported `readOnly` flag without the Anvil action catalog.

- [ ] **Step 4: Add protocol fixtures with external side-effect counters**

Fixtures must expose independent counters for email send, DingTalk message, Feishu update/message, Slack reply, Notion create/update, and GitHub issue/comment. Read actions return deterministic content without incrementing write counters.

- [ ] **Step 5: Implement per-connector authentication, then the six minimal adapters test-first**

先实现认证，再实现读写动作：

```text
mail:     IMAP/SMTP；应用专用密码或提供方要求时的 XOAUTH2
GitHub:   OAuth 授权码 + PKCE，或用户提供的细粒度 PAT
Slack:    OAuth v2 + PKCE（bot scope 按动作最小化）
Notion:   OAuth + PKCE，或 internal integration token
DingTalk: 企业自建应用 app key/secret 换取访问令牌
Feishu:   企业自建应用 app id/secret 换取 tenant 令牌
```

OAuth 回调只用 RFC 8252 的 loopback 重定向：只绑定 `127.0.0.1` 随机端口，校验 `state` 与 PKCE verifier，只接受一次请求，5 分钟超时。V1 不用 `anvil://` 接 OAuth 回调。令牌只进 OS 凭证库，到期前刷新；刷新失败进入 `blocked`（需重新授权），不显示 connected。撤销授权删除凭证，工具从下一个 run 消失。认证测试至少覆盖：state 不匹配、非 loopback 重定向、刷新失败、撤销后不可调用。

然后每个 adapter 先写一个读成功、一个写成功、一个写被拒、一个认证失败、一个超时和一个结果未知的测试。V1 动作严格如下：

```text
mail: search, read, attachment, draft, send
DingTalk: read_scope, send_group
Feishu: read_doc, update_doc, send_message
Slack: read_channel, reply_thread
Notion: query, create_page, update_page
GitHub: read_repo, read_issue, read_pr, create_issue, comment_pr
```

- [ ] **Step 6: Implement health, capability, and unknown-outcome semantics**

A connector reports `connected` only after a real authenticated health call. Missing OAuth registration or test credentials returns `blocked`, not `connected`. A timeout after request dispatch returns `OutcomeUnknown` with the idempotency key and never auto-retries non-idempotent actions.

- [ ] **Step 7: Add app API, production page, and restart persistence tests**

```go
func TestConnectorRestartPreservesDefinitionNotSecret(t *testing.T) { /* config survives; secret remains in OS store */ }
func TestDisabledConnectorToolsAbsentFromNextRun(t *testing.T) { /* actual MCP tools list changes */ }
```

The Connectors page renders real definitions, scopes, health, capabilities, errors, and remediation actions. It uses U00 locale/theme primitives and never shows `connected` before the authenticated health call succeeds.

- [ ] **Step 8: Verify and commit C08**

Run: `go test ./internal/connectors/... ./internal/harnessmcp ./internal/app -run 'TestConnector|TestDeniedExternal|TestDisabledConnector' -count=1 -v`

Run: `"D:\Program Files\nodejs\npm.cmd" --prefix frontend test -- ConnectorsPage`

Run each real platform integration only with dedicated test accounts. Missing accounts produce BLOCKED evidence.

```bash
git add internal/connectors internal/harnessmcp/connector_tools.go internal/harnessmcp/connector_tools_test.go internal/app/connector_api.go internal/app/connector_api_test.go internal/app/desktop.go internal/storage/migrations/003_connectors.sql frontend/src/features/connectors frontend/src/app/Router.tsx frontend/src/app/navigation.ts
git commit -m "C08: add policy-bound V1 connectors"
```

---

### Task C09: Persistent Local Scheduler

**Dependencies:** C08、F04、F05、F07、U00、U01。C09 在 C08 提交之后修改 `internal/app/desktop.go` 和前端导航，禁止二者并行写共享装配文件。

**Acceptance cases:** SCH01–SCH05、SCH07（G2）、SCH06（G3）；与 X01 共享 E2E07。

**Files:**
- Create: `internal/scheduler/types.go`
- Create: `internal/scheduler/store.go`
- Create: `internal/scheduler/clock.go`
- Create: `internal/scheduler/service.go`
- Create: `internal/scheduler/service_test.go`
- Create: `internal/scheduler/timezone_test.go`
- Create: `internal/storage/migrations/004_schedules.sql`
- Create: `internal/app/schedule_api.go`
- Create: `internal/app/schedule_api_test.go`
- Create: `frontend/src/features/schedules/SchedulesPage.tsx`
- Create: `frontend/src/features/schedules/ScheduleEditor.tsx`
- Create: `frontend/src/features/schedules/SchedulesPage.test.tsx`
- Modify: `frontend/src/app/Router.tsx`
- Modify: `frontend/src/app/navigation.ts`
- Modify: `internal/app/desktop.go`
- Modify: `internal/run/service.go`

**Interfaces:**
- Consumes: F05 `RunService`, Profile/workspace references, policy snapshot, SQLite.
- Produces: `Create`, `Update`, `Pause`, `Resume`, `Delete`, `RunNow`, `List`, and scheduler events linked to a normal session/run.

```go
type Schedule struct {
    ID string
    Name string
    Cron string
    TimeZone string
    WorkspaceID contracts.WorkspaceID
    ProfileID contracts.ProfileID
    Prompt string
    PermissionRef string
    OutputTarget string
    MisfirePolicy MisfirePolicy
    CatchUpLimit int
    Enabled bool
    Revision uint64
    LastScheduledAt time.Time
    NextScheduledAt time.Time
    CurrentSessionID contracts.SessionID
    CurrentRunID contracts.RunID
    ConsecutiveFailures int
}
```

- [ ] **Step 1: Write failing cron, timezone, and CAS tests**

```go
func TestRejectsInvalidCronAndTimezone(t *testing.T) { /* exact error codes */ }
func TestScheduleCASRejectsConcurrentEdit(t *testing.T) { /* one writer succeeds */ }
func TestDSTSpringForwardAndFallBackFireOnce(t *testing.T) { /* IANA zone, no duplicate */ }
```

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/scheduler -run 'TestRejectsInvalid|TestScheduleCAS|TestDST' -count=1 -v`

Expected: FAIL because scheduler storage and clock abstraction do not exist.

- [ ] **Step 3: Implement SQLite storage and deterministic clock**

The migration stores schedule definitions and run occurrences with a unique `(schedule_id, scheduled_at)` key. `Clock` exposes `Now()` and a wake signal so tests advance time without sleeping.

- [ ] **Step 4: Write failing queue, no-reentry, and missed-run tests**

```go
func TestSchedulerUsesGlobalRunLimitTwo(t *testing.T) { /* third occurrence queued */ }
func TestSameScheduleDoesNotOverlap(t *testing.T) { /* second occurrence recorded skipped */ }
func TestRestartAppliesConfiguredMisfirePolicy(t *testing.T) { /* one catch-up, not every missed tick */ }
func TestBackwardClockJumpDoesNotDuplicateOccurrence(t *testing.T) { /* unique occurrence key */ }
```

- [ ] **Step 5: Implement dispatch through normal RunService**

Create a normal session and `StartRunRequest`; never invoke Pi directly. Store schedule ID on the run. Reuse the global worker queue and record `queued`, `running`, `waiting_approval`, `completed`, `failed`, `cancelled`, or `skipped` from real events.

- [ ] **Step 6: Write and implement unattended-approval behavior**

```go
func TestUnattendedWriteWaitsForApproval(t *testing.T) { /* fixture counter zero */ }
func TestApprovalExpiryFailsOccurrenceWithoutReplay(t *testing.T) { /* no second call */ }
func TestTwoConsecutiveFailuresAutoPause(t *testing.T) { /* enabled becomes false by CAS */ }
```

Use the configured approval timeout. Never turn a scheduled run into full control merely because no UI is attached.

- [ ] **Step 7: Add app API, production page, and restart recovery**

The API returns stable DTOs with localized-error parameters, not translated Go messages. On startup, rebuild the next-wakeup heap from SQLite and reconcile in-flight occurrences with RunService; unknown writes remain `OutcomeUnknown`. The Schedules page shows real next/last times, linked run/session, approval and failure state, and supports create/edit/pause/resume/delete/run-now through the API.

- [ ] **Step 8: Verify and commit C09**

Run: `go test ./internal/scheduler ./internal/app ./internal/run -run 'TestSchedule|TestScheduler|TestDST|TestUnattended' -count=1 -v`

Run: `"D:\Program Files\nodejs\npm.cmd" --prefix frontend test -- SchedulesPage`

```bash
git add internal/scheduler internal/storage/migrations/004_schedules.sql internal/app/schedule_api.go internal/app/schedule_api_test.go internal/app/desktop.go internal/run/service.go frontend/src/features/schedules frontend/src/app/Router.tsx frontend/src/app/navigation.ts
git commit -m "C09: add persistent local scheduler"
```

---

## Plan Verification

1. Run `go test ./internal/connectors/... ./internal/scheduler ./internal/harnessmcp ./internal/app ./internal/run -count=1`.
2. Run `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static`.
3. Run `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=unit`.
4. Run connector fixture integrations on all six targets; remote test-account gaps remain BLOCKED and are listed per connector.
5. Run scheduler tests with deterministic clocks on all targets and native notification/status tests on the NATIVE matrix.
6. Confirm every denied write/external-send case has an external counter of zero and every allowed case has exactly one side effect.
