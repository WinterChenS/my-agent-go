// Package feishu 是飞书连接器（企业自建应用：App ID/App Secret）。
//
// V1 动作严格按计划：read_doc（读文档块）、update_doc（整体更新文档内容）、
// send_message（发消息到群）。
//
// 认证纪律：
//   - App ID/App Secret 只存在 OS 凭证库里，格式是 "AppID:AppSecret"；
//   - 每次调用换一个 tenant_access_token，令牌只在本次请求的内存里存活；
//   - 飞书在 HTTP 200 里用 code!=0 表达拒绝，适配器必须把这个当真拒绝，
//     不能因为"状态码是 200"就当成功。
package feishu

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
	credentialSeparator = ":"
	defaultLimit        = 20
	maxLimit            = 100
	// 飞书的令牌/应用凭据类错误码：这些必须是"需要重新授权"，而不是普通服务错误。
	codeAppAuthFailed = 99991663
	codeAppNotFound   = 99991661
	codeTokenInvalid  = 99991668
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
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "feishu 连接器缺少 APIBase 端点", false)
	}
	if settings.Now == nil {
		settings.Now = time.Now
	}
	return &Driver{settings: settings}, nil
}

// Kind 返回连接器类型。
func (d *Driver) Kind() connectors.Kind { return connectors.KindFeishu }

// Capabilities 返回本驱动真正实现的能力。
func (d *Driver) Capabilities() []connectors.Capability {
	return []connectors.Capability{
		connectors.CapabilityFeishuRead,
		connectors.CapabilityFeishuWrite,
		connectors.CapabilityFeishuSend,
	}
}

// Actions 返回 catalog 里的动作（风险/幂等由 catalog 决定）。
func (d *Driver) Actions(ctx context.Context) ([]connectors.ActionDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return connectors.Catalog(connectors.KindFeishu), nil
}

// Diagnose 做一次通过认证的真实调用：换取 tenant_access_token。
//
// 换取令牌必须带应用凭据，因此"能换到令牌"就是"应用注册有效"的证据；
// 换不到就是 blocked（需要重新提供应用注册），绝不是 connected。
func (d *Driver) Diagnose(ctx context.Context, cred connectors.Credential) (connectors.Health, error) {
	started := d.settings.Now()
	if _, err := d.tenantToken(ctx, cred.Secret); err != nil {
		return httpx.HealthFromError("feishu 令牌", err), nil
	}
	return connectors.Health{
		Status:      connectors.HealthConnected,
		Detail:      "飞书应用认证通过（已换取 tenant_access_token）",
		Remediation: connectors.RemediationNone,
		CheckedAt:   d.settings.Now().UTC(),
		LatencyMS:   d.settings.Now().Sub(started).Milliseconds(),
	}, nil
}

// Invoke 执行 catalog 动作。
func (d *Driver) Invoke(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	switch inv.ActionID {
	case "read_doc", "update_doc", "send_message":
	default:
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "feishu 驱动不支持动作: "+inv.ActionID, false)
	}
	if err := ctx.Err(); err != nil {
		return connectors.Result{}, err
	}
	token, err := d.tenantToken(ctx, inv.Credential.Secret)
	if err != nil {
		return connectors.Result{}, err
	}
	client := httpx.New(d.settings.Endpoints.APIBase, token)
	switch inv.ActionID {
	case "read_doc":
		return d.readDoc(ctx, client, inv)
	case "update_doc":
		return d.updateDoc(ctx, client, inv)
	}
	return d.sendMessage(ctx, client, inv)
}

// envelope 是飞书的统一返回包：HTTP 200 + code!=0 也算拒绝。
type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// Rejected 把 code!=0 归一成结构化错误（httpx 会在 2xx 之后调用）。
func (e envelope) Rejected() error {
	if e.Code == 0 {
		return nil
	}
	code := contracts.CodeProviderError
	switch e.Code {
	case codeAppAuthFailed, codeAppNotFound, codeTokenInvalid:
		code = contracts.CodeUnauthorized
	}
	return contracts.NewAppError(code,
		"飞书返回错误 code="+strconv.Itoa(e.Code)+": "+strings.TrimSpace(e.Msg), false)
}

// tenantToken 用 App ID/App Secret 换 tenant_access_token。
func (d *Driver) tenantToken(ctx context.Context, secret []byte) (string, error) {
	appID, appSecret, err := splitCredential(secret)
	if err != nil {
		return "", err
	}
	client := httpx.New(d.settings.Endpoints.APIBase, "")
	var out struct {
		envelope
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	body := map[string]string{"app_id": appID, "app_secret": appSecret}
	if err := client.Read(ctx, http.MethodPost,
		"/open-apis/auth/v3/tenant_access_token/internal", nil, body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.TenantAccessToken) == "" {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "飞书没有返回 tenant_access_token", false)
	}
	return out.TenantAccessToken, nil
}

