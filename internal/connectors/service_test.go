package connectors

// C08a 框架测试：注册表/能力范围/限流/策略门/审计/未知结果/秘密不泄漏。
//
// 这些用例用**假驱动**（只计数、不发包）来断言"拒绝时驱动一次都没被调用"，
// 真实 SMTP/IMAP 的副作用计数在 internal/connectors/mail 的用例里用 fixture 证明。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/storage"
)

// fakeDriver 只计数并按需返回预设结果。
type fakeDriver struct {
	kind     Kind
	caps     []Capability
	mu       sync.Mutex
	calls    int
	resultFn func(inv Invocation) Result
	healthFn func(cred Credential) Health
}

func (f *fakeDriver) Kind() Kind { return f.kind }

func (f *fakeDriver) Capabilities() []Capability {
	if f.caps != nil {
		return f.caps
	}
	return []Capability{CapabilityMailRead, CapabilityMailSend}
}

func (f *fakeDriver) Actions(ctx context.Context) ([]ActionDescriptor, error) {
	return Catalog(f.kind), nil
}

func (f *fakeDriver) Diagnose(ctx context.Context, cred Credential) (Health, error) {
	if f.healthFn != nil {
		return f.healthFn(cred), nil
	}
	return Health{Status: HealthConnected, Detail: "fake ok"}, nil
}

func (f *fakeDriver) Invoke(ctx context.Context, inv Invocation) (Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.resultFn != nil {
		return f.resultFn(inv), nil
	}
	return Result{Outcome: OutcomeDone, Dispatched: true, Detail: "ok"}, nil
}

func (f *fakeDriver) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// stubGate 是可以强制给出结论的策略门（用于测试 deny 分支）。
type stubGate struct {
	decision policy.Decision
	reason   string
}

func (g stubGate) Authorize(ctx context.Context, exec contracts.ExecutionContext, call policy.ToolCall) (policy.Result, error) {
	return policy.Result{Decision: g.decision, Reason: g.reason}, nil
}

func (g stubGate) Pending(ctx context.Context, id string) (policy.ApprovalRequest, error) {
	return policy.ApprovalRequest{}, contracts.NewAppError(contracts.CodeNotFound, "没有待审批", false)
}

func (g stubGate) ValidateBinding(ctx context.Context, exec contracts.ExecutionContext, id string, call policy.ToolCall) error {
	if g.decision == policy.DecisionDeny {
		return contracts.NewAppError(contracts.CodeUnauthorized, "策略拒绝", false)
	}
	return nil
}

func (g stubGate) ResolveApproval(ctx context.Context, id string, choice policy.ApprovalChoice, call policy.ToolCall) error {
	return nil
}

const testPassword = "S3NT1NEL-app-password-a7f3"

type testEnv struct {
	svc    *Service
	driver *fakeDriver
	store  *SQLStore
	creds  *credentials.MemStore
	pol    *policy.Service
	root   string
	db     *storage.DB
}

func newTestEnv(t *testing.T, tune func(*Options)) *testEnv {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := storage.Open(ctx, filepath.Join(root, "connectors.db"))
	if err != nil {
		t.Fatalf("打开连接器库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移连接器库: %v", err)
	}
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	creds := credentials.NewMemStore()
	pol := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace"},
		ApprovalTTL: 30 * time.Second,
	})
	driver := &fakeDriver{kind: KindMail}
	opts := Options{
		Definitions:  store,
		Audit:        store,
		Credentials:  creds,
		Policy:       pol,
		Drivers:      []Driver{driver},
		ApprovalWait: 2 * time.Second,
		PollInterval: 2 * time.Millisecond,
	}
	if tune != nil {
		tune(&opts)
	}
	svc, err := NewService(opts)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 默认模拟"用户在对话框里点了允许一次"。给拒绝分支用的 stubGate 不走这里。
	svc.SetPendingSink(func(pa PendingApproval) {
		if err := svc.ResolveApproval(context.Background(), pa.ApprovalID, policy.ApprovalOnce); err != nil {
			t.Errorf("自动批准失败: %v", err)
		}
	})
	return &testEnv{svc: svc, driver: driver, store: store, creds: creds, pol: pol, root: root, db: db}
}

