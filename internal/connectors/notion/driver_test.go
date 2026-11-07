package notion

// C08b 的 Notion 适配器用例：分页查询、写前策略门、派发后超时、限流、幂等判定。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/connectortest"
	"my-agent-go/internal/connectors/fixtures"
	"my-agent-go/internal/connectors/httpx"
)

const (
	fxToken    = "secret_fixture_notion_integration"
	fxDatabase = "db-fixture"
	fxParent   = "page-1"
	queryPath  = "/v1/databases/" + fxDatabase + "/query"
	pagesPath  = "/v1/pages"
)

type notionEnv struct {
	api     *fixtures.API
	counter *fixtures.Counter
	env     *connectortest.Env
}

func newNotionEnv(t *testing.T, opts fixtures.APIOptions, tune func(*connectortest.Config)) *notionEnv {
	t.Helper()
	if opts.Token == "" {
		opts.Token = fxToken
	}
	opts.Platform = fixtures.PlatformNotion
	counter := fixtures.NewCounter(opts.ObservationDir)
	opts.Counter = counter
	api, err := fixtures.NewAPI(opts)
	if err != nil {
		t.Fatalf("启动 Notion fixture: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	cfg := connectortest.Config{
		Kind:             connectors.KindNotion,
		Endpoints:        connectors.Endpoints{APIBase: api.BaseURL(), Account: "测试 workspace"},
		Capabilities:     []connectors.Capability{connectors.CapabilityNotionRead, connectors.CapabilityNotionWrite},
		AllowedResources: []string{fxDatabase, fxParent, "page-2"},
		Credential:       []byte(fxToken),
		Build: func(def connectors.Definition) (connectors.Driver, error) {
			return New(Settings{Endpoints: def.Endpoints})
		},
	}
	if tune != nil {
		tune(&cfg)
	}
	return &notionEnv{api: api, counter: counter, env: connectortest.New(t, cfg)}
}

// TestNotionReadReturnsTypedRecords 覆盖分页查询。
func TestNotionReadReturnsTypedRecords(t *testing.T) {
	e := newNotionEnv(t, fixtures.APIOptions{PageSize: 2}, nil)

	first, err := e.env.Invoke(t, "query", map[string]any{"databaseId": fxDatabase})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if first.Outcome != connectors.OutcomeDone {
		t.Fatalf("读动作应完成: %+v", first)
	}
	var page httpx.Page[Page]
	if err := json.Unmarshal(first.Data, &page); err != nil {
		t.Fatalf("解析读结果: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("第一页应有 2 个页面: %+v", page)
	}
	seen := map[string]bool{}
	for _, rec := range page.Records {
		if strings.TrimSpace(rec.ID) == "" || strings.TrimSpace(rec.Title) == "" {
			t.Fatalf("页面必须有稳定 ID 与标题: %+v", rec)
		}
		seen[rec.ID] = true
	}
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("还有下一页时必须给出 next_cursor: %+v", page)
	}

	second, err := e.env.Invoke(t, "query", map[string]any{"databaseId": fxDatabase, "cursor": page.NextCursor})
	if err != nil {
		t.Fatalf("query 第二页: %v", err)
	}
	var next httpx.Page[Page]
	if err := json.Unmarshal(second.Data, &next); err != nil {
		t.Fatalf("解析第二页: %v", err)
	}
	if len(next.Records) != 2 {
		t.Fatalf("第二页应有 2 个页面: %+v", next)
	}
	for _, rec := range next.Records {
		if seen[rec.ID] {
			t.Fatalf("第二页与第一页重复: %s", rec.ID)
		}
		seen[rec.ID] = true
	}
	if len(seen) != 4 {
		t.Fatalf("两页应正好覆盖 4 个页面，实际 %d", len(seen))
	}
	if got := len(e.api.RequestsFor(queryPath)); got != 2 {
		t.Fatalf("两次查询只应发出 2 个 HTTP 请求，实际 %d", got)
	}
	if snap := e.counter.Snapshot(); snap.ExternalWrites() != 0 {
		t.Fatalf("读动作不得产生写副作用: %+v", snap)
	}
}

// TestNotionWriteIsGatedAndCounted 覆盖 CONN02（新建与更新两条写路径）。
func TestNotionWriteIsGatedAndCounted(t *testing.T) {
	t.Run("create_page_denied", func(t *testing.T) {
		e := newNotionEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) { c.Deny = true })
		res, err := e.env.Invoke(t, "create_page", map[string]any{"parentId": fxParent, "title": "被拒绝的页面"})
		if err != nil {
			t.Fatalf("策略拒绝应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("拒绝必须未派发: %+v", res)
		}
		if got := e.counter.Snapshot().NotionPagesCreated; got != 0 {
			t.Fatalf("拒绝时 fixture 写计数必须为 0，实际 %d", got)
		}
		if got := len(e.api.RequestsFor(pagesPath)); got != 0 {
			t.Fatalf("拒绝时不应发出写请求，实际 %d", got)
		}
		rows := e.env.Audits(t, 0)
		if len(rows) != 1 || rows[0].Decision != "deny" {
			t.Fatalf("应有一条拒绝审计: %+v", rows)
		}
		if rows[0].CredentialRef != connectortest.DefaultCredentialRef {
			t.Fatalf("审计只允许出现 credential ref: %+v", rows[0])
		}
		raw, _ := json.Marshal(rows[0])
		if strings.Contains(string(raw), fxToken) {
			t.Fatalf("审计含 integration token: %s", raw)
		}
	})

	t.Run("create_page_allowed", func(t *testing.T) {
		e := newNotionEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "create_page", map[string]any{"parentId": fxParent, "title": "验收记录"})
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone || !res.Dispatched {
			t.Fatalf("允许后应完成: %+v", res)
		}
		if got := e.counter.Snapshot().NotionPagesCreated; got != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
		}
		writes := e.api.Writes()
		if len(writes) != 1 || writes[0].Target != fxParent || writes[0].Text != "验收记录" {
			t.Fatalf("fixture 收到的写不对: %+v", writes)
		}
		rows := e.env.Audits(t, 0)
		if len(rows) != 1 || rows[0].ToolName != "connector_notion_create_page" {
			t.Fatalf("允许必须留带工具名的审计: %+v", rows)
		}
	})

	t.Run("update_page_allowed", func(t *testing.T) {
		e := newNotionEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "update_page", map[string]any{"pageId": "page-2", "title": "改名后的页面"})
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone {
			t.Fatalf("允许后应完成: %+v", res)
		}
		if got := e.counter.Snapshot().NotionPagesUpdated; got != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
		}
	})

	t.Run("page_outside_scope_denied", func(t *testing.T) {
		e := newNotionEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "update_page", map[string]any{"pageId": "page-999", "title": "越界"})
		if err != nil {
			t.Fatalf("越界应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("越界必须未派发: %+v", res)
		}
		if !strings.Contains(res.Detail, "允许的资源范围") {
			t.Fatalf("越界原因应可辨认: %+v", res)
		}
		if got := e.counter.Snapshot().NotionPagesUpdated; got != 0 {
			t.Fatalf("越界时写计数必须为 0，实际 %d", got)
		}
		// 越界必须在 HTTP 之前被拒：否则"平台返回 404"会伪装成同样的 0 副作用。
		if got := len(e.api.Requests()); got != 0 {
			t.Fatalf("越界时不应发出任何 HTTP 请求，实际 %d", got)
		}
	})
}