// splitCredential 拆开 "AppID:AppSecret"。
func splitCredential(secret []byte) (string, string, error) {
	appID, appSecret, found := strings.Cut(string(secret), credentialSeparator)
	appID = strings.TrimSpace(appID)
	appSecret = strings.TrimSpace(appSecret)
	if !found || appID == "" || appSecret == "" {
		return "", "", contracts.NewAppError(
			contracts.CodeUnauthorized,
			"feishu 凭证格式应为 AppID:AppSecret（只保存在 OS 凭证库）",
			false,
		)
	}
	return appID, appSecret, nil
}

// Block 是文档里的一个块（typed DTO）。
type Block struct {
	ID        string `json:"block_id"`
	BlockType int    `json:"block_type"`
	Text      string `json:"text"`
}

type readDocArgs struct {
	DocumentID string `json:"documentId"`
	Cursor     string `json:"cursor"`
	Limit      int    `json:"limit"`
}

func (d *Driver) readDoc(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args readDocArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_doc 参数不合法: "+err.Error(), false)
	}
	docID := strings.TrimSpace(args.DocumentID)
	if docID == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_doc 需要 documentId", false)
	}
	query := url.Values{"page_size": {strconv.Itoa(httpx.ClampLimit(args.Limit, defaultLimit, maxLimit))}}
	if cursor := strings.TrimSpace(args.Cursor); cursor != "" {
		query.Set("page_token", cursor)
	}
	var out struct {
		envelope
		Data struct {
			Items     []Block `json:"items"`
			HasMore   bool    `json:"has_more"`
			PageToken string  `json:"page_token"`
		} `json:"data"`
	}
	path := "/open-apis/docx/v1/documents/" + url.PathEscape(docID) + "/blocks"
	if err := client.Read(ctx, http.MethodGet, path, query, nil, &out); err != nil {
		return connectors.Result{}, err
	}
	data, err := httpx.Data(httpx.NewPage(out.Data.Items, out.Data.PageToken, out.Data.HasMore))
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      fmt.Sprintf("读取到 %d 个文档块", len(out.Data.Items)),
		Data:        data,
		CompletedAt: d.settings.Now().UTC(),
	}, nil
}

type updateDocArgs struct {
	DocumentID string `json:"documentId"`
	Content    string `json:"content"`
}

func (d *Driver) updateDoc(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args updateDocArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "update_doc 参数不合法: "+err.Error(), false)
	}
	docID := strings.TrimSpace(args.DocumentID)
	if docID == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "update_doc 需要 documentId", false)
	}
	if strings.TrimSpace(args.Content) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "update_doc 需要非空 content", false)
	}
	var out struct {
		envelope
		Data struct {
			DocumentRevisionID int `json:"document_revision_id"`
		} `json:"data"`
	}
	body := map[string]any{"content": args.Content}
	path := "/open-apis/docx/v1/documents/" + url.PathEscape(docID) + "/blocks/batch_update"
	res, err := client.Write(ctx, http.MethodPost, path, nil, body, &out)
	if err != nil {
		return connectors.Result{}, err
	}
	res.IdempotencyKey = inv.IdempotencyKey
	res.CompletedAt = d.settings.Now().UTC()
	res.Detail = fmt.Sprintf("已更新文档 %s（revision %d）", docID, out.Data.DocumentRevisionID)
	return res, nil
}

type sendMessageArgs struct {
	ChatID string `json:"chatId"`
	Text   string `json:"text"`
}

func (d *Driver) sendMessage(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args sendMessageArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "send_message 参数不合法: "+err.Error(), false)
	}
	chatID := strings.TrimSpace(args.ChatID)
	if chatID == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "send_message 需要 chatId", false)
	}
	if strings.TrimSpace(args.Text) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "send_message 需要非空 text", false)
	}
	content, err := json.Marshal(map[string]string{"text": args.Text})
	if err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeInternal, "序列化消息体失败: "+err.Error(), false)
	}
	var out struct {
		envelope
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	query := url.Values{"receive_id_type": {"chat_id"}}
	body := map[string]string{
		"receive_id": chatID,
		"msg_type":   "text",
		"content":    string(content),
	}
	res, err := client.Write(ctx, http.MethodPost, "/open-apis/im/v1/messages", query, body, &out)
	if err != nil {
		return connectors.Result{}, err
	}
	res.IdempotencyKey = inv.IdempotencyKey
	res.CompletedAt = d.settings.Now().UTC()
	res.Detail = fmt.Sprintf("已向会话 %s 发送消息（%s）", chatID, out.Data.MessageID)
	return res, nil
}
