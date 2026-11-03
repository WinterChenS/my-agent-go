package policybridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// Client 是端点的调用方实现。
//
// 生产路径上真正调用它的是 Pi 进程内的 TS 扩展（pi-extensions/harness-policy.ts），
// Go 侧这个 client 用于测试、以及将来本机 CLI/诊断复用同一套语义。
type Client struct {
	base    string
	token   string
	session string
	hc      *http.Client
}

// NewClient 构造客户端。timeout<=0 表示只受 ctx 约束（等待人工确认可能很久）。
func NewClient(ep Endpoint, token string, session contracts.SessionID, timeout time.Duration) *Client {
	return &Client{
		base:    ep.URL,
		token:   token,
		session: string(session),
		hc: &http.Client{
			Timeout: timeout,
			// 显式不走代理：本机常设 http_proxy，默认 Transport 会把 127.0.0.1 的请求
			// 也交给代理，结果是被代理返回一个看似"服务不可用"的响应，极难定位。
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				MaxIdleConns:          4,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
		},
	}
}

// Authorize 请求一次执行前授权。返回的 decision 只可能是 allow/deny；
// 人工确认在本调用内阻塞完成，因此调用方不需要额外状态机。
func (c *Client) Authorize(ctx context.Context, call policy.ToolCall) (policy.Decision, string, error) {
	body := authorizeRequest{
		ToolCallID:    call.ID,
		ToolName:      call.Name,
		Source:        string(call.Source),
		Origin:        call.Origin,
		Arguments:     call.Arguments,
		ResourceScope: call.ResourceScope,
	}
	var resp authorizeResponse
	if err := c.do(ctx, http.MethodPost, "/v1/authorize", body, &resp); err != nil {
		return policy.DecisionDeny, "", err
	}
	switch policy.Decision(resp.Decision) {
	case policy.DecisionAllow, policy.DecisionDeny:
	default:
		return policy.DecisionDeny, "", contracts.NewAppError(
			contracts.CodeInternal, "策略端点返回了未知结论: "+resp.Decision, false)
	}
	return policy.Decision(resp.Decision), resp.Reason, nil
}

// Resolve 提交人工结论。
func (c *Client) Resolve(ctx context.Context, id string, choice policy.ApprovalChoice) error {
	return c.do(ctx, http.MethodPost, "/v1/approvals/"+escapeSegment(id)+"/resolve",
		map[string]string{"choice": string(choice)}, nil)
}

// Revoke 撤销一条待审批调用。
func (c *Client) Revoke(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/approvals/"+escapeSegment(id)+"/revoke", map[string]any{}, nil)
}

// Health 探测端点是否可用（诊断用）。
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var rdr io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return contracts.NewAppError(contracts.CodeInternal, "编码请求失败: "+err.Error(), false)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "构造请求失败: "+err.Error(), false)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-MyAgent-Session", c.session)

	resp, err := c.hc.Do(req)
	if err != nil {
		return contracts.NewAppError(contracts.CodeControlUncertain, "策略端点不可达: "+err.Error(), false)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(payload, &e)
		code := e.Error.Code
		if code == "" {
			code = contracts.CodeUnauthorized
		}
		msg := e.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("策略端点返回 %d", resp.StatusCode)
		}
		return contracts.NewAppError(code, msg, false)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "解析响应失败: "+err.Error(), false)
	}
	return nil
}

// escapeSegment 对路径段做最小转义：审批 ID 只含 [a-z0-9-]，这里仍防一手。
func escapeSegment(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, []byte(fmt.Sprintf("%%%02X", c))...)
		}
	}
	return string(out)
}
