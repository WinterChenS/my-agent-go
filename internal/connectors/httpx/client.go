// Package httpx 是五个 HTTP 平台适配器（钉钉/飞书/Slack/Notion/GitHub）共用的
// 最小客户端。
//
// 这里只做一件必须做对的事：把"请求有没有真的发出去"变成可判定的事实。
// net/http 的 error 把"连不上"和"写出去以后没等到响应"揉在一起，而这两者
// 对非幂等写动作是完全不同的结论：
//
//	未写出        → 确定未派发（OutcomeFailed，可人工再试）
//	写出后失败/5xx → OutcomeUnknown（平台可能已经生效，禁止自动重放）
//
// 判定点用 httptrace 的 WroteRequest：请求字节全部写到连接上之后才置位。
// 这与 mail 驱动"SMTP 终止点写完才算派发"是同一条规则。
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/contracts"
)

// defaultTimeout 是单次 HTTP 交互上限（调用方通常给更短的 ctx 截止时间）。
const defaultTimeout = 30 * time.Second

// maxBody 是响应体上限：读动作不该无界地把平台的整份数据拉进内存。
const maxBody = 8 << 20

// Rejection 是平台在 **HTTP 2xx 里**表达的拒绝。
//
// Slack 用 {"ok":false,"error":...}，飞书用 {"code":!=0,"msg":...}：只看状态码
// 会把它们当成成功，那是最典型的"看起来成功了"。
type Rejection interface {
	Rejected() error
}

// Client 是带固定 base URL 与 bearer 令牌的客户端。
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// New 构造客户端。token 为空表示不带 Authorization（换取令牌的请求）。
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		hc: &http.Client{
			Timeout: defaultTimeout,
			// 不跟随重定向：认证与写动作的结论必须来自被调用的那个端点。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Base 返回 base URL（诊断用）。
func (c *Client) Base() string { return c.base }

// Response 是一次已收到的 HTTP 响应。
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	Method string
	Path   string
}

// Decode 把响应体解成 JSON（失败即结构化错误）。
func (r Response) Decode(out any) error {
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(r.Body)) == 0 {
		return contracts.NewAppError(contracts.CodeProviderError,
			fmt.Sprintf("%s %s 返回空响应体", r.Method, r.Path), false)
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return contracts.NewAppError(contracts.CodeProviderError,
			fmt.Sprintf("%s %s 返回的不是合法 JSON: %v", r.Method, r.Path, err), false)
	}
	return nil
}

// Do 发送一次请求。
//
// 返回的 dispatched 表示请求已经完全写出到连接：只有它为 true 时，"没有结论"
// 才能被当作 OutcomeUnknown。
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (Response, bool, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, false, err
	}
	if c.base == "" {
		return Response{}, false, contracts.NewAppError(
			contracts.CodeConfigMissing, "连接器缺少 API base 端点", false)
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return Response{}, false, contracts.NewAppError(
				contracts.CodeInternal, "序列化请求体失败: "+err.Error(), false)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return Response{}, false, contracts.NewAppError(
			contracts.CodeUnsupported, "构造请求失败: "+err.Error(), false)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err := c.hc.Do(req)
	if err != nil {
		if !wrote.Load() {
			return Response{}, false, contracts.NewAppError(
				contracts.CodeProviderUnreachable, "请求未能发出: "+err.Error(), true)
		}
		return Response{}, true, contracts.NewAppError(
			contracts.CodeRpcTimeout, "请求已发出但没有收到响应: "+err.Error(), true)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	out := Response{Status: resp.StatusCode, Header: resp.Header, Body: raw, Method: method, Path: path}
	if rerr != nil {
		return out, true, contracts.NewAppError(
			contracts.CodeRpcTimeout, "读取响应失败: "+rerr.Error(), true)
	}
	return out, true, nil
}

// Read 执行一次读请求。
//
// 读动作没有平台侧写副作用，因此任何失败都可以是确定失败（框架会把
// Dispatched 置 false，SafeToRetry=true）。
func (c *Client) Read(ctx context.Context, method, path string, query url.Values, body, out any) error {
	_, err := c.ReadWithResponse(ctx, method, path, query, body, out)
	return err
}

// ReadWithResponse 是 Read 的带响应头版本。
//
// 需要它的是 GitHub 的列表分页：下一页在 Link 头里，而不是在响应体里。
func (c *Client) ReadWithResponse(ctx context.Context, method, path string, query url.Values, body, out any) (Response, error) {
	resp, _, err := c.Do(ctx, method, path, query, body)
	if err != nil {
		return Response{}, err
	}
	if resp.Status/100 != 2 {
		return resp, statusError(resp)
	}
	if err := resp.Decode(out); err != nil {
		return resp, err
	}
	if rej, ok := out.(Rejection); ok {
		if rerr := rej.Rejected(); rerr != nil {
			return resp, rerr
		}
	}
	return resp, nil
}

// Write 执行一次写请求，并按 CONN06 归一结果。
//
//	未写出            → error（确定未派发）
//	4xx               → error（平台明确拒绝，没有副作用）
//	5xx               → Result{OutcomeUnknown}（平台可能已经生效）
//	写出后无响应       → Result{OutcomeUnknown}
//	2xx 且平台未拒绝   → Result{OutcomeDone}
func (c *Client) Write(ctx context.Context, method, path string, query url.Values, body, out any) (connectors.Result, error) {
	resp, dispatched, err := c.Do(ctx, method, path, query, body)
	if err != nil {
		if !dispatched {
			return connectors.Result{}, err
		}
		return connectors.Result{
			Outcome:    connectors.OutcomeUnknown,
			Dispatched: true,
			Detail:     "请求已发出但没有收到确认，平台侧可能已经生效: " + err.Error(),
		}, nil
	}
	if resp.Status/100 == 2 {
		if err := resp.Decode(out); err != nil {
			return connectors.Result{}, err
		}
		if rej, ok := out.(Rejection); ok {
			if rerr := rej.Rejected(); rerr != nil {
				// 平台在 2xx 里说"没做"：这是确定失败，不是未知结果。
				return connectors.Result{}, rerr
			}
		}
		return connectors.Result{Outcome: connectors.OutcomeDone, Dispatched: true}, nil
	}
	if resp.Status >= 500 {
		return connectors.Result{
			Outcome:    connectors.OutcomeUnknown,
			Dispatched: true,
			Detail: fmt.Sprintf("平台返回 HTTP %d，无法确认请求是否已生效: %s",
				resp.Status, platformMessage(resp.Body)),
		}, nil
	}
	return connectors.Result{}, statusError(resp)
}

// statusError 把 4xx 归一成带正确 code 的错误。
//
// 这些状态都表示"平台收到了并明确拒绝"，因此没有外部副作用。
func statusError(resp Response) error {
	detail := platformMessage(resp.Body)
	suffix := ""
	if detail != "" {
		suffix = "：" + detail
	}
	switch resp.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return contracts.NewAppError(contracts.CodeUnauthorized,
			fmt.Sprintf("%s %s 认证/授权被拒绝（HTTP %d）%s", resp.Method, resp.Path, resp.Status, suffix), false)
	case http.StatusNotFound:
		return contracts.NewAppError(contracts.CodeNotFound,
			fmt.Sprintf("%s %s 目标不存在（HTTP 404）%s", resp.Method, resp.Path, suffix), false)
	case http.StatusTooManyRequests:
		return contracts.NewAppError(contracts.CodeRateLimited,
			fmt.Sprintf("平台侧限流（HTTP 429）%s", suffix), false)
	}
	return contracts.NewAppError(contracts.CodeProviderError,
		fmt.Sprintf("%s %s 被平台拒绝（HTTP %d）%s", resp.Method, resp.Path, resp.Status, suffix), false)
}

