package connectors

// C08b：每个平台声明自己的本地节流上限，并由 Service 在派发前执行。
//
// brief 要求"rate limiting declared per platform"：上限必须写在表中（可审阅、
// 可测试），而不是散落在各驱动里，也不是一个与平台无关的常数。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/storage"
)

// TestEveryKindDeclaresAPlatformRateLimit 保证"每个平台都声明了上限"。
func TestEveryKindDeclaresAPlatformRateLimit(t *testing.T) {
	for _, kind := range KnownKinds() {
		limit, ok := DefaultRateLimit(kind)
		if !ok {
			t.Fatalf("%s 没有声明默认节流上限", kind)
		}
		if limit.Requests <= 0 || limit.PerSeconds <= 0 {
			t.Fatalf("%s 的节流上限不完整: %+v", kind, limit)
		}
	}
	for kind := range defaultRateLimits {
		if !kind.Known() {
			t.Fatalf("为未实现的类型声明了节流上限: %s", kind)
		}
	}
	// 平台之间不该被抄成同一个数字：至少钉钉/飞书/Notion 与 Slack/GitHub 不同。
	ding, _ := DefaultRateLimit(KindDingtalk)
	slack, _ := DefaultRateLimit(KindSlack)
	if ding.Requests == slack.Requests && ding.PerSeconds == slack.PerSeconds {
		t.Fatalf("钉钉与 Slack 用同一个上限说明「每平台声明」退化成了常量: %+v", ding)
	}
}

// TestServiceEnforcesDeclaredRateLimitPerKind 保证未配置 RateLimit 的定义
// 真的按自己平台的默认上限被限流（不是按一个全局常数）。
func TestServiceEnforcesDeclaredRateLimitPerKind(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := storage.Open(ctx, root+"/connectors.db")
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	driver := &fakeDriver{
		kind: KindDingtalk,
		caps: []Capability{CapabilityDingtalkRead, CapabilityDingtalkSend},
	}
	limit, ok := DefaultRateLimit(KindDingtalk)
	if !ok || limit.Requests <= 0 {
		t.Fatalf("钉钉必须有默认上限")
	}
	creds := credentials.NewMemStore()
	if err := creds.Put(ctx, "connector:ding-1", []byte("app-key:app-secret")); err != nil {
		t.Fatalf("写凭证: %v", err)
	}
	svc, err := NewService(Options{
		Definitions:  store,
		Audit:        store,
		Credentials:  creds,
		Policy:       policy.New(policy.Options{Permissions: config.Permissions{Filesystem: "workspace"}}),
		Drivers:      []Driver{driver},
		ApprovalWait: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 自动批准：本用例只关心限流，不关心审批交互。
	svc.SetPendingSink(func(pa PendingApproval) {
		if perr := svc.ResolveApproval(context.Background(), pa.ApprovalID, policy.ApprovalOnce); perr != nil {
			t.Errorf("自动批准失败: %v", perr)
		}
	})
	// 定义里刻意不给 RateLimit：验证服务用的是该平台的默认值。
	if _, err := svc.SaveDefinition(ctx, Definition{
		ID:               "ding-1",
		Kind:             KindDingtalk,
		DisplayName:      "钉钉",
		CredentialRef:    "connector:ding-1",
		Enabled:          true,
		Capabilities:     []Capability{CapabilityDingtalkRead, CapabilityDingtalkSend},
		AllowedResources: []string{"*"},
	}); err != nil {
		t.Fatalf("保存定义: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{"conversationId": "cid-1", "text": "限流"})
	invoke := func() Result {
		res, ierr := svc.Invoke(ctx, InvokeRequest{
			ConnectorID: "ding-1",
			ActionID:    "send_group",
			Arguments:   raw,
		})
		if ierr != nil {
			t.Fatalf("Invoke: %v", ierr)
		}
		return res
	}
	for i := 0; i < limit.Requests; i++ {
		if res := invoke(); res.Outcome != OutcomeDone {
			t.Fatalf("第 %d 次调用应在限额内: %+v", i+1, res)
		}
	}
	res := invoke()
	if res.Outcome != OutcomeFailed || res.Dispatched || !strings.Contains(res.Detail, "限流") {
		t.Fatalf("超出平台默认上限必须被拒绝: %+v", res)
	}
	if got := driver.CallCount(); got != limit.Requests {
		t.Fatalf("被限流的调用不得到达驱动：驱动被调用 %d 次，期望 %d", got, limit.Requests)
	}
	rows, err := store.ListAudits(ctx, "ding-1", 0)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if last := rows[len(rows)-1]; last.Outcome != "rate_limited" {
		t.Fatalf("限流必须留结构化审计: %+v", last)
	}
}
