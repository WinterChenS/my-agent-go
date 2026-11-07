package feishu

// C08b 的飞书适配器用例：分页读文档块、写前策略门、派发后超时、限流、幂等判定。
//
// 关键差异（与钉钉相比）：飞书在 HTTP 200 里用 code!=0 表达拒绝，因此"认证失败"
// 既要覆盖 HTTP 401，也要覆盖 200+code —— 只看状态码的适配器会把失败当成功。

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
	fxAppID     = "cli_fixture_app"
	fxAppSecret = "feishu-app-secret-S3NT1NEL-c08b"
	fxDocID     = "doc-fixture"
	fxChatID    = "oc_fixture_chat"
	docPath     = "/open-apis/docx/v1/documents/" + fxDocID + "/blocks"
	updatePath  = docPath + "/batch_update"
	messagePath = "/open-apis/im/v1/messages"
)

type feishuEnv struct {
	api     *fixtures.API
	counter *fixtures.Counter
	env     *connectortest.Env
}

func newFeishuEnv(t *testing.T, opts fixtures.APIOptions, tune func(*connectortest.Config)) *feishuEnv {
	t.Helper()
	if opts.AppKey == "" {
		opts.AppKey = fxAppID
	}
	if opts.AppSecret == "" {
		opts.AppSecret = fxAppSecret
	}
	opts.Platform = fixtures.PlatformFeishu
	counter := fixtures.NewCounter(opts.ObservationDir)
	opts.Counter = counter
	api, err := fixtures.NewAPI(opts)
	if err != nil {
		t.Fatalf("启动飞书 fixture: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	cfg := connectortest.Config{
		Kind: connectors.KindFeishu,
		Endpoints: connectors.Endpoints{
			APIBase: api.BaseURL(),
			Account: "测试租户",
		},
		Capabilities: []connectors.Capability{
			connectors.CapabilityFeishuRead,
			connectors.CapabilityFeishuWrite,
			connectors.CapabilityFeishuSend,
		},
		AllowedResources: []string{fxDocID, fxChatID},
		Credential:       []byte(fxAppID + ":" + fxAppSecret),
		Build: func(def connectors.Definition) (connectors.Driver, error) {
			return New(Settings{Endpoints: def.Endpoints})
		},
	}
	if tune != nil {
		tune(&cfg)
	}
	return &feishuEnv{api: api, counter: counter, env: connectortest.New(t, cfg)}
}

// TestFeishuReadReturnsTypedRecords 覆盖分页读：两页各两条不同的块，只发两次读请求。
func TestFeishuReadReturnsTypedRecords(t *testing.T) {
	e := newFeishuEnv(t, fixtures.APIOptions{PageSize: 2}, nil)

	first, err := e.env.Invoke(t, "read_doc", map[string]any{"documentId": fxDocID})
	if err != nil {
		t.Fatalf("read_doc: %v", err)
	}
	if first.Outcome != connectors.OutcomeDone {
		t.Fatalf("读动作应完成: %+v", first)
	}
	var page httpx.Page[Block]
	if err := json.Unmarshal(first.Data, &page); err != nil {
		t.Fatalf("解析读结果: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("第一页应有 2 个块: %+v", page)
	}
	seen := map[string]bool{}
	for _, block := range page.Records {
		if strings.TrimSpace(block.ID) == "" {
			t.Fatalf("块必须有稳定 ID: %+v", block)
		}
		seen[block.ID] = true
	}
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("还有下一页时必须给出稳定 page_token: %+v", page)
	}

	second, err := e.env.Invoke(t, "read_doc", map[string]any{"documentId": fxDocID, "cursor": page.NextCursor})
	if err != nil {
		t.Fatalf("read_doc 第二页: %v", err)
	}
	var next httpx.Page[Block]
	if err := json.Unmarshal(second.Data, &next); err != nil {
		t.Fatalf("解析第二页: %v", err)
	}
	if len(next.Records) != 2 {
		t.Fatalf("第二页应有 2 个块: %+v", next)
	}
	for _, block := range next.Records {
		if seen[block.ID] {
			t.Fatalf("第二页与第一页重复: %s", block.ID)
		}
		seen[block.ID] = true
	}
	if len(seen) != 4 {
		t.Fatalf("两页应正好覆盖 4 个块（无重复、无跳页），实际 %d: %v", len(seen), seen)
	}
	if got := len(e.api.RequestsFor(docPath)); got != 2 {
		t.Fatalf("两次读只应发出 2 个 HTTP 请求，实际 %d: %+v", got, e.api.Requests())
	}
	if snap := e.counter.Snapshot(); snap.ExternalWrites() != 0 {
		t.Fatalf("读动作不得产生写副作用: %+v", snap)
	}
}

// TestFeishuWriteIsGatedAndCounted 覆盖 CONN02（写与发消息两条外发路径）。
func TestFeishuWriteIsGatedAndCounted(t *testing.T) {
	t.Run("update_doc_denied", func(t *testing.T) {
		e := newFeishuEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) { c.Deny = true })
		res, err := e.env.Invoke(t, "update_doc", map[string]any{"documentId": fxDocID, "content": "新内容"})
		if err != nil {
			t.Fatalf("策略拒绝应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("拒绝必须未派发: %+v", res)
		}
		if got := e.counter.Snapshot().FeishuDocUpdated; got != 0 {
			t.Fatalf("拒绝时 fixture 写计数必须为 0，实际 %d", got)
		}
		if got := len(e.api.RequestsFor(updatePath)); got != 0 {
			t.Fatalf("拒绝时不应发出写请求，实际 %d", got)
		}
		rows := e.env.Audits(t, 0)
		if len(rows) != 1 || rows[0].Decision != "deny" {
			t.Fatalf("应有一条拒绝审计: %+v", rows)
		}
		if rows[0].CredentialRef != connectortest.DefaultCredentialRef {
			t.Fatalf("审计只允许出现 credential ref: %+v", rows[0])
		}
	})

	t.Run("update_doc_allowed", func(t *testing.T) {
		e := newFeishuEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "update_doc", map[string]any{"documentId": fxDocID, "content": "验收记录"})
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone || !res.Dispatched {
			t.Fatalf("允许后应完成: %+v", res)
		}
		if got := e.counter.Snapshot().FeishuDocUpdated; got != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
		}
		writes := e.api.Writes()
		if len(writes) != 1 || writes[0].Target != fxDocID || writes[0].Text != "验收记录" {
			t.Fatalf("fixture 收到的写不对: %+v", writes)
		}
		rows := e.env.Audits(t, 0)
		if len(rows) != 1 || rows[0].Decision != "allow" || rows[0].Outcome != "done" {
			t.Fatalf("允许必须留 allow/done 审计: %+v", rows)
		}
		if rows[0].ToolName != "connector_feishu_update_doc" {
			t.Fatalf("工具名不对: %+v", rows[0])
		}
	})

	t.Run("send_message_outside_scope_denied", func(t *testing.T) {
		e := newFeishuEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "send_message", map[string]any{"chatId": "oc_other_team", "text": "越界"})
		if err != nil {
			t.Fatalf("越界应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("越界必须未派发: %+v", res)
		}
		if got := e.counter.Snapshot().FeishuMessages; got != 0 {
			t.Fatalf("越界时 fixture 写计数必须为 0，实际 %d", got)
		}
		if !strings.Contains(res.Detail, "允许的资源范围") {
			t.Fatalf("越界原因应可辨认: %+v", res)
		}
	})

	t.Run("send_message_allowed", func(t *testing.T) {
		e := newFeishuEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "send_message", map[string]any{"chatId": fxChatID, "text": "构建已完成"})
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone {
			t.Fatalf("允许后应完成: %+v", res)
		}
		if got := e.counter.Snapshot().FeishuMessages; got != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
		}
	})
}

