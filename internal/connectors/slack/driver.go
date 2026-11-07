// Package slack 是 Slack 连接器（用户提供的 App：bot token）。
//
// V1 动作严格按计划：read_channel（读频道历史）、reply_thread（在线程里回复）。
//
// 认证纪律：
//   - bot token 只存在 OS 凭证库里（用户自己的 App 颁发），配置与审计里只有 ref；
//   - Slack 在 HTTP 200 里用 {"ok":false,"error":"..."} 表达拒绝，
//     适配器必须按错误处理——只看状态码会把 invalid_auth 当成功。
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/httpx"
	"my-agent-go/internal/contracts"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

// Settings 是驱动需要的非敏感连接参数。
type Settings struct {
	Endpoints connectors.Endpoints
	Now       func() time.Time
}

// Driver 实现 connectors.Driver。
type Driver struct {
	settings Settings
}

// New 构造驱动。
func New(settings Settings) (*Driver, error) {
	if strings.TrimSpace(settings.Endpoints.APIBase) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "slack 连接器缺少 APIBase 端点", false)
	}
	if settings.Now == nil {
		settings.Now = time.Now
	}
	return &Driver{settings: settings}, nil
}

// Kind 返回连接器类型。
func (d *Driver) Kind() connectors.Kind { return connectors.KindSlack }

// Capabilities 返回本驱动真正实现的能力。
func (d *Driver) Capabilities() []connectors.Capability {
	return []connectors.Capability{connectors.CapabilitySlackRead, connectors.CapabilitySlackSend}
}

// Actions 返回 catalog 里的动作。
func (d *Driver) Actions(ctx context.Context) ([]connectors.ActionDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return connectors.Catalog(connectors.KindSlack), nil
}

// Diagnose 调用 auth.test：这是 Slack 官方用于确认"这个 token 属于谁"的端点，
// 因此它是真实的、通过认证的健康检查。
func (d *Driver) Diagnose(ctx context.Context, cred connectors.Credential) (connectors.Health, error) {
	token, err := d.token(cred.Secret)
	if err != nil {
		return httpx.HealthFromError("slack", err), nil
	}
	started := d.settings.Now()
	client := httpx.New(d.settings.Endpoints.APIBase, token)
	var out authTestResponse
	if err := client.Read(ctx, http.MethodPost, "/api/auth.test", nil, nil, &out); err != nil {
		return httpx.HealthFromError("slack", err), nil
	}
	return connectors.Health{
		Status:      connectors.HealthConnected,
		Detail:      "Slack 认证通过（auth.test）",
		Remediation: connectors.RemediationNone,
		CheckedAt:   d.settings.Now().UTC(),
		LatencyMS:   d.settings.Now().Sub(started).Milliseconds(),
	}, nil
}

// Invoke 执行 catalog 动作。
func (d *Driver) Invoke(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	switch inv.ActionID {
	case "read_channel", "reply_thread":
	default:
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "slack 驱动不支持动作: "+inv.ActionID, false)
	}
	if err := ctx.Err(); err != nil {
		return connectors.Result{}, err
	}
	token, err := d.token(inv.Credential.Secret)
	if err != nil {
		return connectors.Result{}, err
	}
	client := httpx.New(d.settings.Endpoints.APIBase, token)
	if inv.ActionID == "read_channel" {
		return d.readChannel(ctx, client, inv)
	}
	return d.replyThread(ctx, client, inv)
}

// token 校验凭证明文格式（Slack 的 token 是不透明字符串，只做非空校验）。
func (d *Driver) token(secret []byte) (string, error) {
	token := strings.TrimSpace(string(secret))
	if token == "" {
		return "", contracts.NewAppError(
			contracts.CodeUnauthorized, "slack 凭证为空（token 只保存在 OS 凭证库）", false)
	}
	return token, nil
}

// envelope 是 Slack 的统一返回包：HTTP 200 + ok:false 也是拒绝。
type envelope struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
}

// Rejected 把 ok:false 归一成结构化错误。
func (e envelope) Rejected() error {
	if e.Ok {
		return nil
	}
	code := contracts.CodeProviderError
	switch e.Error {
	case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive":
		code = contracts.CodeUnauthorized
	case "channel_not_found", "thread_not_found":
		code = contracts.CodeNotFound
	case "ratelimited":
		code = contracts.CodeRateLimited
	}
	detail := strings.TrimSpace(e.Error)
	if detail == "" {
		detail = "未给出错误码"
	}
	return contracts.NewAppError(code, "Slack 返回失败: "+detail, false)
}

type authTestResponse struct {
	envelope
	Team   string `json:"team"`
	UserID string `json:"user_id"`
}

// Message 是频道里的一条消息（typed DTO）。
type Message struct {
	TS         string `json:"ts"`
	User       string `json:"user"`
	Text       string `json:"text"`
	ThreadTS   string `json:"thread_ts"`
	ReplyCount int    `json:"reply_count"`
}

type readChannelArgs struct {
	Channel string `json:"channel"`
	Cursor  string `json:"cursor"`
	Limit   int    `json:"limit"`
}

func (d *Driver) readChannel(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args readChannelArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_channel 参数不合法: "+err.Error(), false)
	}
	channel := strings.TrimSpace(args.Channel)
	if channel == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_channel 需要 channel", false)
	}
	query := url.Values{
		"channel": {channel},
		"limit":   {strconv.Itoa(httpx.ClampLimit(args.Limit, defaultLimit, maxLimit))},
	}
	if cursor := strings.TrimSpace(args.Cursor); cursor != "" {
		query.Set("cursor", cursor)
	}
	var out struct {
		envelope
		Messages         []Message `json:"messages"`
		HasMore          bool      `json:"has_more"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := client.Read(ctx, http.MethodGet, "/api/conversations.history", query, nil, &out); err != nil {
		return connectors.Result{}, err
	}
	next := strings.TrimSpace(out.ResponseMetadata.NextCursor)
	page := httpx.NewPage(out.Messages, next, next != "")
	data, err := httpx.Data(page)
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      fmt.Sprintf("读取到 %d 条频道消息", len(page.Records)),
		Data:        data,
		CompletedAt: d.settings.Now().UTC(),
	}, nil
}

type replyThreadArgs struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"threadTs"`
	Text     string `json:"text"`
}

func (d *Driver) replyThread(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args replyThreadArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "reply_thread 参数不合法: "+err.Error(), false)
	}
	channel := strings.TrimSpace(args.Channel)
	if channel == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "reply_thread 需要 channel", false)
	}
	if strings.TrimSpace(args.ThreadTS) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "reply_thread 需要 threadTs", false)
	}
	if strings.TrimSpace(args.Text) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "reply_thread 需要非空 text", false)
	}
	var out struct {
		envelope
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	body := map[string]string{
		"channel":   channel,
		"thread_ts": strings.TrimSpace(args.ThreadTS),
		"text":      args.Text,
	}
	res, err := client.Write(ctx, http.MethodPost, "/api/chat.postMessage", nil, body, &out)
	if err != nil {
		return connectors.Result{}, err
	}
	res.IdempotencyKey = inv.IdempotencyKey
	res.CompletedAt = d.settings.Now().UTC()
	res.Detail = fmt.Sprintf("已在 %s 的线 %s 下回复（ts=%s）", channel, args.ThreadTS, out.TS)
	return res, nil
}
