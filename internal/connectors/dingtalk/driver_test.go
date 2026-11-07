package dingtalk

// C08b 的钉钉适配器用例：分页读、写前策略门、派发后超时、限流、幂等判定。
//
// 外部 oracle 是 fixtures.API：写计数器与 HTTP 请求日志都不经过被测代码，
// 所以"拒绝时一次都没发出去"是从 fixture 数出来的，不是驱动自述的。

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
	fxAppKey       = "ding-app-key"
	fxAppSecret    = "ding-app-secret-S3NT1NEL-c08b"
	fxConversation = "cidFixtureGroup"
	sendPath       = "/v1.0/robot/groupMessages/send"
	scopePath      = "/v1.0/contact/scopes"
)

type dingtalkEnv struct {
	api     *fixtures.API
	counter *fixtures.Counter
	env     *connectortest.Env
}

func newDingtalkEnv(t *testing.T, opts fixtures.APIOptions, tune func(*connectortest.Config)) *dingtalkEnv {
	t.Helper()
	if opts.AppKey == "" {
		opts.AppKey = fxAppKey
	}
	if opts.AppSecret == "" {
		opts.AppSecret = fxAppSecret
	}
	opts.Platform = fixtures.PlatformDingtalk
	counter := fixtures.NewCounter(opts.ObservationDir)
	opts.Counter = counter
	api, err := fixtures.NewAPI(opts)
	if err != nil {
		t.Fatalf("启动钉钉 fixture: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	cfg := connectortest.Config{
		Kind: connectors.KindDingtalk,
		Endpoints: connectors.Endpoints{
			APIBase: api.BaseURL(),
			Account: "测试企业",
		},
		Capabilities:     []connectors.Capability{connectors.CapabilityDingtalkRead, connectors.CapabilityDingtalkSend},
		AllowedResources: []string{fxConversation},
		Credential:       []byte(fxAppKey + ":" + fxAppSecret),
		Build: func(def connectors.Definition) (connectors.Driver, error) {
			return New(Settings{Endpoints: def.Endpoints})
		},
	}
	if tune != nil {
		tune(&cfg)
	}
	return &dingtalkEnv{api: api, counter: counter, env: connectortest.New(t, cfg)}
}

// TestDingtalkReadReturnsTypedRecords 覆盖分页读：两页各取一条不同的记录，
// 且 fixture 只收到两次读请求（没有重复抓取、没有跳页）。
func TestDingtalkReadReturnsTypedRecords(t *testing.T) {
	e := newDingtalkEnv(t, fixtures.APIOptions{PageSize: 2}, nil)

	first, err := e.env.Invoke(t, "read_scope", map[string]any{"limit": 2})
	if err != nil {
		t.Fatalf("read_scope: %v", err)
	}
	if first.Outcome != connectors.OutcomeDone {
		t.Fatalf("读动作应完成: %+v", first)
	}
	var page httpx.Page[Scope]
	if err := json.Unmarshal(first.Data, &page); err != nil {
		t.Fatalf("解析读结果: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("第一页应有 2 条 typed 记录: %+v", page)
	}
	ids := map[string]bool{}
	for _, rec := range page.Records {
		if strings.TrimSpace(rec.ID) == "" || strings.TrimSpace(rec.Name) == "" {
			t.Fatalf("记录的稳定 ID/名称不能为空: %+v", rec)
		}
		if ids[rec.ID] {
			t.Fatalf("第一页里出现重复 ID: %s", rec.ID)
		}
		ids[rec.ID] = true
	}
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("还有下一页时必须给出稳定游标: %+v", page)
	}

	second, err := e.env.Invoke(t, "read_scope", map[string]any{"limit": 2, "cursor": page.NextCursor})
	if err != nil {
		t.Fatalf("read_scope 第二页: %v", err)
	}
	var next httpx.Page[Scope]
	if err := json.Unmarshal(second.Data, &next); err != nil {
		t.Fatalf("解析第二页: %v", err)
	}
	if len(next.Records) != 2 {
		t.Fatalf("第二页应有 2 条记录: %+v", next)
	}
	for _, rec := range next.Records {
		if ids[rec.ID] {
			t.Fatalf("第二页与第一页重复: %s", rec.ID)
		}
		ids[rec.ID] = true
	}
	// 5 条记录 + 每页 2 条：两页覆盖 scope-1..4，没有跳页。
	for _, want := range []string{"scope-1", "scope-2", "scope-3", "scope-4"} {
		if !ids[want] {
			t.Fatalf("两页应覆盖 %s，实际 %v", want, ids)
		}
	}
	if got := len(e.api.RequestsFor(scopePath)); got != 2 {
		t.Fatalf("两次读只应发出 2 个 HTTP 请求，实际 %d: %+v", got, e.api.Requests())
	}
	if snap := e.counter.Snapshot(); snap.ExternalWrites() != 0 {
		t.Fatalf("读动作不得产生写副作用: %+v", snap)
	}
}

// TestDingtalkWriteIsGatedAndCounted 覆盖 CONN02：拒绝 → 计数 0；允许一次 → 计数 1；
// 两条路径都必须留下带 credential ref 的审计，且审计里没有 AppSecret。
func TestDingtalkWriteIsGatedAndCounted(t *testing.T) {
	args := map[string]any{"conversationId": fxConversation, "text": "周会改到 15:00"}

	t.Run("denied", func(t *testing.T) {
		e := newDingtalkEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) { c.Deny = true })
		res, err := e.env.Invoke(t, "send_group", args)
		if err != nil {
			t.Fatalf("策略拒绝应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("拒绝必须未派发: %+v", res)
		}
		if got := e.counter.Snapshot().DingtalkGroupMessages; got != 0 {
			t.Fatalf("策略拒绝时 fixture 写计数必须为 0，实际 %d", got)
		}
		if len(e.api.Writes()) != 0 {
			t.Fatalf("策略拒绝时 fixture 不应收到任何写: %+v", e.api.Writes())
		}
		if got := len(e.api.RequestsFor(sendPath)); got != 0 {
			t.Fatalf("策略拒绝时不应发出任何写请求，实际 %d", got)
		}
		rows := e.env.Audits(t, 0)
		if len(rows) != 1 {
			t.Fatalf("应有一条拒绝审计: %+v", rows)
		}
		if rows[0].Decision != "deny" || rows[0].CredentialRef != connectortest.DefaultCredentialRef {
			t.Fatalf("审计内容不对: %+v", rows[0])
		}
		raw, _ := json.Marshal(rows[0])
		if strings.Contains(string(raw), fxAppSecret) {
			t.Fatalf("审计含 AppSecret: %s", raw)
		}
	})

	t.Run("allowed", func(t *testing.T) {
		e := newDingtalkEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "send_group", args)
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone || !res.Dispatched {
			t.Fatalf("允许后应完成: %+v", res)
		}
		if got := e.counter.Snapshot().DingtalkGroupMessages; got != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
		}
		writes := e.api.Writes()
		if len(writes) != 1 || writes[0].Target != fxConversation {
			t.Fatalf("fixture 收到的写不对: %+v", writes)
		}
		if !strings.Contains(writes[0].Text, "周会改到") {
			t.Fatalf("fixture 收到的消息内容不对: %+v", writes[0])
		}
		rows := e.env.Audits(t, 0)
		if len(rows) != 1 || rows[0].Decision != "allow" || rows[0].Outcome != "done" {
			t.Fatalf("允许必须留 allow/done 审计: %+v", rows)
		}
		if rows[0].CredentialRef != connectortest.DefaultCredentialRef {
			t.Fatalf("审计只允许出现 credential ref: %+v", rows[0])
		}
		if rows[0].ToolName != "connector_dingtalk_send_group" {
			t.Fatalf("工具名必须是 catalog 派生的稳定名字: %+v", rows[0])
		}
		raw, _ := json.Marshal(rows[0])
		if strings.Contains(string(raw), fxAppSecret) {
			t.Fatalf("审计含 AppSecret: %s", raw)
		}
	})
}