// seedMail 写入一条可用定义（并可选调整）。
func (e *testEnv) seedMail(t *testing.T, edit func(*Definition)) Definition {
	t.Helper()
	def := Definition{
		ID:               "mail-1",
		Kind:             KindMail,
		DisplayName:      "邮箱",
		CredentialRef:    "connector:mail-1",
		Enabled:          true,
		Capabilities:     []Capability{CapabilityMailRead, CapabilityMailSend},
		AllowedResources: []string{"@fixture.test"},
		RateLimit:        RateLimit{Requests: 100, PerSeconds: 60},
	}
	if edit != nil {
		edit(&def)
	}
	if def.Enabled || len(def.Capabilities) > 0 {
		if err := e.creds.Put(context.Background(), def.CredentialRef, []byte(testPassword)); err != nil {
			t.Fatalf("写凭证: %v", err)
		}
	}
	saved, err := e.svc.SaveDefinition(context.Background(), def)
	if err != nil {
		t.Fatalf("保存定义: %v", err)
	}
	return saved
}

func (e *testEnv) invoke(t *testing.T, action string, args map[string]any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数: %v", err)
	}
	return e.svc.Invoke(context.Background(), InvokeRequest{
		Execution:   contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: "sess-1", RunID: "run-1", ConfigRevision: 7},
		ConnectorID: "mail-1",
		ActionID:    action,
		Arguments:   raw,
	})
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("期望 AppError(%s)，实际 %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
}

// TestConnectorRegistryDeclaresCapabilitiesAndScopes 覆盖 CONN04 的第一层：
// 只有 catalog 里的 kind/动作/能力可用，且"能力未授予"在驱动之前就被拒绝。
func TestConnectorRegistryDeclaresCapabilitiesAndScopes(t *testing.T) {
	env := newTestEnv(t, nil)
	ctx := context.Background()

	// 未实现的平台类型不得被接受成"已实现"（V1 只有 mail 与 C08b 的五个平台，
	// jira 永远不在这张表里——这里用它是为了让"不支持"有一个稳定样本）。
	unsupported := &fakeDriver{kind: Kind("jira")}
	err := env.svc.Register(unsupported)
	requireCode(t, err, contracts.CodeUnsupported)

	// 重复注册同一 kind：说不清是谁在发这封信，直接拒绝。
	dup := newTestEnv(t, nil)
	requireCode(t, dup.svc.Register(&fakeDriver{kind: KindMail}), contracts.CodeConfigConflict)

	// catalog 自洽：每个动作都声明风险/能力/幂等/schema。
	actions := Catalog(KindMail)
	ids := []string{}
	for _, a := range actions {
		ids = append(ids, a.ID)
		if a.Risk == "" || a.Capability == "" || a.Idempotency == "" || a.InputSchema == nil {
			t.Fatalf("动作 %s 声明不完整: %+v", a.ID, a)
		}
	}
	if strings.Join(ids, ",") != "search,read,attachment,send" {
		t.Fatalf("mail 动作集不对: %v", ids)
	}
	if send, ok := LookupAction(KindMail, "send"); !ok || send.Risk != RiskExternalSend || send.Idempotency != IdempotencyNone {
		t.Fatalf("send 必须是 external-send 且非幂等: %+v", send)
	}
	if send, _ := LookupAction(KindMail, "send"); send.Replayable("k") {
		t.Fatal("非幂等动作带任何键都不应被判定为可重放")
	}

	// 未知动作：结构化拒绝，驱动一次都没被调到。
	env.seedMail(t, nil)
	_, err = env.invoke(t, "draft", map[string]any{"uid": 1})
	requireCode(t, err, contracts.CodeUnsupported)
	if env.driver.CallCount() != 0 {
		t.Fatalf("未知动作不得触达驱动，实际调用 %d 次", env.driver.CallCount())
	}

	// 能力未授予：send 需要 mail.send，这里只授予读。
	env2 := newTestEnv(t, nil)
	env2.seedMail(t, func(d *Definition) {
		d.Capabilities = []Capability{CapabilityMailRead}
	})
	res, err := env2.invoke(t, "send", map[string]any{"to": []string{"a@fixture.test"}, "subject": "s", "body": "b"})
	if err != nil {
		t.Fatalf("能力未授予应以结果返回而不是协议错误: %v", err)
	}
	if res.Outcome != OutcomeFailed || !strings.Contains(res.Detail, "能力未授予") {
		t.Fatalf("期望能力未授予的失败结果，实际 %+v", res)
	}
	if env2.driver.CallCount() != 0 {
		t.Fatalf("能力未授予时不得触达驱动，实际 %d", env2.driver.CallCount())
	}
	rows, lerr := env2.store.ListAudits(ctx, "mail-1", 0)
	if lerr != nil {
		t.Fatalf("读审计: %v", lerr)
	}
	if len(rows) == 0 || rows[len(rows)-1].Outcome != "denied" {
		t.Fatalf("拒绝必须留审计，实际 %+v", rows)
	}

	// 未注册的连接器 ID：NOT_FOUND。
	_, err = env2.svc.Invoke(ctx, InvokeRequest{ConnectorID: "nope", ActionID: "read"})
	requireCode(t, err, contracts.CodeNotFound)

	// 身份字段不允许由参数指定（CONN08）。
	_, err = env.invoke(t, "read", map[string]any{"uid": 1, "sessionId": "other"})
	requireCode(t, err, contracts.CodeUnauthorized)
	if env.driver.CallCount() != 0 {
		t.Fatalf("身份字段被伪造时不得触达驱动，实际 %d", env.driver.CallCount())
	}
}

