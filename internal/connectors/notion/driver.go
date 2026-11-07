// Package notion 是 Notion 连接器（用户提供的 internal integration token）。
//
// V1 动作严格按计划：query（查数据库页）、create_page（在页面下新建子页）、
// update_page（改标题属性）。
//
// 认证纪律：integration token 只存在 OS 凭证库里；健康状态来自 /v1/users/me，
// 这是"这次令牌属于谁"的真实调用，不是配置检查。
package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "notion 连接器缺少 APIBase 端点", false)
	}
	if settings.Now == nil {
		settings.Now = time.Now
	}
	return &Driver{settings: settings}, nil
}

// Kind 返回连接器类型。
func (d *Driver) Kind() connectors.Kind { return connectors.KindNotion }

// Capabilities 返回本驱动真正实现的能力。
func (d *Driver) Capabilities() []connectors.Capability {
	return []connectors.Capability{connectors.CapabilityNotionRead, connectors.CapabilityNotionWrite}
}

// Actions 返回 catalog 里的动作。
func (d *Driver) Actions(ctx context.Context) ([]connectors.ActionDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return connectors.Catalog(connectors.KindNotion), nil
}

// Diagnose 用 /v1/users/me 做真实健康检查（令牌无效会是 401，而不是 connected）。
func (d *Driver) Diagnose(ctx context.Context, cred connectors.Credential) (connectors.Health, error) {
	token, err := d.token(cred.Secret)
	if err != nil {
		return httpx.HealthFromError("notion", err), nil
	}
	started := d.settings.Now()
	client := httpx.New(d.settings.Endpoints.APIBase, token)
	var me struct {
		Object string `json:"object"`
		ID     string `json:"id"`
		Name   string `json:"name"`
	}
	if err := client.Read(ctx, http.MethodGet, "/v1/users/me", nil, nil, &me); err != nil {
		return httpx.HealthFromError("notion", err), nil
	}
	if strings.TrimSpace(me.ID) == "" {
		return connectors.Health{
			Status:      connectors.HealthError,
			Detail:      "notion 没有返回 bot 身份（不显示 connected）",
			Remediation: connectors.RemediationCheckNetwork,
			CheckedAt:   d.settings.Now().UTC(),
		}, nil
	}
	return connectors.Health{
		Status:      connectors.HealthConnected,
		Detail:      "Notion 集成认证通过（/v1/users/me）",
		Remediation: connectors.RemediationNone,
		CheckedAt:   d.settings.Now().UTC(),
		LatencyMS:   d.settings.Now().Sub(started).Milliseconds(),
	}, nil
}

// Invoke 执行 catalog 动作。
func (d *Driver) Invoke(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	switch inv.ActionID {
	case "query", "create_page", "update_page":
	default:
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "notion 驱动不支持动作: "+inv.ActionID, false)
	}
	if err := ctx.Err(); err != nil {
		return connectors.Result{}, err
	}
	token, err := d.token(inv.Credential.Secret)
	if err != nil {
		return connectors.Result{}, err
	}
	client := httpx.New(d.settings.Endpoints.APIBase, token)
	switch inv.ActionID {
	case "query":
		return d.query(ctx, client, inv)
	case "create_page":
		return d.createPage(ctx, client, inv)
	}
	return d.updatePage(ctx, client, inv)
}

func (d *Driver) token(secret []byte) (string, error) {
	token := strings.TrimSpace(string(secret))
	if token == "" {
		return "", contracts.NewAppError(
			contracts.CodeUnauthorized, "notion 凭证为空（integration token 只保存在 OS 凭证库）", false)
	}
	return token, nil
}

// Page 是一页 Notion 页面（typed DTO）。
type Page struct {
	Object         string `json:"object"`
	ID             string `json:"id"`
	Title          string `json:"title"`
	LastEditedTime string `json:"last_edited_time"`
}

type queryArgs struct {
	DatabaseID string `json:"databaseId"`
	Cursor     string `json:"cursor"`
	Limit      int    `json:"limit"`
}

func (d *Driver) query(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args queryArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "query 参数不合法: "+err.Error(), false)
	}
	databaseID := strings.TrimSpace(args.DatabaseID)
	if databaseID == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "query 需要 databaseId", false)
	}
	body := map[string]any{"page_size": httpx.ClampLimit(args.Limit, defaultLimit, maxLimit)}
	if cursor := strings.TrimSpace(args.Cursor); cursor != "" {
		body["start_cursor"] = cursor
	}
	var out struct {
		Object     string `json:"object"`
		Results    []Page `json:"results"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	path := "/v1/databases/" + url.PathEscape(databaseID) + "/query"
	if err := client.Read(ctx, http.MethodPost, path, nil, body, &out); err != nil {
		return connectors.Result{}, err
	}
	next := strings.TrimSpace(out.NextCursor)
	page := httpx.NewPage(out.Results, next, out.HasMore && next != "")
	data, err := httpx.Data(page)
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      fmt.Sprintf("查询到 %d 个页面", len(page.Records)),
		Data:        data,
		CompletedAt: d.settings.Now().UTC(),
	}, nil
}

type createPageArgs struct {
	ParentID string `json:"parentId"`
	Title    string `json:"title"`
}

func (d *Driver) createPage(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args createPageArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "create_page 参数不合法: "+err.Error(), false)
	}
	parent := strings.TrimSpace(args.ParentID)
	if parent == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "create_page 需要 parentId", false)
	}
	if strings.TrimSpace(args.Title) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "create_page 需要非空 title", false)
	}
	body := map[string]any{
		"parent":     map[string]string{"page_id": parent},
		"properties": map[string]any{"title": args.Title},
	}
	var out Page
	res, err := client.Write(ctx, http.MethodPost, "/v1/pages", nil, body, &out)
	if err != nil {
		return connectors.Result{}, err
	}
	res.IdempotencyKey = inv.IdempotencyKey
	res.CompletedAt = d.settings.Now().UTC()
	res.Detail = fmt.Sprintf("已在页面 %s 下创建子页 %s", parent, out.ID)
	return res, nil
}

type updatePageArgs struct {
	PageID string `json:"pageId"`
	Title  string `json:"title"`
}

func (d *Driver) updatePage(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args updatePageArgs
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "update_page 参数不合法: "+err.Error(), false)
	}
	pageID := strings.TrimSpace(args.PageID)
	if pageID == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "update_page 需要 pageId", false)
	}
	if strings.TrimSpace(args.Title) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "update_page 需要非空 title", false)
	}
	body := map[string]any{"properties": map[string]any{"title": args.Title}}
	var out Page
	res, err := client.Write(ctx, http.MethodPatch, "/v1/pages/"+url.PathEscape(pageID), nil, body, &out)
	if err != nil {
		return connectors.Result{}, err
	}
	res.IdempotencyKey = inv.IdempotencyKey
	res.CompletedAt = d.settings.Now().UTC()
	res.Detail = fmt.Sprintf("已更新页面 %s 的标题", pageID)
	return res, nil
}
