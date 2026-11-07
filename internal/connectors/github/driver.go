// Package github 是 GitHub 连接器（用户提供的细粒度 PAT）。
//
// V1 动作严格按计划：read_repo、read_issue、read_pr（含改动文件分页）、
// create_issue、comment_pr。
//
// 认证选择（见 docs/acceptance/v1-external-dependencies.md）：GitHub 桌面端优先
// device flow 或细粒度 PAT；V1 这里只接受用户提供的 PAT，PAT 只存在 OS 凭证库。
// 没有把 client secret 编译进二进制的路径。
package github

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
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "github 连接器缺少 APIBase 端点", false)
	}
	if settings.Now == nil {
		settings.Now = time.Now
	}
	return &Driver{settings: settings}, nil
}

// Kind 返回连接器类型。
func (d *Driver) Kind() connectors.Kind { return connectors.KindGitHub }

// Capabilities 返回本驱动真正实现的能力。
func (d *Driver) Capabilities() []connectors.Capability {
	return []connectors.Capability{connectors.CapabilityGitHubRead, connectors.CapabilityGitHubWrite}
}

// Actions 返回 catalog 里的动作。
func (d *Driver) Actions(ctx context.Context) ([]connectors.ActionDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return connectors.Catalog(connectors.KindGitHub), nil
}

// Diagnose 用 /user 做真实健康检查：它同时验证令牌有效与"这是谁"。
func (d *Driver) Diagnose(ctx context.Context, cred connectors.Credential) (connectors.Health, error) {
	token, err := d.token(cred.Secret)
	if err != nil {
		return httpx.HealthFromError("github", err), nil
	}
	started := d.settings.Now()
	client := httpx.New(d.settings.Endpoints.APIBase, token)
	var me struct {
		Login string `json:"login"`
		ID    int    `json:"id"`
	}
	if err := client.Read(ctx, http.MethodGet, "/user", nil, nil, &me); err != nil {
		return httpx.HealthFromError("github", err), nil
	}
	if strings.TrimSpace(me.Login) == "" {
		return connectors.Health{
			Status:      connectors.HealthError,
			Detail:      "GitHub 没有返回账号身份（不显示 connected）",
			Remediation: connectors.RemediationCheckNetwork,
			CheckedAt:   d.settings.Now().UTC(),
		}, nil
	}
	return connectors.Health{
		Status:      connectors.HealthConnected,
		Detail:      "GitHub 令牌认证通过（/user）",
		Remediation: connectors.RemediationNone,
		CheckedAt:   d.settings.Now().UTC(),
		LatencyMS:   d.settings.Now().Sub(started).Milliseconds(),
	}, nil
}

// Invoke 执行 catalog 动作。
func (d *Driver) Invoke(ctx context.Context, inv connectors.Invocation) (connectors.Result, error) {
	switch inv.ActionID {
	case "read_repo", "read_issue", "read_pr", "create_issue", "comment_pr":
	default:
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "github 驱动不支持动作: "+inv.ActionID, false)
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
	case "read_repo":
		return d.readRepo(ctx, client, inv)
	case "read_issue":
		return d.readIssue(ctx, client, inv)
	case "read_pr":
		return d.readPR(ctx, client, inv)
	case "create_issue":
		return d.createIssue(ctx, client, inv)
	}
	return d.commentPR(ctx, client, inv)
}

func (d *Driver) token(secret []byte) (string, error) {
	token := strings.TrimSpace(string(secret))
	if token == "" {
		return "", contracts.NewAppError(
			contracts.CodeUnauthorized, "github 凭证为空（PAT 只保存在 OS 凭证库）", false)
	}
	return token, nil
}

// splitRepo 把 "owner/name" 拆成两段，并拒绝更复杂的形式。
func splitRepo(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	owner, name, found := strings.Cut(value, "/")
	if !found || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return "", "", contracts.NewAppError(
			contracts.CodeUnsupported, "repo 参数必须是 owner/name 形式: "+value, false)
	}
	return owner, name, nil
}

// Repo 是一个仓库（typed DTO）。
type Repo struct {
	ID            int    `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// Issue 是一个议题（typed DTO）。
type Issue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Author  string `json:"author"`
	HTMLURL string `json:"htmlUrl"`
}

// PullRequest 是一个合并请求（typed DTO）。
type PullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HeadRef string `json:"headRef"`
	BaseRef string `json:"baseRef"`
}

// PRFile 是 PR 里一个改动文件（typed DTO）。
type PRFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

func (d *Driver) readRepo(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_repo 参数不合法: "+err.Error(), false)
	}
	owner, name, err := splitRepo(args.Repo)
	if err != nil {
		return connectors.Result{}, err
	}
	var raw struct {
		ID            int    `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
	}
	if err := client.Read(ctx, http.MethodGet,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, nil, &raw); err != nil {
		return connectors.Result{}, err
	}
	repo := Repo{
		ID:            raw.ID,
		FullName:      raw.FullName,
		DefaultBranch: raw.DefaultBranch,
		Private:       raw.Private,
	}
	data, err := httpx.Data(repo)
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      "已读取仓库 " + repo.FullName,
		Data:        data,
		CompletedAt: d.settings.Now().UTC(),
	}, nil
}

