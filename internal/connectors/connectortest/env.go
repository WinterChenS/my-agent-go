// Package connectortest 是五个 HTTP 平台适配器共用的测试装配。
//
// 它只做一件事：把「Service + SQLStore + OS 凭证库替身 + 策略门 + 一个真驱动」
// 拼成可调用的整体，让每个适配器的用例只关心自己的协议形状与断言，而不是
// 重复一百多行装配代码。
//
// 生产代码不引用这个包（只有 _test.go 引用），它也不提供任何"放行"能力：
// 拒绝分支用的是硬拒绝的策略门，允许分支用的是真 policy.Service。
package connectortest

// 注意：本包刻意调用 t.Helper()/t.Fatalf，因此签名里带 testing.T。
// 它不是产品路径，不参与任何构建标签开关——漏引用不会影响二进制。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/connectors"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/storage"
)

// DefaultDefinitionID 是测试定义的 ID。
const DefaultDefinitionID = "conn-1"

// DefaultCredentialRef 是测试凭证的 ref（唯一出现在配置/审计里的凭证标识）。
const DefaultCredentialRef = "connector:" + DefaultDefinitionID

// Config 描述一次测试装配。
type Config struct {
	// Kind 是连接器类型。
	Kind connectors.Kind
	// Endpoints 是定义里的非敏感端点（APIBase 等）。
	Endpoints connectors.Endpoints
	// Capabilities 是授予的能力。
	Capabilities []connectors.Capability
	// AllowedResources 是允许触达的资源范围。
	AllowedResources []string
	// RateLimit 为零值时用 100 次/60 秒（避免用例被默认限流干扰）。
	RateLimit connectors.RateLimit
	// Credential 是要放进凭证库的凭证材料。
	Credential []byte
	// Deny 为 true 时使用硬拒绝的策略门（每次调用都 deny）。
	Deny bool
	// Drivers 是显式注册的驱动；为空时用 Build 按定义构造。
	Drivers []connectors.Driver
	// Build 按定义构造驱动（生产中由 app 的装配代码提供）。
	Build func(def connectors.Definition) (connectors.Driver, error)
	// Audit 覆盖审计存储（默认为 SQLStore）。
	Audit connectors.AuditStore
	// ObservationDir 覆盖观察目录（未设置时用 t.TempDir 里的空目录以免污染环境）。
	ObservationDir string
	// Tune 用于调整 Service 选项。
	Tune func(*connectors.Options)
}

// Env 是一次装配好的环境。
type Env struct {
	Svc          *connectors.Service
	Store        *connectors.SQLStore
	Creds        *credentials.MemStore
	DefinitionID string

	approvals atomic.Int32
	denials   atomic.Int32
}

// DenyGate 是硬拒绝的策略门（模拟用户在审批框里拒绝）。
type DenyGate struct {
	denials *atomic.Int32
}

// Authorize 永远拒绝。
func (g DenyGate) Authorize(ctx context.Context, exec contracts.ExecutionContext, call policy.ToolCall) (policy.Result, error) {
	if g.denials != nil {
		g.denials.Add(1)
	}
	return policy.Result{Decision: policy.DecisionDeny, Reason: "测试：用户拒绝外部资源访问"}, nil
}

// Pending 没有待审批。
func (g DenyGate) Pending(ctx context.Context, id string) (policy.ApprovalRequest, error) {
	return policy.ApprovalRequest{}, contracts.NewAppError(contracts.CodeNotFound, "没有待审批", false)
}

// ValidateBinding 一律拒绝。
func (g DenyGate) ValidateBinding(ctx context.Context, exec contracts.ExecutionContext, id string, call policy.ToolCall) error {
	return contracts.NewAppError(contracts.CodeUnauthorized, "策略拒绝", false)
}

// ResolveApproval 无事可做。
func (g DenyGate) ResolveApproval(ctx context.Context, id string, choice policy.ApprovalChoice, call policy.ToolCall) error {
	return nil
}

