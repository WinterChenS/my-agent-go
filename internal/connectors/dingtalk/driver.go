// Package dingtalk 是钉钉连接器（企业自建应用：AppKey/AppSecret）。
//
// V1 动作严格按计划：read_scope（读应用被授权的范围）、send_group（发群消息）。
//
// 认证纪律（与 C08a 的整体契约一致）：
//   - AppKey/AppSecret 只存在 OS 凭证库里，格式是 "AppKey:AppSecret"；
//   - 每次调用用它换一个 access token，令牌只在本次请求的内存里存活，
//     不写配置、不写审计、不出现在结果里；
//   - 健康状态只能来自一次通过认证的真实调用。
package dingtalk

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
	// credentialSeparator 分隔用户提供的 AppKey 与 AppSecret。
	credentialSeparator = ":"
	// defaultLimit / maxLimit 让读动作有界：V1 不允许"把权限范围全抓下来"。
	defaultLimit = 20
	maxLimit     = 100
)

// Settings 是驱动需要的非敏感连接参数。
type Settings struct {
	Endpoints connectors.Endpoints
	// Now 便于测试注入时钟。
	Now func() time.Time
}

// Driver 实现 connectors.Driver。
type Driver struct {
	settings Settings
}

// New 构造驱动。缺少 API 基地址时直接失败：让"没配置"出现在连接器页，
// 而不是等第一次派发才炸。
func New(settings Settings) (*Driver, error) {
	if strings.TrimSpace(settings.Endpoints.APIBase) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "dingtalk 连接器缺少 APIBase 端点", false)
	}
	if settings.Now == nil {
		settings.Now = time.Now
	}
	return &Driver{settings: settings}, nil
}

// Kind 返回连接器类型。
func (d *Driver) Kind() connectors.Kind { return connectors.KindDingtalk }

// Capabilities 返回本驱动真正实现的能力。
func (d *Driver) Capabilities() []connectors.Capability {
	return []connectors.Capability{connectors.CapabilityDingtalkRead, connectors.CapabilityDingtalkSend}
}

// Actions 返回 catalog 里的动作（风险/幂等由 catalog 决定，驱动不得降级）。
func (d *Driver) Actions(ctx context.Context) ([]connectors.ActionDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return connectors.Catalog(connectors.KindDingtalk), nil
}

// Diagnose 做一次通过认证的真实健康调用。
//
// 两步都要成功：先换令牌（应用注册有效），再用令牌读一次权限范围（应用真的被
// 授权）。只换到令牌就报 connected 会把"应用存在但没授权"显示成可用。
func (d *Driver) Diagnose(ctx context.Context, cred connectors.Credential) (connectors.Health, error) {
	started := d.settings.Now()
	token, err := d.accessToken(ctx, cred.Secret)
	if err != nil {
		return httpx.HealthFromError("dingtalk 令牌", err), nil
	}
	client := httpx.New(d.settings.Endpoints.APIBase, token)
	var page scopePage
	query := url.Values{"maxResults": {strconv.Itoa(1)}}
	if err := client.Read(ctx, http.MethodGet, "/v1.0/contact/scopes", query, nil, &page); err != nil {
		return httpx.HealthFromError("dingtalk 权限范围", err), nil
	}
	return connectors.Health{
		Status:      connectors.HealthConnected,
		Detail:      "钉钉应用认证通过（已换取 access token 并读取权限范围）",
		Remediation: connectors.RemediationNone,
		CheckedAt:   d.settings.Now().UTC(),
		LatencyMS:   d.settings.Now().Sub(started).Milliseconds(),
	}, nil
}

// Invoke 执行 catalog 动作。
func (d *Driver) Invoke(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	switch inv.ActionID {
	case "read_scope", "send_group":
	default:
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "dingtalk 驱动不支持动作: "+inv.ActionID, false)
	}
	if err := ctx.Err(); err != nil {
		return connectors.Result{}, err
	}
	token, err := d.accessToken(ctx, inv.Credential.Secret)
	if err != nil {
		return connectors.Result{}, err
	}
	client := httpx.New(d.settings.Endpoints.APIBase, token)
	if inv.ActionID == "read_scope" {
		return d.readScope(ctx, client, inv)
	}
	return d.sendGroup(ctx, client, inv)
}