// TestFeishuWriteTimeoutIsOutcomeUnknown 覆盖 CONN06：文档已更新但没收到确认。
func TestFeishuWriteTimeoutIsOutcomeUnknown(t *testing.T) {
	e := newFeishuEnv(t, fixtures.APIOptions{StallAfterApply: true}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	res, err := e.env.InvokeCtx(t, ctx, "update_doc",
		map[string]any{"documentId": fxDocID, "content": "会不会写两次"}, "")
	if err != nil {
		t.Fatalf("派发后超时不应是协议错误: %v", err)
	}
	if res.Outcome != connectors.OutcomeUnknown {
		t.Fatalf("派发后超时必须返回 OutcomeUnknown，实际 %+v", res)
	}
	if res.SafeToRetry {
		t.Fatal("未知结果不得声明可安全重试")
	}
	if got := e.counter.Snapshot().FeishuDocUpdated; got != 1 {
		t.Fatalf("平台已应用一次写入，实际 %d", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := e.counter.Snapshot().FeishuDocUpdated; got != 1 {
		t.Fatalf("非幂等动作不得自动重放，写计数变成 %d", got)
	}
	if got := len(e.api.RequestsFor(updatePath)); got != 1 {
		t.Fatalf("写请求只应发出一次，实际 %d", got)
	}
	rows := e.env.Audits(t, 0)
	if len(rows) != 1 || rows[0].Outcome != string(connectors.OutcomeUnknown) {
		t.Fatalf("未知结果必须留审计: %+v", rows)
	}
}

// TestFeishuRateLimitIsEnforcedAndVisible 覆盖限流：超限调用不发请求、不弹审批。
func TestFeishuRateLimitIsEnforcedAndVisible(t *testing.T) {
	e := newFeishuEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) {
		c.RateLimit = connectors.RateLimit{Requests: 2, PerSeconds: 60}
	})
	args := map[string]any{"chatId": fxChatID, "text": "限流用例"}
	for i := 0; i < 2; i++ {
		if res, err := e.env.Invoke(t, "send_message", args); err != nil || res.Outcome != connectors.OutcomeDone {
			t.Fatalf("第 %d 次调用应该允许: %+v %v", i+1, res, err)
		}
	}
	before := len(e.api.Requests())
	approvalsBefore := e.env.Approvals()

	res, err := e.env.Invoke(t, "send_message", args)
	if err != nil {
		t.Fatalf("限流应以结果返回: %v", err)
	}
	if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
		t.Fatalf("限流必须未派发: %+v", res)
	}
	if !strings.Contains(res.Detail, "限流") {
		t.Fatalf("限流结论必须可辨认: %+v", res)
	}
	if got := len(e.api.Requests()); got != before {
		t.Fatalf("被限流的调用不应发出任何 HTTP 请求：%d → %d", before, got)
	}
	if e.env.Approvals() != approvalsBefore {
		t.Fatal("被限流的调用不该弹审批")
	}
	rows := e.env.Audits(t, 0)
	last := rows[len(rows)-1]
	if last.Outcome != "rate_limited" {
		t.Fatalf("限流必须留结构化审计: %+v", last)
	}
}

