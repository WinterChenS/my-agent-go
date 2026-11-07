package harnessmcp

// C08a：连接器动作经自有 MCP 暴露时的策略门测试。
//
// 这条链路是真的：MCP HTTP 请求 → handler → connectors.Service → policy →
// mail driver → 本地 SMTP fixture。判定落在 fixture 的副作用计数上，
// 而不是"工具返回了 isError=true"。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/fixtures"
	"my-agent-go/internal/connectors/mail"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/runtime/pi"
	"my-agent-go/internal/storage"
)

const (
	connUser  = "user@fixture.test"
	connPass  = "fixture-app-password"
	connFrom  = "user@fixture.test"
	connDefID = "mail-mcp"
	connRef   = "connector:mail-mcp"
)

// connectorDenyGate 强制拒绝（模拟用户在审批里点了拒绝）。
type connectorDenyGate struct{}

func (connectorDenyGate) Authorize(ctx context.Context, exec contracts.ExecutionContext, call policy.ToolCall) (policy.Result, error) {
	return policy.Result{Decision: policy.DecisionDeny, Reason: "用户拒绝"}, nil
}

func (connectorDenyGate) Pending(ctx context.Context, id string) (policy.ApprovalRequest, error) {
	return policy.ApprovalRequest{}, contracts.NewAppError(contracts.CodeNotFound, "没有待审批", false)
}

func (connectorDenyGate) ValidateBinding(ctx context.Context, exec contracts.ExecutionContext, id string, call policy.ToolCall) error {
	return contracts.NewAppError(contracts.CodeUnauthorized, "策略拒绝", false)
}

func (connectorDenyGate) ResolveApproval(ctx context.Context, id string, choice policy.ApprovalChoice, call policy.ToolCall) error {
	return nil
}

// connectorFixture 起 SMTP/IMAP fixture 并返回副作用计数器。
func connectorFixture(t *testing.T) (*fixtures.SMTP, *fixtures.IMAP, *fixtures.Counter) {
	t.Helper()
	opts := fixtures.Options{Username: connUser, Password: connPass}
	counter := fixtures.NewCounter("")
	opts.Counter = counter
	smtp, err := fixtures.NewSMTP(opts)
	if err != nil {
		t.Fatalf("SMTP fixture: %v", err)
	}
	t.Cleanup(func() { _ = smtp.Close() })
	imap, err := fixtures.NewIMAP(opts)
	if err != nil {
		t.Fatalf("IMAP fixture: %v", err)
	}
	t.Cleanup(func() { _ = imap.Close() })
	return smtp, imap, counter
}

// newConnectorService 组一个可用的连接器服务（enabled + 已授权 + 端点指向 fixture）。
func newConnectorService(t *testing.T, smtp *fixtures.SMTP, imap *fixtures.IMAP, gate connectors.PolicyGate) *connectors.Service {
	t.Helper()
	ctx := context.Background()
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
	if err := creds.Put(ctx, connRef, []byte(connPass)); err != nil {
		t.Fatalf("写凭证: %v", err)
	}
	driver, err := mail.New(mail.Settings{Endpoints: connectors.Endpoints{
		IMAPAddr: imap.Addr(),
		SMTPAddr: smtp.Addr(),
		Username: connUser,
		From:     connFrom,
	}})
	if err != nil {
		t.Fatalf("mail.New: %v", err)
	}
	if gate == nil {
		gate = policy.New(policy.Options{
			Permissions: config.Permissions{Filesystem: "workspace"},
			ApprovalTTL: 30 * time.Second,
		})
	}
	svc, err := connectors.NewService(connectors.Options{
		Definitions:  store,
		Audit:        store,
		Credentials:  creds,
		Policy:       gate,
		Drivers:      []connectors.Driver{driver},
		ApprovalWait: 3 * time.Second,
		PollInterval: 2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SetPendingSink(func(pa connectors.PendingApproval) {
		if err := svc.ResolveApproval(context.Background(), pa.ApprovalID, policy.ApprovalOnce); err != nil {
			t.Errorf("自动批准失败: %v", err)
		}
	})
	if _, err := svc.SaveDefinition(ctx, connectors.Definition{
		ID:               connDefID,
		Kind:             connectors.KindMail,
		DisplayName:      "MCP 邮箱",
		CredentialRef:    connRef,
		Enabled:          true,
		Capabilities:     []connectors.Capability{connectors.CapabilityMailRead, connectors.CapabilityMailSend},
		AllowedResources: []string{"@fixture.test"},
		RateLimit:        connectors.RateLimit{Requests: 100, PerSeconds: 60},
		Endpoints: connectors.Endpoints{
			IMAPAddr: imap.Addr(),
			SMTPAddr: smtp.Addr(),
			Username: connUser,
			From:     connFrom,
		},
	}); err != nil {
		t.Fatalf("保存定义: %v", err)
	}
	return svc
}

func toolNamed(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl
		}
	}
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	t.Fatalf("工具 %s 不在注册表里（现有 %v）", name, names)
	return Tool{}
}