// TestConnectorWriteIsPolicyGatedAndAudited 覆盖 CONN02 的框架侧：拒绝时驱动 0 次，
// 允许一次时 1 次，且审计里出现决策与 credential ref（没有密码文本）。
func TestConnectorWriteIsPolicyGatedAndAudited(t *testing.T) {
	sendArgs := map[string]any{"to": []string{"ops@fixture.test"}, "subject": "周报", "body": "正文"}

	t.Run("deny", func(t *testing.T) {
		env := newTestEnv(t, func(o *Options) {
			o.Policy = stubGate{decision: policy.DecisionDeny, reason: "用户拒绝外部发送"}
		})
		env.seedMail(t, nil)
		res, err := env.invoke(t, "send", sendArgs)
		if err != nil {
			t.Fatalf("策略拒绝应以结果返回: %v", err)
		}
		if res.Outcome != OutcomeFailed || res.Dispatched {
			t.Fatalf("拒绝必须是未派发的失败: %+v", res)
		}
		if env.driver.CallCount() != 0 {
			t.Fatalf("策略拒绝时驱动调用必须为 0，实际 %d", env.driver.CallCount())
		}
		rows, _ := env.store.ListAudits(context.Background(), "mail-1", 0)
		if len(rows) == 0 {
			t.Fatal("拒绝必须留审计")
		}
		last := rows[len(rows)-1]
		if last.Decision != "deny" || last.Outcome != "denied" || last.CredentialRef != "connector:mail-1" {
			t.Fatalf("审计内容不对: %+v", last)
		}
		if last.ToolName != "connector_mail_send" {
			t.Fatalf("审计必须记录真实工具名: %+v", last)
		}
	})

	t.Run("allow-once", func(t *testing.T) {
		env := newTestEnv(t, nil)
		env.seedMail(t, nil)
		res, err := env.invoke(t, "send", sendArgs)
		if err != nil {
			t.Fatalf("允许路径不应报错: %v", err)
		}
		if res.Outcome != OutcomeDone || !res.Dispatched {
			t.Fatalf("期望完成，实际 %+v", res)
		}
		if env.driver.CallCount() != 1 {
			t.Fatalf("允许一次必须只派发一次，实际 %d", env.driver.CallCount())
		}
		rows, _ := env.store.ListAudits(context.Background(), "mail-1", 0)
		if len(rows) == 0 {
			t.Fatal("允许也必须有审计")
		}
		last := rows[len(rows)-1]
		if last.Decision != "allow" || last.Outcome != "done" {
			t.Fatalf("审计应记录 allow/done: %+v", last)
		}
		if last.CredentialRef != "connector:mail-1" {
			t.Fatalf("审计只能出现 credential ref: %+v", last)
		}
		body, _ := json.Marshal(last)
		if strings.Contains(string(body), testPassword) {
			t.Fatalf("审计里出现了凭证材料: %s", body)
		}
	})
}