// TestDingtalkWriteTimeoutIsOutcomeUnknown 覆盖 CONN06：
// 平台已经收下消息但不确认 → OutcomeUnknown，且不发生第二次发送。
func TestDingtalkWriteTimeoutIsOutcomeUnknown(t *testing.T) {
	e := newDingtalkEnv(t, fixtures.APIOptions{StallAfterApply: true}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	res, err := e.env.InvokeCtx(t, ctx, "send_group",
		map[string]any{"conversationId": fxConversation, "text": "会不会重复"}, "")
	if err != nil {
		t.Fatalf("派发后超时不应是协议错误: %v", err)
	}
	if res.Outcome != connectors.OutcomeUnknown {
		t.Fatalf("派发后超时必须返回 OutcomeUnknown，实际 %+v", res)
	}
	if res.SafeToRetry {
		t.Fatal("未知结果不得声明可安全重试")
	}
	if got := e.counter.Snapshot().DingtalkGroupMessages; got != 1 {
		t.Fatalf("平台已收到一次写入（外部 oracle 应为 1），实际 %d", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := e.counter.Snapshot().DingtalkGroupMessages; got != 1 {
		t.Fatalf("非幂等动作不得自动重放，写计数变成 %d", got)
	}
	if got := len(e.api.RequestsFor(sendPath)); got != 1 {
		t.Fatalf("写请求只应发出一次，实际 %d", got)
	}
	rows := e.env.Audits(t, 0)
	if len(rows) != 1 || rows[0].Outcome != string(connectors.OutcomeUnknown) {
		t.Fatalf("未知结果必须留审计: %+v", rows)
	}
}

// TestDingtalkRateLimitIsEnforcedAndVisible 覆盖限流：超限的调用不弹审批、不发请求。
func TestDingtalkRateLimitIsEnforcedAndVisible(t *testing.T) {
	e := newDingtalkEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) {
		c.RateLimit = connectors.RateLimit{Requests: 2, PerSeconds: 60}
	})
	args := map[string]any{"conversationId": fxConversation, "text": "限流用例"}
	for i := 0; i < 2; i++ {
		if res, err := e.env.Invoke(t, "send_group", args); err != nil || res.Outcome != connectors.OutcomeDone {
			t.Fatalf("第 %d 次调用应该允许: %+v %v", i+1, res, err)
		}
	}
	before := len(e.api.Requests())
	approvalsBefore := e.env.Approvals()

	res, err := e.env.Invoke(t, "send_group", args)
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
		t.Fatal("被限流的调用不该弹审批（批准了也执行不了）")
	}
	if got := e.counter.Snapshot().DingtalkGroupMessages; got != 2 {
		t.Fatalf("限流不应改变外部写计数，实际 %d", got)
	}
	rows := e.env.Audits(t, 0)
	last := rows[len(rows)-1]
	if last.Outcome != "rate_limited" || last.Decision != "deny" {
		t.Fatalf("限流必须留结构化审计: %+v", last)
	}
}