// accessToken 用 AppKey/AppSecret 换一次访问令牌。
//
// 令牌不缓存：缓存会把"凭证被撤销后仍然能用"变成常态，而一次令牌交换的代价
// 远小于一次错误的外发。
func (d *Driver) accessToken(ctx context.Context, secret []byte) (string, error) {
	appKey, appSecret, err := splitCredential(secret)
	if err != nil {
		return "", err
	}
	client := httpx.New(d.settings.Endpoints.APIBase, "")
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	body := map[string]string{"appKey": appKey, "appSecret": appSecret}
	if err := client.Read(ctx, http.MethodPost, "/v1.0/oauth2/accessToken", nil, body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "钉钉没有返回 access token", false)
	}
	return out.AccessToken, nil
}

// splitCredential 拆开 "AppKey:AppSecret"。
//
// 只按第一个冒号切：secret 里允许出现冒号，AppKey 不允许（平台规则）。
func splitCredential(secret []byte) (string, string, error) {
	raw := string(secret)
	key, value, found := strings.Cut(raw, credentialSeparator)
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if !found || key == "" || value == "" {
		return "", "", contracts.NewAppError(
			contracts.CodeUnauthorized,
			"dingtalk 凭证格式应为 AppKey:AppSecret（只保存在 OS 凭证库）",
			false,
		)
	}
	return key, value, nil
}

// Scope 是一条应用权限范围（typed DTO）。
type Scope struct {
	ID   string `json:"scopeId"`
	Name string `json:"name"`
}

type scopePage struct {
	Scopes    []Scope `json:"scopes"`
	NextToken string  `json:"nextToken"`
}

type readScopeArgs struct {
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

func (d *Driver) readScope(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args readScopeArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_scope 参数不合法: "+err.Error(), false)
	}
	limit := httpx.ClampLimit(args.Limit, defaultLimit, maxLimit)
	query := url.Values{"maxResults": {strconv.Itoa(limit)}}
	if cursor := strings.TrimSpace(args.Cursor); cursor != "" {
		query.Set("nextToken", cursor)
	}
	var page scopePage
	if err := client.Read(ctx, http.MethodGet, "/v1.0/contact/scopes", query, nil, &page); err != nil {
		return connectors.Result{}, err
	}
	data, err := httpx.Data(httpx.NewPage(page.Scopes, page.NextToken, page.NextToken != ""))
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      fmt.Sprintf("读取到 %d 条权限范围", len(page.Scopes)),
		Data:        data,
		CompletedAt: d.settings.Now().UTC(),
	}, nil
}

type sendGroupArgs struct {
	ConversationID string `json:"conversationId"`
	Text           string `json:"text"`
}

func (d *Driver) sendGroup(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args sendGroupArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "send_group 参数不合法: "+err.Error(), false)
	}
	conversation := strings.TrimSpace(args.ConversationID)
	if conversation == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "send_group 需要 conversationId", false)
	}
	if strings.TrimSpace(args.Text) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "send_group 需要非空 text", false)
	}
	msgParam, err := json.Marshal(map[string]string{"content": args.Text})
	if err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeInternal, "序列化消息体失败: "+err.Error(), false)
	}
	var out struct {
		ProcessQueryKey string `json:"processQueryKey"`
	}
	res, err := client.Write(ctx, http.MethodPost, "/v1.0/robot/groupMessages/send", nil, map[string]string{
		"openConversationId": conversation,
		"msgKey":             "sampleText",
		"msgParam":           string(msgParam),
	}, &out)
	if err != nil {
		return connectors.Result{}, err
	}
	res.IdempotencyKey = inv.IdempotencyKey
	res.CompletedAt = d.settings.Now().UTC()
	res.Detail = fmt.Sprintf("已向群会话 %s 发送消息", conversation)
	return res, nil
}