// TestConnectorRateLimitBlocksBeforeDispatch 覆盖限流也发生在派发之前。
func TestConnectorRateLimitBlocksBeforeDispatch(t *testing.T) {
	env := newTestEnv(t, nil)
	env.seedMail(t, func(d *Definition) {
		d.RateLimit = RateLimit{Requests: 1, PerSeconds: 60}
	})
	readArgs := map[string]any{"uid": 1}
	if _, err := env.invoke(t, "read", readArgs); err != nil {
		t.Fatalf("第一次调用应成功: %v", err)
	}
	res, err := env.invoke(t, "read", readArgs)
	if err != nil {
		t.Fatalf("限流应以结果返回: %v", err)
	}
	if res.Outcome != OutcomeFailed || !strings.Contains(res.Detail, "限流") {
		t.Fatalf("期望限流失败，实际 %+v", res)
	}
	if env.driver.CallCount() != 1 {
		t.Fatalf("限流必须发生在派发前，实际驱动调用 %d", env.driver.CallCount())
	}
	rows, _ := env.store.ListAudits(context.Background(), "mail-1", 0)
	if len(rows) == 0 || rows[len(rows)-1].Outcome != "rate_limited" {
		t.Fatalf("限流必须留审计: %+v", rows)
	}
}

// TestConnectorOutcomeUnknownIsNotReplayed 覆盖 CONN06 的框架约定：未知结果不会被
// 服务自动重放，且结果里明确 SafeToRetry=false。
func TestConnectorOutcomeUnknownIsNotReplayed(t *testing.T) {
	env := newTestEnv(t, nil)
	env.driver.resultFn = func(inv Invocation) Result {
		return Result{Outcome: OutcomeUnknown, Dispatched: true, Detail: "连接在确认前断开"}
	}
	env.seedMail(t, nil)

	res, err := env.invoke(t, "send", map[string]any{"to": []string{"ops@fixture.test"}, "subject": "s", "body": "b"})
	if err != nil {
		t.Fatalf("未知结果不应是协议错误: %v", err)
	}
	if res.Outcome != OutcomeUnknown {
		t.Fatalf("期望 OutcomeUnknown，实际 %+v", res)
	}
	if res.SafeToRetry {
		t.Fatal("未知结果不得声明可安全重试")
	}
	// 再等一会：如果真的发生了自动重放，驱动计数会变成 2。
	time.Sleep(40 * time.Millisecond)
	if env.driver.CallCount() != 1 {
		t.Fatalf("未知结果不得自动重放，实际调用 %d 次", env.driver.CallCount())
	}
	rows, _ := env.store.ListAudits(context.Background(), "mail-1", 0)
	if len(rows) == 0 || rows[len(rows)-1].Outcome != string(OutcomeUnknown) {
		t.Fatalf("未知结果必须留审计: %+v", rows)
	}
}

// TestConnectorRejectsOutOfScopeRecipients 覆盖 CONN08 的资源范围：
// 不在 AllowedResources 里的收件人在派发前被拒。
func TestConnectorRejectsOutOfScopeRecipients(t *testing.T) {
	env := newTestEnv(t, nil)
	env.seedMail(t, func(d *Definition) {
		d.AllowedResources = []string{"@fixture.test"}
	})
	res, err := env.invoke(t, "send", map[string]any{"to": []string{"someone@elsewhere.test"}, "subject": "s", "body": "b"})
	if err != nil {
		t.Fatalf("越界应以结果返回: %v", err)
	}
	if res.Outcome != OutcomeFailed || !strings.Contains(res.Detail, "允许的资源范围") {
		t.Fatalf("期望资源范围拒绝，实际 %+v", res)
	}
	if env.driver.CallCount() != 0 {
		t.Fatalf("越界时不得触达驱动，实际 %d", env.driver.CallCount())
	}
	if ok, err := env.invoke(t, "send", map[string]any{"to": []string{"ok@fixture.test"}, "subject": "s", "body": "b"}); err != nil || ok.Outcome != OutcomeDone {
		t.Fatalf("范围内收件人应成功: %+v %v", ok, err)
	}
}