// platformMessage 从平台的错误包里取一句话（不猜结构，只挑常见字段）。
func platformMessage(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return truncate(string(trimmed))
	}
	for _, key := range []string{"message", "msg", "error_description", "error", "detail"} {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return truncate(s)
			}
		}
	}
	if data, ok := obj["data"].(map[string]any); ok {
		for _, key := range []string{"message", "msg", "error"} {
			if s, ok := data[key].(string); ok && strings.TrimSpace(s) != "" {
				return truncate(s)
			}
		}
	}
	return truncate(string(trimmed))
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// Page 是读动作的统一返回信封。
//
// "稳定游标 + 明确的 hasMore"比裸数组更难被误用：调用方拿到 nextCursor 之前的
// 数据一定是不重叠的一页，而 hasMore 让"还有没有下一页"不靠猜。
type Page[T any] struct {
	Records    []T    `json:"records"`
	NextCursor string `json:"nextCursor,omitempty"`
	HasMore    bool   `json:"hasMore"`
}

// ClampLimit 归一读动作的 limit（缺省值 + 单页上限，防无界抓取）。
func ClampLimit(limit, fallback, max int) int {
	if limit <= 0 {
		limit = fallback
	}
	if max > 0 && limit > max {
		limit = max
	}
	return limit
}

// HealthFromError 把认证/网络错误分类成 blocked/error。
//
// 只有驱动完成一次通过认证的真实调用才能报告 connected；这个函数永远不返回
// connected，避免任何一个适配器把"配置齐了"当成"连上了"。
func HealthFromError(stage string, err error) connectors.Health {
	status := connectors.HealthBlocked
	remediation := connectors.RemediationCheckNetwork
	detail := stage + " 健康检查失败: " + err.Error()
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && appErr != nil {
		switch appErr.Code {
		case contracts.CodeUnauthorized:
			remediation = connectors.RemediationAddCredentials
			detail = stage + " 认证失败（凭证或应用注册可能已失效）: " + appErr.Message
		case contracts.CodeProviderUnreachable, contracts.CodeRpcTimeout:
			status = connectors.HealthError
		case contracts.CodeProviderError, contracts.CodeRateLimited:
			status = connectors.HealthError
		case contracts.CodeConfigMissing:
			remediation = connectors.RemediationFixSettings
		}
	}
	return connectors.Health{Status: status, Detail: detail, Remediation: remediation}
}

// NewPage 组装一页。
func NewPage[T any](records []T, next string, hasMore bool) Page[T] {
	if records == nil {
		records = []T{}
	}
	return Page[T]{Records: records, NextCursor: next, HasMore: hasMore}
}

// Data 把读结果编码进 Result.Data。
func Data(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化读结果失败: "+err.Error(), false)
	}
	return raw, nil
}