// TestFeishuIdempotencyDecisionIsExplicit 覆盖幂等判定（写与发消息都是 none）。
func TestFeishuIdempotencyDecisionIsExplicit(t *testing.T) {
	for _, id := range []string{"update_doc", "send_message"} {
		action, ok := connectors.LookupAction(connectors.KindFeishu, id)
		if !ok {
			t.Fatalf("%s 必须在 catalog 里", id)
		}
		if action.Idempotency != connectors.IdempotencyNone {
			t.Fatalf("飞书不接受幂等键，%s 必须声明 none: %+v", id, action)
		}
		if action.Replayable("any-key") {
			t.Fatalf("%s 带任何键都不应被判定为可重放", id)
		}
	}

	e := newFeishuEnv(t, fixtures.APIOptions{}, nil)
	args := map[string]any{"chatId": fxChatID, "text": "重试用例"}
	for i := 0; i < 2; i++ {
		if _, err := e.env.InvokeWithKey(t, "send_message", args, "same-key"); err != nil {
			t.Fatalf("第 %d 次调用: %v", i+1, err)
		}
	}
	if got := e.env.Approvals(); got != 2 {
		t.Fatalf("非幂等重试必须各自重新授权（两次审批），实际 %d", got)
	}
	if got := e.counter.Snapshot().FeishuMessages; got != 2 {
		t.Fatalf("相同幂等键不能让平台去重，实际 %d", got)
	}
}

// TestFeishuHealthNeverGuessesConnected 覆盖 CONN01 的驱动侧。
//
// 特别覆盖"HTTP 200 + code!=0"这种飞书风格的拒绝：把它当成功就会显示 connected。
func TestFeishuHealthNeverGuessesConnected(t *testing.T) {
	e := newFeishuEnv(t, fixtures.APIOptions{}, nil)
	ctx := context.Background()

	health, err := e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status != connectors.HealthConnected {
		t.Fatalf("正确应用凭据应 connected: %+v", health)
	}

	if err := e.env.Creds.Put(ctx, connectortest.DefaultCredentialRef, []byte("cli_wrong:wrong")); err != nil {
		t.Fatalf("写错凭证: %v", err)
	}
	health, err = e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status == connectors.HealthConnected {
		t.Fatalf("应用凭据不对不得报告 connected: %+v", health)
	}
	if health.Remediation != connectors.RemediationAddCredentials {
		t.Fatalf("认证失败应提示补凭证/应用注册: %+v", health)
	}

	// 凭证格式不对（缺少冒号）：必须在派发前就拒绝，而不是发一个空应用登录。
	if err := e.env.Creds.Put(ctx, connectortest.DefaultCredentialRef, []byte("no-separator")); err != nil {
		t.Fatalf("写坏格式凭证: %v", err)
	}
	res, err := e.env.Invoke(t, "send_message", map[string]any{"chatId": fxChatID, "text": "x"})
	if err != nil {
		t.Fatalf("坏格式凭证应以结果返回: %v", err)
	}
	if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
		t.Fatalf("坏格式凭证必须未派发: %+v", res)
	}
	if got := e.counter.Snapshot().FeishuMessages; got != 0 {
		t.Fatalf("坏格式凭证不得产生副作用，实际 %d", got)
	}
}