// TestConnectorHealthNeverGuessesConnected 覆盖 CONN01 的框架侧：
// 没有凭证就是 blocked，真实健康检查通过才是 connected。
func TestConnectorHealthNeverGuessesConnected(t *testing.T) {
	env := newTestEnv(t, nil)
	env.seedMail(t, nil)
	ctx := context.Background()
	if err := env.creds.Delete(ctx, "connector:mail-1"); err != nil {
		t.Fatalf("删除凭证: %v", err)
	}
	status, err := env.svc.Status(ctx, "mail-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Health.Status == HealthConnected {
		t.Fatalf("没有凭证时不得报告 connected: %+v", status.Health)
	}
	if status.Health.Remediation != RemediationAddCredentials {
		t.Fatalf("应提示重新授权: %+v", status.Health)
	}
	if err := env.creds.Put(ctx, "connector:mail-1", []byte(testPassword)); err != nil {
		t.Fatalf("写凭证: %v", err)
	}
	status, err = env.svc.Status(ctx, "mail-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Health.Status != HealthConnected {
		t.Fatalf("健康检查通过后应 connected: %+v", status.Health)
	}
	// 健康检查返回空状态时不得默认 connected。
	env.driver.healthFn = func(cred Credential) Health { return Health{} }
	health, err := env.svc.Health(ctx, "mail-1")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status == HealthConnected {
		t.Fatalf("驱动没给状态时必须按错误处理: %+v", health)
	}
}

// TestNoSecretLeaksIntoLogsConfigOrDTO 是 CONN03 的 sentinel 扫描：
// sentinel 只允许出现在凭证库里，配置/审计/DTO/审批载荷一律 0 命中。
func TestNoSecretLeaksIntoLogsConfigOrDTO(t *testing.T) {
	sentinel := testPassword
	var pendingJSON []byte
	env := newTestEnv(t, nil)
	env.svc.SetPendingSink(func(pa PendingApproval) {
		raw, _ := json.Marshal(pa)
		pendingJSON = raw
		if err := env.svc.ResolveApproval(context.Background(), pa.ApprovalID, policy.ApprovalOnce); err != nil {
			t.Errorf("批准失败: %v", err)
		}
	})
	env.seedMail(t, nil)

	// 驱动故意把密码写进结果：服务必须遮蔽并留审计说明。
	env.driver.resultFn = func(inv Invocation) Result {
		return Result{
			Outcome:    OutcomeFailed,
			Dispatched: true,
			Detail:     "smtp rejected password " + string(inv.Credential.Secret),
			Data:       json.RawMessage(`{"echo":"` + string(inv.Credential.Secret) + `"}`),
		}
	}
	res, err := env.invoke(t, "send", map[string]any{"to": []string{"ops@fixture.test"}, "subject": "s", "body": "b"})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	rawRes, _ := json.Marshal(res)
	if strings.Contains(string(rawRes), sentinel) {
		t.Fatalf("结果里出现了凭证材料: %s", rawRes)
	}
	if strings.Contains(res.Detail, sentinel) || strings.Contains(string(res.Data), sentinel) {
		t.Fatalf("驱动返回的凭证材料未被遮蔽: %+v", res)
	}

	// 配置（定义表 + 数据库文件字节）
	defs, err := env.svc.ListDefinitions(context.Background())
	if err != nil {
		t.Fatalf("ListDefinitions: %v", err)
	}
	rawDefs, _ := json.Marshal(defs)
	if strings.Contains(string(rawDefs), sentinel) {
		t.Fatalf("定义里出现了凭证材料: %s", rawDefs)
	}
	dbBytes := readDBFiles(t, env.root)
	if strings.Contains(dbBytes, sentinel) {
		t.Fatal("连接器数据库文件里出现了凭证材料")
	}

	// 审计
	rows, err := env.store.ListAudits(context.Background(), "mail-1", 0)
	if err != nil {
		t.Fatalf("ListAudits: %v", err)
	}
	rawRows, _ := json.Marshal(rows)
	if strings.Contains(string(rawRows), sentinel) {
		t.Fatalf("审计里出现了凭证材料: %s", rawRows)
	}
	if len(rows) == 0 {
		t.Fatal("审计为空")
	}

	// DTO（状态/动作）与审批载荷
	statuses, err := env.svc.ListStatuses(context.Background())
	if err != nil {
		t.Fatalf("ListStatuses: %v", err)
	}
	rawStatuses, _ := json.Marshal(statuses)
	if strings.Contains(string(rawStatuses), sentinel) {
		t.Fatalf("状态 DTO 里出现了凭证材料: %s", rawStatuses)
	}
	if len(pendingJSON) == 0 {
		t.Fatal("没有捕获到审批载荷")
	}
	if strings.Contains(string(pendingJSON), sentinel) {
		t.Fatalf("审批载荷里出现了凭证材料: %s", pendingJSON)
	}

	// 凭证库本身当然有它——否则上面的 0 命中就意味着"根本没存"。
	if _, err := env.creds.Get(context.Background(), "connector:mail-1"); err != nil {
		t.Fatalf("凭证库应保留凭证: %v", err)
	}
}