// TestDingtalkIdempotencyDecisionIsExplicit 覆盖幂等判定：
// catalog 明确声明 send_group 为 none（钉钉不接受幂等键），因此"重试"是一次
// 新的、必须重新经过授权的动作，而不是同一次动作的重放。
func TestDingtalkIdempotencyDecisionIsExplicit(t *testing.T) {
	action, ok := connectors.LookupAction(connectors.KindDingtalk, "send_group")
	if !ok {
		t.Fatal("send_group 必须在 catalog 里")
	}
	if action.Idempotency != connectors.IdempotencyNone {
		t.Fatalf("钉钉群消息不接受幂等键，必须声明 none: %+v", action)
	}
	if action.Replayable("any-key") {
		t.Fatal("非幂等动作带任何键都不应被判定为可重放")
	}

	e := newDingtalkEnv(t, fixtures.APIOptions{}, nil)
	args := map[string]any{"conversationId": fxConversation, "text": "重试用例"}
	for i := 0; i < 2; i++ {
		if _, err := e.env.InvokeWithKey(t, "send_group", args, "same-key"); err != nil {
			t.Fatalf("第 %d 次调用: %v", i+1, err)
		}
	}
	if got := e.env.Approvals(); got != 2 {
		t.Fatalf("非幂等重试必须各自重新授权（两次审批），实际 %d", got)
	}
	if got := len(e.api.RequestsFor(sendPath)); got != 2 {
		t.Fatalf("两次授权对应两次派发，实际 %d", got)
	}
	if got := e.counter.Snapshot().DingtalkGroupMessages; got != 2 {
		t.Fatalf("相同的幂等键不能让平台只生效一次（平台没有这个能力），实际 %d", got)
	}
}

// TestDingtalkHealthNeverGuessesConnected 覆盖 CONN01 的驱动侧：
// 只有应用凭据真的能换取令牌、并真的读到权限范围，才是 connected。
func TestDingtalkHealthNeverGuessesConnected(t *testing.T) {
	e := newDingtalkEnv(t, fixtures.APIOptions{}, nil)
	ctx := context.Background()

	health, err := e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status != connectors.HealthConnected {
		t.Fatalf("正确应用凭据应 connected: %+v", health)
	}

	if err := e.env.Creds.Put(ctx, connectortest.DefaultCredentialRef, []byte("wrong:secret")); err != nil {
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

	if err := e.env.Creds.Delete(ctx, connectortest.DefaultCredentialRef); err != nil {
		t.Fatalf("删凭证: %v", err)
	}
	status, err := e.env.Svc.Status(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Health.Status == connectors.HealthConnected {
		t.Fatalf("没有凭证不得 connected: %+v", status.Health)
	}
	res, err := e.env.Invoke(t, "send_group", map[string]any{"conversationId": fxConversation, "text": "x"})
	if err != nil {
		t.Fatalf("缺凭证应以结果返回: %v", err)
	}
	if res.Outcome != connectors.OutcomeFailed || res.Dispatched || !strings.Contains(res.Detail, "未派发") {
		t.Fatalf("缺凭证必须未派发: %+v", res)
	}
}
