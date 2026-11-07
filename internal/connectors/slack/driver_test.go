package slack

// C08b 的 Slack 适配器用例。
//
// Slack 的拒绝是"HTTP 200 + ok:false"，因此这里额外覆盖：
// 认证失败不会因为状态码是 200 而被当成 connected / done。

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
	fxToken    = "xoxb-fixture-bot-token"
	fxChannel  = "C0FIXTURE"
	fxThreadTS = "1700000001.000001"
	postPath   = "/api/chat.postMessage"
	histPath   = "/api/conversations.history"
)

type slackEnv struct {
	api     *fixtures.API
	counter *fixtures.Counter
	env     *connectortest.Env
}

func newSlackEnv(t *testing.T, opts fixtures.APIOptions, tune func(*connectortest.Config)) *slackEnv {
	t.Helper()
	if opts.Token == "" {
		opts.Token = fxToken
	}
	opts.Platform = fixtures.PlatformSlack
	counter := fixtures.NewCounter(opts.ObservationDir)
	opts.Counter = counter
	api, err := fixtures.NewAPI(opts)
	if err != nil {
		t.Fatalf("启动 Slack fixture: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	cfg := connectortest.Config{
		Kind:             connectors.KindSlack,
		Endpoints:        connectors.Endpoints{APIBase: api.BaseURL(), Account: "fixture-workspace"},
		Capabilities:     []connectors.Capability{connectors.CapabilitySlackRead, connectors.CapabilitySlackSend},
		AllowedResources: []string{fxChannel},
		Credential:       []byte(fxToken),
		Build: func(def connectors.Definition) (connectors.Driver, error) {
			return New(Settings{Endpoints: def.Endpoints})
		},
	}
	if tune != nil {
		tune(&cfg)
	}
	return &slackEnv{api: api, counter: counter, env: connectortest.New(t, cfg)}
}

// TestSlackReadReturnsTypedRecords 覆盖分页读频道历史。
func TestSlackReadReturnsTypedRecords(t *testing.T) {
	e := newSlackEnv(t, fixtures.APIOptions{PageSize: 2}, nil)

	first, err := e.env.Invoke(t, "read_channel", map[string]any{"channel": fxChannel})
	if err != nil {
		t.Fatalf("read_channel: %v", err)
	}
	if first.Outcome != connectors.OutcomeDone {
		t.Fatalf("读动作应完成: %+v", first)
	}
	var page httpx.Page[Message]
	if err := json.Unmarshal(first.Data, &page); err != nil {
		t.Fatalf("解析读结果: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("第一页应有 2 条消息: %+v", page)
	}
	seen := map[string]bool{}
	for _, msg := range page.Records {
		if strings.TrimSpace(msg.TS) == "" {
			t.Fatalf("消息必须有稳定 ts: %+v", msg)
		}
		seen[msg.TS] = true
	}
	if !page.HasMore || page.NextCursor == "" {
		t.Fatalf("还有下一页时必须给出 cursor: %+v", page)
	}

	second, err := e.env.Invoke(t, "read_channel", map[string]any{"channel": fxChannel, "cursor": page.NextCursor})
	if err != nil {
		t.Fatalf("read_channel 第二页: %v", err)
	}
	var next httpx.Page[Message]
	if err := json.Unmarshal(second.Data, &next); err != nil {
		t.Fatalf("解析第二页: %v", err)
	}
	if len(next.Records) != 2 {
		t.Fatalf("第二页应有 2 条消息: %+v", next)
	}
	for _, msg := range next.Records {
		if seen[msg.TS] {
			t.Fatalf("第二页与第一页重复: %s", msg.TS)
		}
		seen[msg.TS] = true
	}
	if len(seen) != 4 {
		t.Fatalf("两页应正好覆盖 4 条消息，实际 %d", len(seen))
	}
	if got := len(e.api.RequestsFor(histPath)); got != 2 {
		t.Fatalf("两次读只应发出 2 个 HTTP 请求，实际 %d", got)
	}
	if snap := e.counter.Snapshot(); snap.ExternalWrites() != 0 {
		t.Fatalf("读动作不得产生写副作用: %+v", snap)
	}
}

// TestSlackWriteIsGatedAndCounted 覆盖 CONN02：拒绝 → 0，允许一次 → 1。
func TestSlackWriteIsGatedAndCounted(t *testing.T) {
	args := map[string]any{"channel": fxChannel, "threadTs": fxThreadTS, "text": "已经合并"}

	t.Run("denied", func(t *testing.T) {
		e := newSlackEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) { c.Deny = true })
		res, err := e.env.Invoke(t, "reply_thread", args)
		if err != nil {
			t.Fatalf("策略拒绝应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("拒绝必须未派发: %+v", res)
		}
		if got := e.counter.Snapshot().SlackReplies; got != 0 {
			t.Fatalf("拒绝时 fixture 写计数必须为 0，实际 %d", got)
		}
		if got := len(e.api.RequestsFor(postPath)); got != 0 {
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
			t.Fatalf("审计含 bot token: %s", raw)
		}
	})

	t.Run("allowed", func(t *testing.T) {
		e := newSlackEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "reply_thread", args)
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone || !res.Dispatched {
			t.Fatalf("允许后应完成: %+v", res)
		}
		if got := e.counter.Snapshot().SlackReplies; got != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
		}
		writes := e.api.Writes()
		if len(writes) != 1 || writes[0].Target != fxChannel || writes[0].Text != "已经合并" {
			t.Fatalf("fixture 收到的写不对: %+v", writes)
		}
		rows := e.env.Audits(t, 0)
		if len(rows) != 1 || rows[0].Decision != "allow" || rows[0].Outcome != "done" {
			t.Fatalf("允许必须留 allow/done 审计: %+v", rows)
		}
		if rows[0].ToolName != "connector_slack_reply_thread" {
			t.Fatalf("工具名不对: %+v", rows[0])
		}
	})

	t.Run("out_of_scope_channel", func(t *testing.T) {
		e := newSlackEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "reply_thread", map[string]any{
			"channel": "C0OTHER", "threadTs": fxThreadTS, "text": "越界",
		})
		if err != nil {
			t.Fatalf("越界应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("越界必须未派发: %+v", res)
		}
		if got := e.counter.Snapshot().SlackReplies; got != 0 {
			t.Fatalf("越界时写计数必须为 0，实际 %d", got)
		}
	})
}

