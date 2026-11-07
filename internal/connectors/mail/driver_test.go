package mail

// C08a 的 mail 适配器测试：真实 SMTP/IMAP fixture 上的读/写/健康/未知结果，
// 以及 CONN02 的"拒绝时副作用计数为 0、允许一次为 1"。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/fixtures"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/storage"
)

const (
	fxUser     = "user@fixture.test"
	fxPassword = "fixture-app-password"
	fxFrom     = "user@fixture.test"
	defID      = "mail-1"
	defRef     = "connector:mail-1"
)

type mailEnv struct {
	smtp    *fixtures.SMTP
	imap    *fixtures.IMAP
	counter *fixtures.Counter
	driver  *Driver
	svc     *connectors.Service
	store   *connectors.SQLStore
	creds   *credentials.MemStore
}

// denyGate 强制拒绝（模拟用户在审批对话框里拒绝）。
type denyGate struct{}

func (denyGate) Authorize(ctx context.Context, exec contracts.ExecutionContext, call policy.ToolCall) (policy.Result, error) {
	return policy.Result{Decision: policy.DecisionDeny, Reason: "用户拒绝外部发送"}, nil
}

func (denyGate) Pending(ctx context.Context, id string) (policy.ApprovalRequest, error) {
	return policy.ApprovalRequest{}, contracts.NewAppError(contracts.CodeNotFound, "没有待审批", false)
}

func (denyGate) ValidateBinding(ctx context.Context, exec contracts.ExecutionContext, id string, call policy.ToolCall) error {
	return contracts.NewAppError(contracts.CodeUnauthorized, "策略拒绝", false)
}

func (denyGate) ResolveApproval(ctx context.Context, id string, choice policy.ApprovalChoice, call policy.ToolCall) error {
	return nil
}

func newMailEnv(t *testing.T, fxOpts fixtures.Options, gate connectors.PolicyGate, tune func(*connectors.Options)) *mailEnv {
	t.Helper()
	ctx := context.Background()
	if fxOpts.Username == "" {
		fxOpts.Username = fxUser
	}
	if fxOpts.Password == "" {
		fxOpts.Password = fxPassword
	}
	counter := fixtures.NewCounter(fxOpts.ObservationDir)
	fxOpts.Counter = counter

	smtp, err := fixtures.NewSMTP(fxOpts)
	if err != nil {
		t.Fatalf("启动 SMTP fixture: %v", err)
	}
	t.Cleanup(func() { _ = smtp.Close() })
	imap, err := fixtures.NewIMAP(fxOpts)
	if err != nil {
		t.Fatalf("启动 IMAP fixture: %v", err)
	}
	t.Cleanup(func() { _ = imap.Close() })

	driver, err := New(Settings{Endpoints: connectors.Endpoints{
		IMAPAddr: imap.Addr(),
		SMTPAddr: smtp.Addr(),
		Username: fxUser,
		From:     fxFrom,
	}})
	if err != nil {
		t.Fatalf("构造驱动: %v", err)
	}

	root := t.TempDir()
	db, err := storage.Open(ctx, filepath.Join(root, "connectors.db"))
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	store, err := connectors.NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	creds := credentials.NewMemStore()
	if err := creds.Put(ctx, defRef, []byte(fxPassword)); err != nil {
		t.Fatalf("写凭证: %v", err)
	}
	if gate == nil {
		gate = policy.New(policy.Options{
			Permissions: config.Permissions{Filesystem: "workspace"},
			ApprovalTTL: 30 * time.Second,
		})
	}
	opts := connectors.Options{
		Definitions:  store,
		Audit:        store,
		Credentials:  creds,
		Policy:       gate,
		Drivers:      []connectors.Driver{driver},
		ApprovalWait: 3 * time.Second,
		PollInterval: 2 * time.Millisecond,
	}
	if tune != nil {
		tune(&opts)
	}
	svc, err := connectors.NewService(opts)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetPendingSink(func(pa connectors.PendingApproval) {
		if err := svc.ResolveApproval(context.Background(), pa.ApprovalID, policy.ApprovalOnce); err != nil {
			t.Errorf("自动批准失败: %v", err)
		}
	})
	if _, err := svc.SaveDefinition(ctx, connectors.Definition{
		ID:               defID,
		Kind:             connectors.KindMail,
		DisplayName:      "测试邮箱",
		CredentialRef:    defRef,
		Enabled:          true,
		Capabilities:     []connectors.Capability{connectors.CapabilityMailRead, connectors.CapabilityMailSend},
		AllowedResources: []string{"@fixture.test"},
		RateLimit:        connectors.RateLimit{Requests: 100, PerSeconds: 60},
		Endpoints: connectors.Endpoints{
			IMAPAddr: imap.Addr(),
			SMTPAddr: smtp.Addr(),
			Username: fxUser,
			From:     fxFrom,
		},
	}); err != nil {
		t.Fatalf("保存定义: %v", err)
	}
	return &mailEnv{smtp: smtp, imap: imap, counter: counter, driver: driver, svc: svc, store: store, creds: creds}
}