// TestConnectorMCPToolsArePolicyGated 是 brief 指定的用例。
func TestConnectorMCPToolsArePolicyGated(t *testing.T) {
	smtp, imap, counter := connectorFixture(t)
	binding := contracts.ExecutionContext{
		WorkspaceID: "ws-1", SessionID: "sess-mcp", RunID: "run-mcp", ConfigRevision: 11,
	}

	// 1) 策略拒绝：MCP 通道返回 isError，fixture 计数为 0。
	deniedSvc := newConnectorService(t, smtp, imap, connectorDenyGate{})
	deniedTools, err := ConnectorTools(deniedSvc, binding)
	if err != nil {
		t.Fatalf("ConnectorTools: %v", err)
	}
	deniedHarness := newHarnessWithTools(t, t.TempDir(), deniedTools)
	text, isErr := callTool(t, deniedHarness, "connector_mail_send", map[string]any{
		"to": []string{"ops@fixture.test"}, "subject": "被拒", "body": "正文",
	})
	if !isErr {
		t.Fatalf("策略拒绝必须是 isError 结果: %s", text)
	}
	if !strings.Contains(text, "未派发") {
		t.Fatalf("拒绝结果必须说明未派发: %s", text)
	}
	if got := counter.Snapshot().MailAccepted; got != 0 {
		t.Fatalf("策略拒绝时 fixture 计数必须为 0，实际 %d", got)
	}

	// 2) 允许：同一个工具名，fixture 计数为 1。
	allowedSvc := newConnectorService(t, smtp, imap, nil)
	allowedTools, err := ConnectorTools(allowedSvc, binding)
	if err != nil {
		t.Fatalf("ConnectorTools: %v", err)
	}
	allowedHarness := newHarnessWithTools(t, t.TempDir(), allowedTools)
	text, isErr = callTool(t, allowedHarness, "connector_mail_send", map[string]any{
		"to": []string{"ops@fixture.test"}, "subject": "允许", "body": "正文",
	})
	if isErr {
		t.Fatalf("允许路径不应 isError: %s", text)
	}
	if got := counter.Snapshot().MailAccepted; got != 1 {
		t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
	}

	// 3) 参数里的身份字段必须被拒（CONN08）：仍不产生副作用。
	text, isErr = callTool(t, allowedHarness, "connector_mail_send", map[string]any{
		"to": []string{"ops@fixture.test"}, "subject": "伪造", "body": "正文", "sessionId": "sess-other",
	})
	if !isErr || !strings.Contains(text, "执行身份") {
		t.Fatalf("伪造身份必须被拒: %s", text)
	}
	if got := counter.Snapshot().MailAccepted; got != 1 {
		t.Fatalf("伪造身份不得产生副作用，实际 %d", got)
	}

	// 4) 禁用的连接器不出现在工具表里（CONN05 的框架侧）。
	if _, err := allowedSvc.SaveDefinition(context.Background(), connectors.Definition{
		ID:               connDefID,
		Kind:             connectors.KindMail,
		DisplayName:      "MCP 邮箱",
		CredentialRef:    connRef,
		Enabled:          false,
		Capabilities:     []connectors.Capability{connectors.CapabilityMailRead, connectors.CapabilityMailSend},
		AllowedResources: []string{"@fixture.test"},
		Endpoints: connectors.Endpoints{
			IMAPAddr: imap.Addr(),
			SMTPAddr: smtp.Addr(),
			Username: connUser,
			From:     connFrom,
		},
		RateLimit: connectors.RateLimit{Requests: 100, PerSeconds: 60},
		Revision:  1,
	}); err != nil {
		t.Fatalf("禁用定义: %v", err)
	}
	afterDisable, err := ConnectorTools(allowedSvc, binding)
	if err != nil {
		t.Fatalf("ConnectorTools: %v", err)
	}
	for _, tl := range afterDisable {
		if strings.HasPrefix(tl.Name, "connector_mail_") {
			t.Fatalf("禁用后连接器工具必须消失: %s", tl.Name)
		}
	}
}

// TestConnectorMCPConfigCarriesNoToken 断言写进 agentDir 的 mcp.json 只有令牌引用。
func TestConnectorMCPConfigCarriesNoToken(t *testing.T) {
	smtp, imap, _ := connectorFixture(t)
	svc := newConnectorService(t, smtp, imap, nil)
	tools, err := ConnectorTools(svc, contracts.ExecutionContext{SessionID: "sess-mcp", RunID: "run-mcp"})
	if err != nil {
		t.Fatalf("ConnectorTools: %v", err)
	}
	toolNamed(t, tools, "connector_mail_send")

	h := newHarnessWithTools(t, t.TempDir(), tools)
	agentDir := t.TempDir()
	if err := pi.WriteHarnessMCPConfig(agentDir, h.url, "Anvil 自有工具与连接器动作"); err != nil {
		t.Fatalf("写 mcp.json: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(agentDir, "mcp.json"))
	if err != nil {
		t.Fatalf("读 mcp.json: %v", err)
	}
	if !strings.Contains(string(raw), `"harness"`) {
		t.Fatalf("mcp.json 缺少 harness 条目: %s", raw)
	}
	if h.token == "" {
		t.Fatal("测试自身没有拿到 MCP 令牌")
	}
	if strings.Contains(string(raw), h.token) {
		t.Fatalf("mcp.json 里出现了明文令牌: %s", raw)
	}
	if !strings.Contains(string(raw), "${MYAGENT_HARNESS_TOKEN}") {
		t.Fatalf("mcp.json 应引用环境变量: %s", raw)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("mcp.json 不是合法 JSON: %v", err)
	}
	servers := root["mcpServers"].(map[string]any)
	entry := servers["harness"].(map[string]any)
	if entry["exposure"] != "direct" {
		t.Fatalf("exposure 必须是 direct: %v", entry)
	}
	if entry["url"] != h.url {
		t.Fatalf("url 不对: %v", entry["url"])
	}
}