func (d *Driver) readIssue(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_issue 参数不合法: "+err.Error(), false)
	}
	owner, name, err := splitRepo(args.Repo)
	if err != nil {
		return connectors.Result{}, err
	}
	if args.Number <= 0 {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_issue 需要正整数 number", false)
	}
	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
		HTMLURL string `json:"html_url"`
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(name), args.Number)
	if err := client.Read(ctx, http.MethodGet, path, nil, nil, &raw); err != nil {
		return connectors.Result{}, err
	}
	issue := Issue{
		Number:  raw.Number,
		Title:   raw.Title,
		State:   raw.State,
		Author:  raw.User.Login,
		HTMLURL: raw.HTMLURL,
	}
	data, err := httpx.Data(issue)
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      fmt.Sprintf("已读取议题 #%d: %s", issue.Number, issue.Title),
		Data:        data,
		CompletedAt: d.settings.Now().UTC(),
	}, nil
}

// pullRequestPage 是 read_pr 的返回：PR 元数据 + 一页改动文件。
type pullRequestPage struct {
	PullRequest PullRequest        `json:"pullRequest"`
	Files       httpx.Page[PRFile] `json:"files"`
}

func (d *Driver) readPR(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_pr 参数不合法: "+err.Error(), false)
	}
	owner, name, err := splitRepo(args.Repo)
	if err != nil {
		return connectors.Result{}, err
	}
	if args.Number <= 0 {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "read_pr 需要正整数 number", false)
	}
	pageNumber := 1
	if cursor := strings.TrimSpace(args.Cursor); cursor != "" {
		parsed, perr := strconv.Atoi(cursor)
		if perr != nil || parsed <= 0 {
			return connectors.Result{}, contracts.NewAppError(
				contracts.CodeUnsupported, "read_pr 的 cursor 必须是正整数页码: "+cursor, false)
		}
		pageNumber = parsed
	}
	size := httpx.ClampLimit(args.Limit, 2, maxLimit)
	basePath := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(name), args.Number)

	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := client.Read(ctx, http.MethodGet, basePath, nil, nil, &raw); err != nil {
		return connectors.Result{}, err
	}

	query := url.Values{"page": {strconv.Itoa(pageNumber)}, "per_page": {strconv.Itoa(size)}}
	var files []PRFile
	resp, err := client.ReadWithResponse(ctx, http.MethodGet, basePath+"/files", query, nil, &files)
	if err != nil {
		return connectors.Result{}, err
	}
	// GitHub 的下一页在 Link 头里：只认 rel="next"，不猜"这一页满了就是还有"。
	hasMore := linkHasNext(resp.Header.Get("Link"))
	next := ""
	if hasMore {
		next = strconv.Itoa(pageNumber + 1)
	}
	page := pullRequestPage{
		PullRequest: PullRequest{
			Number:  raw.Number,
			Title:   raw.Title,
			State:   raw.State,
			HeadRef: raw.Head.Ref,
			BaseRef: raw.Base.Ref,
		},
		Files: httpx.NewPage(files, next, hasMore),
	}
	data, err := httpx.Data(page)
	if err != nil {
		return connectors.Result{}, err
	}
	return connectors.Result{
		Outcome:     connectors.OutcomeDone,
		Detail:      fmt.Sprintf("已读取 PR #%d（%d 个改动文件）", raw.Number, len(page.Files.Records)),
		Data:        data,
		CompletedAt: d.settings.Now().UTC(),
	}, nil
}

// linkHasNext 判断 Link 头里是否有 rel="next"。
func linkHasNext(header string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.Contains(part, `rel="next"`) {
			return true
		}
	}
	return false
}

func (d *Driver) createIssue(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		Repo  string `json:"repo"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "create_issue 参数不合法: "+err.Error(), false)
	}
	owner, name, err := splitRepo(args.Repo)
	if err != nil {
		return connectors.Result{}, err
	}
	if strings.TrimSpace(args.Title) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "create_issue 需要非空 title", false)
	}
	var out struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/issues"
	body := map[string]string{"title": args.Title, "body": args.Body}
	res, err := client.Write(ctx, http.MethodPost, path, nil, body, &out)
	if err != nil {
		return connectors.Result{}, err
	}
	res.IdempotencyKey = inv.IdempotencyKey
	res.CompletedAt = d.settings.Now().UTC()
	res.Detail = fmt.Sprintf("已在 %s 创建议题 #%d", args.Repo, out.Number)
	return res, nil
}

func (d *Driver) commentPR(ctx context.Context, client *httpx.Client, inv connectors.Invocation) (connectors.Result, error) {
	var args struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(inv.Arguments, &args); err != nil {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "comment_pr 参数不合法: "+err.Error(), false)
	}
	owner, name, err := splitRepo(args.Repo)
	if err != nil {
		return connectors.Result{}, err
	}
	if args.Number <= 0 {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "comment_pr 需要正整数 number", false)
	}
	if strings.TrimSpace(args.Body) == "" {
		return connectors.Result{}, contracts.NewAppError(
			contracts.CodeUnsupported, "comment_pr 需要非空 body", false)
	}
	var out struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments",
		url.PathEscape(owner), url.PathEscape(name), args.Number)
	res, err := client.Write(ctx, http.MethodPost, path, nil, map[string]string{"body": args.Body}, &out)
	if err != nil {
		return connectors.Result{}, err
	}
	res.IdempotencyKey = inv.IdempotencyKey
	res.CompletedAt = d.settings.Now().UTC()
	res.Detail = fmt.Sprintf("已在 %s#%d 留下评论（id=%d）", args.Repo, args.Number, out.ID)
	return res, nil
}