func (e *mailEnv) invoke(ctx context.Context, action string, args map[string]any) (connectors.Result, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return connectors.Result{}, err
	}
	return e.svc.Invoke(ctx, connectors.InvokeRequest{
		Execution:   contracts.ExecutionContext{WorkspaceID: "ws-1", SessionID: "sess-1", RunID: "run-1", ConfigRevision: 3},
		ConnectorID: defID,
		ActionID:    action,
		Arguments:   raw,
	})
}

// TestMailReadActionsUseFixtureWithoutWriteSideEffects 覆盖读路径：
// 搜索/读取/取附件都成功，且外部写计数保持 0。
func TestMailReadActionsUseFixtureWithoutWriteSideEffects(t *testing.T) {
	env := newMailEnv(t, fixtures.Options{}, nil, nil)
	ctx := context.Background()

	res, err := env.invoke(ctx, "search", map[string]any{"query": "周报"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Outcome != connectors.OutcomeDone {
		t.Fatalf("search 应完成: %+v", res)
	}
	var found struct {
		Messages []struct {
			UID     int    `json:"uid"`
			Subject string `json:"subject"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(res.Data, &found); err != nil {
		t.Fatalf("解析 search 数据: %v", err)
	}
	if len(found.Messages) != 1 || found.Messages[0].UID != 101 {
		t.Fatalf("搜索应命中 UID 101: %+v", found)
	}

	res, err = env.invoke(ctx, "read", map[string]any{"uid": 101})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.Outcome != connectors.OutcomeDone || !strings.Contains(string(res.Data), "本周进度") {
		t.Fatalf("read 应返回正文: %+v", res)
	}
	var detail struct {
		Attachments []struct {
			Name string `json:"name"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(res.Data, &detail); err != nil {
		t.Fatalf("解析 read 数据: %v", err)
	}
	if len(detail.Attachments) != 1 || detail.Attachments[0].Name != "report.txt" {
		t.Fatalf("read 应列出附件元数据: %+v", detail)
	}

	res, err = env.invoke(ctx, "attachment", map[string]any{"uid": 101, "name": "report.txt"})
	if err != nil {
		t.Fatalf("attachment: %v", err)
	}
	var att struct {
		Name    string `json:"name"`
		DataB64 string `json:"dataBase64"`
		Size    int    `json:"size"`
	}
	if err := json.Unmarshal(res.Data, &att); err != nil {
		t.Fatalf("解析附件: %v", err)
	}
	body, err := base64.StdEncoding.DecodeString(att.DataB64)
	if err != nil {
		t.Fatalf("附件 base64 解不开: %v", err)
	}
	if string(body) != "report-body\n" {
		t.Fatalf("附件内容不对: %q", string(body))
	}

	snap := env.counter.Snapshot()
	if snap.ExternalWrites() != 0 {
		t.Fatalf("读动作不得产生外部写副作用: %+v", snap)
	}
	if snap.MailRead == 0 || snap.MailSearched == 0 {
		t.Fatalf("读动作应在 fixture 侧计数: %+v", snap)
	}
}

// TestMailSendIsGatedByPolicy 是 brief 指定的用例：
// 拒绝 → fixture 计数 0；允许 → 计数 1；审计里有决策与 credential ref，没有密码。
func TestMailSendIsGatedByPolicy(t *testing.T) {
	sendArgs := map[string]any{"to": []string{"ops@fixture.test"}, "subject": "周报-测试", "body": "正文"}

	t.Run("denied", func(t *testing.T) {
		env := newMailEnv(t, fixtures.Options{}, denyGate{}, nil)
		res, err := env.invoke(context.Background(), "send", sendArgs)
		if err != nil {
			t.Fatalf("拒绝应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("拒绝必须未派发: %+v", res)
		}
		snap := env.counter.Snapshot()
		if snap.MailAccepted != 0 {
			t.Fatalf("策略拒绝时 fixture 计数必须为 0: %+v", snap)
		}
		if len(env.smtp.Accepted()) != 0 {
			t.Fatal("策略拒绝时不应有任何邮件被接收")
		}
		rows, err := env.store.ListAudits(context.Background(), defID, 0)
		if err != nil {
			t.Fatalf("读审计: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("应有一条拒绝审计: %+v", rows)
		}
		if rows[0].Decision != "deny" || rows[0].CredentialRef != defRef {
			t.Fatalf("审计内容不对: %+v", rows[0])
		}
		raw, _ := json.Marshal(rows[0])
		if strings.Contains(string(raw), fxPassword) {
			t.Fatalf("审计含密码: %s", raw)
		}
	})

	t.Run("allowed", func(t *testing.T) {
		env := newMailEnv(t, fixtures.Options{}, nil, nil)
		res, err := env.invoke(context.Background(), "send", sendArgs)
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone || !res.Dispatched {
			t.Fatalf("允许后应完成: %+v", res)
		}
		snap := env.counter.Snapshot()
		if snap.MailAccepted != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用: %+v", snap)
		}
		accepted := env.smtp.Accepted()
		if len(accepted) != 1 || accepted[0].Subject != "周报-测试" {
			t.Fatalf("fixture 收到的邮件不对: %+v", accepted)
		}
		if accepted[0].To[0] != "ops@fixture.test" {
			t.Fatalf("收件人不对: %+v", accepted[0])
		}
		rows, _ := env.store.ListAudits(context.Background(), defID, 0)
		if len(rows) != 1 || rows[0].Decision != "allow" || rows[0].Outcome != "done" {
			t.Fatalf("允许必须留 allow/done 审计: %+v", rows)
		}
		if rows[0].CredentialRef != defRef {
			t.Fatalf("审计只允许出现 credential ref: %+v", rows[0])
		}
		raw, _ := json.Marshal(rows[0])
		if strings.Contains(string(raw), fxPassword) {
			t.Fatalf("审计含密码: %s", raw)
		}
	})
}

// TestMailSendTimeoutIsOutcomeUnknown 是 brief 指定的用例：
// fixture 接收后卡住不确认 → OutcomeUnknown；且不会发生第二次发送。
func TestMailSendTimeoutIsOutcomeUnknown(t *testing.T) {
	env := newMailEnv(t, fixtures.Options{StallAfterData: true}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	res, err := env.invoke(ctx, "send", map[string]any{
		"to": []string{"ops@fixture.test"}, "subject": "超时用例", "body": "正文",
	})
	if err != nil {
		t.Fatalf("未知结果不应是协议错误: %v", err)
	}
	if res.Outcome != connectors.OutcomeUnknown {
		t.Fatalf("派发后超时必须返回 OutcomeUnknown，实际 %+v", res)
	}
	if res.SafeToRetry {
		t.Fatal("未知结果不得声明可安全重试")
	}
	snap := env.counter.Snapshot()
	if snap.MailAccepted != 1 {
		t.Fatalf("fixture 应已接收这封信（外部 oracle 看到 1 次副作用）: %+v", snap)
	}
	// 再等一会：服务若自动重放，fixture 计数会变成 2。
	time.Sleep(150 * time.Millisecond)
	if got := env.counter.Snapshot().MailAccepted; got != 1 {
		t.Fatalf("非幂等动作不得自动重放，fixture 计数变成 %d", got)
	}
	if len(env.smtp.Accepted()) != 1 {
		t.Fatalf("fixture 只应收到一封: %d", len(env.smtp.Accepted()))
	}
	rows, _ := env.store.ListAudits(context.Background(), defID, 0)
	if len(rows) != 1 || rows[0].Outcome != string(connectors.OutcomeUnknown) {
		t.Fatalf("未知结果必须留审计: %+v", rows)
	}
}

// TestMailHealthRequiresAuthenticatedCall 覆盖 CONN01 的驱动侧：
// 只有真实认证通过才 connected；密码不对是 blocked，缺凭证也是 blocked。
func TestMailHealthRequiresAuthenticatedCall(t *testing.T) {
	env := newMailEnv(t, fixtures.Options{}, nil, nil)
	ctx := context.Background()

	health, err := env.svc.Health(ctx, defID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status != connectors.HealthConnected {
		t.Fatalf("正确凭证应 connected: %+v", health)
	}

	if err := env.creds.Put(ctx, defRef, []byte("wrong-password")); err != nil {
		t.Fatalf("写错凭证: %v", err)
	}
	health, err = env.svc.Health(ctx, defID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status == connectors.HealthConnected {
		t.Fatalf("认证失败不得报告 connected: %+v", health)
	}
	if health.Remediation != connectors.RemediationAddCredentials {
		t.Fatalf("认证失败应提示补凭证: %+v", health)
	}

	if err := env.creds.Delete(ctx, defRef); err != nil {
		t.Fatalf("删凭证: %v", err)
	}
	status, err := env.svc.Status(ctx, defID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Health.Status == connectors.HealthConnected {
		t.Fatalf("没有凭证不得 connected: %+v", status.Health)
	}
	// 缺凭证时的调用必须"未派发"。
	res, err := env.invoke(ctx, "read", map[string]any{"uid": 101})
	if err != nil {
		t.Fatalf("缺凭证应以结果返回: %v", err)
	}
	if res.Outcome != connectors.OutcomeFailed || res.Dispatched || !strings.Contains(res.Detail, "未派发") {
		t.Fatalf("缺凭证必须未派发: %+v", res)
	}
}

// TestMailReadFailureIsSafeToRetry 覆盖读路径的失败语义：未派发 → 可安全再试。
func TestMailReadFailureIsSafeToRetry(t *testing.T) {
	env := newMailEnv(t, fixtures.Options{}, nil, nil)
	ctx := context.Background()
	if err := env.creds.Put(ctx, defRef, []byte("wrong-password")); err != nil {
		t.Fatalf("写错凭证: %v", err)
	}
	res, err := env.invoke(ctx, "read", map[string]any{"uid": 101})
	if err != nil {
		t.Fatalf("调用: %v", err)
	}
	if res.Outcome != connectors.OutcomeFailed {
		t.Fatalf("认证失败应是确定失败: %+v", res)
	}
	if !res.SafeToRetry {
		t.Fatalf("未派发的读动作可以安全再试: %+v", res)
	}
	if snap := env.counter.Snapshot(); snap.ExternalWrites() != 0 {
		t.Fatalf("读失败不得产生外部写: %+v", snap)
	}
}

// TestConnectorFixtureObservation 是 brief 指定的用例：设置了 ANVIL_OBSERVATION_DIR
// 之后，fixture 写 connector-counter.json、服务写 audit.jsonl，事实从文件读回。
func TestConnectorFixtureObservation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(connectors.ObservationDirEnv, dir)
	env := newMailEnv(t, fixtures.Options{ObservationDir: dir}, nil, func(o *connectors.Options) {
		// 故意不传 Audit：让服务按观察目录自动使用 JSONL 审计。
		o.Audit = nil
		o.ObservationDir = dir
	})
	res, err := env.invoke(context.Background(), "send", map[string]any{
		"to": []string{"ops@fixture.test"}, "subject": "观察用例", "body": "正文",
	})
	if err != nil || res.Outcome != connectors.OutcomeDone {
		t.Fatalf("send: %+v %v", res, err)
	}

	counterPath := filepath.Join(dir, connectors.ObservationCounterFile)
	rawCounter, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("fixture 没有写 %s: %v", counterPath, err)
	}
	var snap fixtures.Snapshot
	if err := json.Unmarshal(rawCounter, &snap); err != nil {
		t.Fatalf("计数快照不是合法 JSON: %v", err)
	}
	if snap.MailAccepted != 1 {
		t.Fatalf("从文件读回的外部副作用应为 1，实际 %+v", snap)
	}

	auditPath := filepath.Join(dir, connectors.ObservationAuditFile)
	rawAudit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("没有写 %s: %v", auditPath, err)
	}
	lines := strings.Split(strings.TrimSpace(string(rawAudit)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		t.Fatal("audit.jsonl 为空")
	}
	var entry connectors.AuditEntry
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatalf("审计行不是合法 JSON: %v", err)
	}
	if entry.Decision != "allow" || entry.Outcome != "done" || entry.CredentialRef != defRef {
		t.Fatalf("审计行内容不对: %+v", entry)
	}
	if strings.Contains(string(rawAudit), fxPassword) {
		t.Fatal("audit.jsonl 里出现了凭证材料")
	}
}

// TestMailDriverRejectsUnsupportedAction 保证未知动作不会落到"什么也没做却返回成功"。
func TestMailDriverRejectsUnsupportedAction(t *testing.T) {
	env := newMailEnv(t, fixtures.Options{}, nil, nil)
	_, err := env.driver.Invoke(context.Background(), connectors.Invocation{
		ActionID:   "draft",
		Credential: connectors.Credential{Ref: defRef, Secret: []byte(fxPassword)},
	})
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr.Code != contracts.CodeUnsupported {
		t.Fatalf("期望 UNSUPPORTED，实际 %v", err)
	}
}

// TestConnectorDriverFactoryKeepsDefinitionsApart 覆盖"同一个 kind 的两个定义"：
// 端点属于定义，驱动缓存不能按 kind 共享，否则 B 的健康检查会打到 A 的服务器。
func TestConnectorDriverFactoryKeepsDefinitionsApart(t *testing.T) {
	ctx := context.Background()

	// 两个互不相通的邮箱 fixture：A 用正确口令，B 用另一个口令。
	optsA := fixtures.Options{Username: fxUser, Password: fxPassword}
	optsA.Counter = fixtures.NewCounter("")
	smtpA, err := fixtures.NewSMTP(optsA)
	if err != nil {
		t.Fatalf("SMTP A: %v", err)
	}
	t.Cleanup(func() { _ = smtpA.Close() })
	imapA, err := fixtures.NewIMAP(optsA)
	if err != nil {
		t.Fatalf("IMAP A: %v", err)
	}
	t.Cleanup(func() { _ = imapA.Close() })

	optsB := fixtures.Options{Username: fxUser, Password: "another-password"}
	optsB.Counter = fixtures.NewCounter("")
	smtpB, err := fixtures.NewSMTP(optsB)
	if err != nil {
		t.Fatalf("SMTP B: %v", err)
	}
	t.Cleanup(func() { _ = smtpB.Close() })
	imapB, err := fixtures.NewIMAP(optsB)
	if err != nil {
		t.Fatalf("IMAP B: %v", err)
	}
	t.Cleanup(func() { _ = imapB.Close() })

	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "connectors.db"))
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	store, err := connectors.NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	creds := credentials.NewMemStore()
	if err := creds.Put(ctx, "connector:a", []byte(fxPassword)); err != nil {
		t.Fatalf("写凭证 A: %v", err)
	}
	if err := creds.Put(ctx, "connector:b", []byte("another-password")); err != nil {
		t.Fatalf("写凭证 B: %v", err)
	}
	pol := policy.New(policy.Options{Permissions: config.Permissions{Filesystem: "workspace"}})
	svc, err := connectors.NewService(connectors.Options{
		Definitions: store,
		Audit:       store,
		Credentials: creds,
		Policy:      pol,
		DriverFactory: func(def connectors.Definition) (connectors.Driver, error) {
			return New(Settings{Endpoints: def.Endpoints})
		},
		ApprovalWait: 2 * time.Second,
		PollInterval: 2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 两个定义的 revision 都是 1，端点不同：缓存必须按定义区分。
	mk := func(id, ref string, imapAddr, smtpAddr string) {
		if _, err := svc.SaveDefinition(ctx, connectors.Definition{
			ID:               id,
			Kind:             connectors.KindMail,
			DisplayName:      id,
			CredentialRef:    ref,
			Enabled:          true,
			Capabilities:     []connectors.Capability{connectors.CapabilityMailRead},
			AllowedResources: []string{"@fixture.test"},
			RateLimit:        connectors.RateLimit{Requests: 10, PerSeconds: 60},
			Endpoints:        connectors.Endpoints{IMAPAddr: imapAddr, SMTPAddr: smtpAddr, Username: fxUser, From: fxFrom},
		}); err != nil {
			t.Fatalf("保存定义 %s: %v", id, err)
		}
	}
	mk("mail-a", "connector:a", imapA.Addr(), smtpA.Addr())
	mk("mail-b", "connector:b", imapB.Addr(), smtpB.Addr())

	healthA, err := svc.Health(ctx, "mail-a")
	if err != nil {
		t.Fatalf("Health A: %v", err)
	}
	healthB, err := svc.Health(ctx, "mail-b")
	if err != nil {
		t.Fatalf("Health B: %v", err)
	}
	if healthA.Status != connectors.HealthConnected || healthB.Status != connectors.HealthConnected {
		t.Fatalf("两个定义都配置正确时应各自 connected: A=%+v B=%+v", healthA, healthB)
	}
	// B 的口令被换掉：只有 B 变 blocked，A 不受影响。
	if err := creds.Put(ctx, "connector:b", []byte("now-wrong")); err != nil {
		t.Fatalf("改 B 凭证: %v", err)
	}
	healthA, _ = svc.Health(ctx, "mail-a")
	healthB, _ = svc.Health(ctx, "mail-b")
	if healthA.Status != connectors.HealthConnected {
		t.Fatalf("A 的定义/凭证没变，应仍 connected: %+v", healthA)
	}
	if healthB.Status == connectors.HealthConnected {
		t.Fatalf("B 的口令不对，不得 connected: %+v", healthB)
	}
}