// TestSlackWriteTimeoutIsOutcomeUnknown 覆盖 CONN06。
func TestSlackWriteTimeoutIsOutcomeUnknown(t *testing.T) {
	e := newSlackEnv(t, fixtures.APIOptions{StallAfterApply: true}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	res, err := e.env.InvokeCtx(t, ctx, "reply_thread",
		map[string]any{"channel": fxChannel, "threadTs": fxThreadTS, "text": "会不会重复"}, "")
	if err != nil {
		t.Fatalf("派发后超时不应是协议错误: %v", err)
	}
	if res.Outcome != connectors.OutcomeUnknown {
		t.Fatalf("派发后超时必须返回 OutcomeUnknown，实际 %+v", res)
	}
	if res.SafeToRetry {
		t.Fatal("未知结果不得声明可安全重试")
	}
	if got := e.counter.Snapshot().SlackReplies; got != 1 {
		t.Fatalf("平台已收到一次回复，实际 %d", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := e.counter.Snapshot().SlackReplies; got != 1 {
		t.Fatalf("非幂等动作不得自动重放，写计数变成 %d", got)
	}
	if got := len(e.api.RequestsFor(postPath)); got != 1 {
		t.Fatalf("写请求只应发出一次，实际 %d", got)
	}
	rows := e.env.Audits(t, 0)
	if len(rows) != 1 || rows[0].Outcome != string(connectors.OutcomeUnknown) {
		t.Fatalf("未知结果必须留审计: %+v", rows)
	}
}

// TestSlackRateLimitIsEnforcedAndVisible 覆盖本地限流。
func TestSlackRateLimitIsEnforcedAndVisible(t *testing.T) {
	e := newSlackEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) {
		c.RateLimit = connectors.RateLimit{Requests: 2, PerSeconds: 60}
	})
	args := map[string]any{"channel": fxChannel, "threadTs": fxThreadTS, "text": "限流用例"}
	for i := 0; i < 2; i++ {
		if res, err := e.env.Invoke(t, "reply_thread", args); err != nil || res.Outcome != connectors.OutcomeDone {
			t.Fatalf("第 %d 次调用应该允许: %+v %v", i+1, res, err)
		}
	}
	before := len(e.api.Requests())
	approvalsBefore := e.env.Approvals()

	res, err := e.env.Invoke(t, "reply_thread", args)
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

// TestSlackIdempotencyDecisionIsExplicit 覆盖幂等判定。
func TestSlackIdempotencyDecisionIsExplicit(t *testing.T) {
	action, ok := connectors.LookupAction(connectors.KindSlack, "reply_thread")
	if !ok {
		t.Fatal("reply_thread 必须在 catalog 里")
	}
	if action.Idempotency != connectors.IdempotencyNone {
		t.Fatalf("Slack 回复不接受 Anvil 可控的幂等键，必须声明 none: %+v", action)
	}
	if action.Replayable("same-key") {
		t.Fatal("非幂等动作带任何键都不应被判定为可重放")
	}

	e := newSlackEnv(t, fixtures.APIOptions{}, nil)
	args := map[string]any{"channel": fxChannel, "threadTs": fxThreadTS, "text": "重试用例"}
	for i := 0; i < 2; i++ {
		if _, err := e.env.InvokeWithKey(t, "reply_thread", args, "same-key"); err != nil {
			t.Fatalf("第 %d 次调用: %v", i+1, err)
		}
	}
	if got := e.env.Approvals(); got != 2 {
		t.Fatalf("非幂等重试必须各自重新授权（两次审批），实际 %d", got)
	}
	if got := e.counter.Snapshot().SlackReplies; got != 2 {
		t.Fatalf("两次授权对应两次真实回复，实际 %d", got)
	}
}

// TestSlackHealthNeverGuessesConnected 覆盖 CONN01 的驱动侧：
// ok:false 的 200 响应不得被当成 connected。
func TestSlackHealthNeverGuessesConnected(t *testing.T) {
	e := newSlackEnv(t, fixtures.APIOptions{}, nil)
	ctx := context.Background()

	health, err := e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status != connectors.HealthConnected {
		t.Fatalf("正确 token 应 connected: %+v", health)
	}

	if err := e.env.Creds.Put(ctx, connectortest.DefaultCredentialRef, []byte("xoxb-revoked")); err != nil {
		t.Fatalf("写错凭证: %v", err)
	}
	health, err = e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status == connectors.HealthConnected {
		t.Fatalf("无效 token 不得报告 connected（200 + ok:false）: %+v", health)
	}
	if health.Remediation != connectors.RemediationAddCredentials {
		t.Fatalf("认证失败应提示补凭证: %+v", health)
	}
	if got := e.counter.Snapshot().HTTPUnauthorized; got == 0 {
		t.Fatalf("fixture 应记录一次未授权访问: %+v", e.counter.Snapshot())
	}
}