// New 装配环境并保存一条可用定义。
func New(t *testing.T, cfg Config) *Env {
	t.Helper()
	ctx := context.Background()
	env := &Env{DefinitionID: DefaultDefinitionID}

	root := t.TempDir()
	db, err := storage.Open(ctx, filepath.Join(root, "connectors.db"))
	if err != nil {
		t.Fatalf("打开连接器库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移连接器库: %v", err)
	}
	store, err := connectors.NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	env.Store = store

	creds := credentials.NewMemStore()
	env.Creds = creds
	if len(cfg.Credential) > 0 {
		if err := creds.Put(ctx, DefaultCredentialRef, cfg.Credential); err != nil {
			t.Fatalf("写凭证: %v", err)
		}
	}

	var gate connectors.PolicyGate
	if cfg.Deny {
		gate = DenyGate{denials: &env.denials}
	} else {
		gate = policy.New(policy.Options{
			Permissions: config.Permissions{Filesystem: "workspace"},
			ApprovalTTL: 30 * time.Second,
		})
	}

	rate := cfg.RateLimit
	if rate.Requests == 0 && rate.PerSeconds == 0 {
		rate = connectors.RateLimit{Requests: 100, PerSeconds: 60}
	}
	audit := cfg.Audit
	if audit == nil {
		audit = store
	}
	opts := connectors.Options{
		Definitions:   store,
		Audit:         audit,
		Credentials:   creds,
		Policy:        gate,
		Drivers:       cfg.Drivers,
		DriverFactory: cfg.Build,
		ApprovalWait:  3 * time.Second,
		PollInterval:  2 * time.Millisecond,
	}
	if cfg.Tune != nil {
		cfg.Tune(&opts)
	}
	svc, err := connectors.NewService(opts)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 允许分支：模拟用户在审批框里点"允许一次"。
	svc.SetPendingSink(func(pa connectors.PendingApproval) {
		env.approvals.Add(1)
		if err := svc.ResolveApproval(context.Background(), pa.ApprovalID, policy.ApprovalOnce); err != nil {
			t.Errorf("自动批准失败: %v", err)
		}
	})
	env.Svc = svc

	if _, err := svc.SaveDefinition(ctx, connectors.Definition{
		ID:               DefaultDefinitionID,
		Kind:             cfg.Kind,
		DisplayName:      string(cfg.Kind),
		CredentialRef:    DefaultCredentialRef,
		Enabled:          true,
		Capabilities:     cfg.Capabilities,
		AllowedResources: cfg.AllowedResources,
		RateLimit:        rate,
		Endpoints:        cfg.Endpoints,
	}); err != nil {
		t.Fatalf("保存定义: %v", err)
	}
	return env
}

// Execution 返回本条用例使用的服务端绑定执行身份。
func Execution() contracts.ExecutionContext {
	return contracts.ExecutionContext{
		WorkspaceID:    "ws-c08b",
		SessionID:      "sess-c08b",
		RunID:          "run-c08b",
		ConfigRevision: 11,
	}
}

// Invoke 调用一次动作。
func (e *Env) Invoke(t *testing.T, action string, args map[string]any) (connectors.Result, error) {
	t.Helper()
	return e.InvokeWithKey(t, action, args, "")
}

// InvokeWithKey 带幂等键调用一次动作。
func (e *Env) InvokeWithKey(t *testing.T, action string, args map[string]any, key string) (connectors.Result, error) {
	t.Helper()
	return e.InvokeCtx(t, context.Background(), action, args, key)
}

// InvokeCtx 用给定 ctx 调用一次动作（超时/取消用例）。
func (e *Env) InvokeCtx(t *testing.T, ctx context.Context, action string, args map[string]any, key string) (connectors.Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数: %v", err)
	}
	return e.Svc.Invoke(ctx, connectors.InvokeRequest{
		Execution:      Execution(),
		ConnectorID:    e.DefinitionID,
		ActionID:       action,
		Arguments:      raw,
		IdempotencyKey: key,
	})
}

// Audits 读回审计行。
func (e *Env) Audits(t *testing.T, limit int) []connectors.AuditEntry {
	t.Helper()
	rows, err := e.Store.ListAudits(context.Background(), e.DefinitionID, limit)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	return rows
}

// Approvals 返回服务推送过多少次待审批（"重试是否需要重新批准"的证据）。
func (e *Env) Approvals() int { return int(e.approvals.Load()) }

// Denials 返回策略门被问过多少次。
func (e *Env) Denials() int { return int(e.denials.Load()) }