// TestNotionWriteTimeoutIsOutcomeUnknown 覆盖 CONN06。
func TestNotionWriteTimeoutIsOutcomeUnknown(t *testing.T) {
	e := newNotionEnv(t, fixtures.APIOptions{StallAfterApply: true}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	res, err := e.env.InvokeCtx(t, ctx, "create_page",
		map[string]any{"parentId": fxParent, "title": "会不会建两个"}, "")
	if err != nil {
		t.Fatalf("派发后超时不应是协议错误: %v", err)
	}
	if res.Outcome != connectors.OutcomeUnknown {
		t.Fatalf("派发后超时必须返回 OutcomeUnknown，实际 %+v", res)
	}
	if res.SafeToRetry {
		t.Fatal("未知结果不得声明可安全重试")
	}
	if got := e.counter.Snapshot().NotionPagesCreated; got != 1 {
		t.Fatalf("平台已创建一次，实际 %d", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := e.counter.Snapshot().NotionPagesCreated; got != 1 {
		t.Fatalf("非幂等动作不得自动重放，写计数变成 %d", got)
	}
	if got := len(e.api.RequestsFor(pagesPath)); got != 1 {
		t.Fatalf("写请求只应发出一次，实际 %d", got)
	}
	rows := e.env.Audits(t, 0)
	if len(rows) != 1 || rows[0].Outcome != string(connectors.OutcomeUnknown) {
		t.Fatalf("未知结果必须留审计: %+v", rows)
	}
}

// TestNotionRateLimitIsEnforcedAndVisible 覆盖本地限流。
func TestNotionRateLimitIsEnforcedAndVisible(t *testing.T) {
	e := newNotionEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) {
		c.RateLimit = connectors.RateLimit{Requests: 2, PerSeconds: 60}
	})
	args := map[string]any{"pageId": fxParent, "title": "限流用例"}
	for i := 0; i < 2; i++ {
		if res, err := e.env.Invoke(t, "update_page", args); err != nil || res.Outcome != connectors.OutcomeDone {
			t.Fatalf("第 %d 次调用应该允许: %+v %v", i+1, res, err)
		}
	}
	before := len(e.api.Requests())
	approvalsBefore := e.env.Approvals()

	res, err := e.env.Invoke(t, "update_page", args)
	if err != nil {
		t.Fatalf("限流应以结果返回: %v", err)
	}
	if res.Outcome != connectors.OutcomeFailed || res.Dispatched || !strings.Contains(res.Detail, "限流") {
		t.Fatalf("限流必须未派发且可辨认: %+v", res)
	}
	if got := len(e.api.Requests()); got != before {
		t.Fatalf("被限流的调用不应发出任何 HTTP 请求：%d → %d", before, got)
	}
	if e.env.Approvals() != approvalsBefore {
		t.Fatal("被限流的调用不该弹审批")
	}
	rows := e.env.Audits(t, 0)
	if last := rows[len(rows)-1]; last.Outcome != "rate_limited" {
		t.Fatalf("限流必须留结构化审计: %+v", last)
	}
}

// TestNotionIdempotencyDecisionIsExplicit 覆盖幂等判定。
func TestNotionIdempotencyDecisionIsExplicit(t *testing.T) {
	for _, id := range []string{"create_page", "update_page"} {
		action, ok := connectors.LookupAction(connectors.KindNotion, id)
		if !ok {
			t.Fatalf("%s 必须在 catalog 里", id)
		}
		if action.Idempotency != connectors.IdempotencyNone {
			t.Fatalf("Notion 不接受幂等键，%s 必须声明 none: %+v", id, action)
		}
		if action.Replayable("same-key") {
			t.Fatalf("%s 带任何键都不应被判定为可重放", id)
		}
	}

	e := newNotionEnv(t, fixtures.APIOptions{}, nil)
	args := map[string]any{"parentId": fxParent, "title": "重试用例"}
	for i := 0; i < 2; i++ {
		if _, err := e.env.InvokeWithKey(t, "create_page", args, "same-key"); err != nil {
			t.Fatalf("第 %d 次调用: %v", i+1, err)
		}
	}
	if got := e.env.Approvals(); got != 2 {
		t.Fatalf("非幂等重试必须各自重新授权（两次审批），实际 %d", got)
	}
	if got := e.counter.Snapshot().NotionPagesCreated; got != 2 {
		t.Fatalf("相同幂等键不能让 Notion 去重，实际 %d", got)
	}
}

// TestNotionHealthNeverGuessesConnected 覆盖 CONN01 的驱动侧。
func TestNotionHealthNeverGuessesConnected(t *testing.T) {
	e := newNotionEnv(t, fixtures.APIOptions{}, nil)
	ctx := context.Background()

	health, err := e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status != connectors.HealthConnected {
		t.Fatalf("正确 token 应 connected: %+v", health)
	}

	if err := e.env.Creds.Put(ctx, connectortest.DefaultCredentialRef, []byte("secret_revoked")); err != nil {
		t.Fatalf("写错凭证: %v", err)
	}
	health, err = e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status == connectors.HealthConnected {
		t.Fatalf("无效 token 不得报告 connected: %+v", health)
	}
	if health.Remediation != connectors.RemediationAddCredentials {
		t.Fatalf("认证失败应提示补凭证: %+v", health)
	}
}