// readDBFiles 读回连接器库及其 WAL/SHM 的字节（SQLite 的写入可能还在 WAL 里）。
func readDBFiles(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	for _, name := range []string{"connectors.db", "connectors.db-wal", "connectors.db-shm"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		out.Write(data)
	}
	return out.String()
}

// TestConnectorDefinitionSaveIsCAS 覆盖并发编辑：旧 revision 必须失败。
func TestConnectorDefinitionSaveIsCAS(t *testing.T) {
	env := newTestEnv(t, nil)
	saved := env.seedMail(t, nil)
	if saved.Revision != 1 {
		t.Fatalf("首次保存 revision 应为 1，实际 %d", saved.Revision)
	}
	// 第二个写者先成功一次，把版本推到 2。
	current, err := env.svc.SaveDefinition(context.Background(), saved)
	if err != nil {
		t.Fatalf("第二次保存应成功: %v", err)
	}
	if current.Revision != 2 {
		t.Fatalf("revision 应递增到 2，实际 %d", current.Revision)
	}
	// 第三个写者仍拿着旧版本：必须失败，不能静默覆盖。
	stale := saved
	stale.DisplayName = "旧版本改名"
	_, err = env.svc.SaveDefinition(context.Background(), stale)
	requireCode(t, err, contracts.CodeStaleRevision)
}

// TestConnectorDriverCannotDowngradeRisk 覆盖 CONN04 的驱动侧约束：
// 驱动把非幂等动作说成幂等（或降低风险）时，服务拒绝执行。
func TestConnectorDriverCannotDowngradeRisk(t *testing.T) {
	// 用一个"自述更宽松"的驱动：send 说成 read-only 且带幂等键。
	liar := &lyingDriver{}
	env2 := newTestEnv(t, func(o *Options) { o.Drivers = []Driver{liar} })
	env2.seedMail(t, nil)
	_, err := env2.invoke(t, "send", map[string]any{"to": []string{"ops@fixture.test"}, "subject": "s", "body": "b"})
	requireCode(t, err, contracts.CodeUnsupported)
	if liar.calls != 0 {
		t.Fatalf("声明与 catalog 不一致时不得派发，实际 %d", liar.calls)
	}
}

// lyingDriver 声明 send 是只读且带幂等键。
type lyingDriver struct {
	calls int
}

func (l *lyingDriver) Kind() Kind { return KindMail }

func (l *lyingDriver) Capabilities() []Capability {
	return []Capability{CapabilityMailRead, CapabilityMailSend}
}

func (l *lyingDriver) Actions(ctx context.Context) ([]ActionDescriptor, error) {
	out := Catalog(KindMail)
	for i := range out {
		if out[i].ID == "send" {
			out[i].Risk = RiskRead
			out[i].Idempotency = IdempotencyKey
		}
	}
	return out, nil
}

func (l *lyingDriver) Diagnose(ctx context.Context, cred Credential) (Health, error) {
	return Health{Status: HealthConnected}, nil
}

func (l *lyingDriver) Invoke(ctx context.Context, inv Invocation) (Result, error) {
	l.calls++
	return Result{Outcome: OutcomeDone, Dispatched: true}, nil
}

// TestConnectorAuditIsAppendOnly 证明审计表在数据库层面拒绝改/删。
func TestConnectorAuditIsAppendOnly(t *testing.T) {
	env := newTestEnv(t, nil)
	env.seedMail(t, nil)
	if _, err := env.invoke(t, "read", map[string]any{"uid": 1}); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	ctx := context.Background()
	if _, err := env.db.SQL().ExecContext(ctx, `UPDATE connector_audit SET outcome = 'forged'`); err == nil {
		t.Fatal("审计表必须拒绝 UPDATE")
	}
	if _, err := env.db.SQL().ExecContext(ctx, `DELETE FROM connector_audit`); err == nil {
		t.Fatal("审计表必须拒绝 DELETE")
	}
}
