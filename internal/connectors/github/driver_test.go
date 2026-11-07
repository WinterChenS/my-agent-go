package github

// C08b 的 GitHub 适配器用例：Link 头分页、写前策略门、派发后超时、限流、幂等判定。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/connectortest"
	"my-agent-go/internal/connectors/fixtures"
)

const (
	fxToken    = "github_pat_fixture_S3NT1NEL_c08b"
	fxRepo     = "acme/app"
	fxPR       = 7
	filesPath  = "/repos/acme/app/pulls/7/files"
	issuesPath = "/repos/acme/app/issues"
)

type githubEnv struct {
	api     *fixtures.API
	counter *fixtures.Counter
	env     *connectortest.Env
}

func newGitHubEnv(t *testing.T, opts fixtures.APIOptions, tune func(*connectortest.Config)) *githubEnv {
	t.Helper()
	if opts.Token == "" {
		opts.Token = fxToken
	}
	opts.Platform = fixtures.PlatformGitHub
	counter := fixtures.NewCounter(opts.ObservationDir)
	opts.Counter = counter
	api, err := fixtures.NewAPI(opts)
	if err != nil {
		t.Fatalf("启动 GitHub fixture: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	cfg := connectortest.Config{
		Kind:             connectors.KindGitHub,
		Endpoints:        connectors.Endpoints{APIBase: api.BaseURL(), Account: "acme"},
		Capabilities:     []connectors.Capability{connectors.CapabilityGitHubRead, connectors.CapabilityGitHubWrite},
		AllowedResources: []string{fxRepo},
		Credential:       []byte(fxToken),
		Build: func(def connectors.Definition) (connectors.Driver, error) {
			return New(Settings{Endpoints: def.Endpoints})
		},
	}
	if tune != nil {
		tune(&cfg)
	}
	return &githubEnv{api: api, counter: counter, env: connectortest.New(t, cfg)}
}

// TestGitHubReadReturnsTypedRecords 覆盖三种读：仓库、议题（typed DTO）与
// PR 改动文件的 Link 头分页（两次请求，第二条记录与第一条不重复）。
func TestGitHubReadReturnsTypedRecords(t *testing.T) {
	e := newGitHubEnv(t, fixtures.APIOptions{PageSize: 2}, nil)

	repoRes, err := e.env.Invoke(t, "read_repo", map[string]any{"repo": fxRepo})
	if err != nil {
		t.Fatalf("read_repo: %v", err)
	}
	var repo Repo
	if err := json.Unmarshal(repoRes.Data, &repo); err != nil {
		t.Fatalf("解析仓库: %v", err)
	}
	if repo.FullName != fxRepo || repo.DefaultBranch == "" {
		t.Fatalf("仓库 DTO 不对: %+v", repo)
	}

	issueRes, err := e.env.Invoke(t, "read_issue", map[string]any{"repo": fxRepo, "number": 1})
	if err != nil {
		t.Fatalf("read_issue: %v", err)
	}
	var issue Issue
	if err := json.Unmarshal(issueRes.Data, &issue); err != nil {
		t.Fatalf("解析议题: %v", err)
	}
	if issue.Number != 1 || issue.Title == "" {
		t.Fatalf("议题 DTO 不对: %+v", issue)
	}

	first, err := e.env.Invoke(t, "read_pr", map[string]any{"repo": fxRepo, "number": fxPR})
	if err != nil {
		t.Fatalf("read_pr: %v", err)
	}
	var page pullRequestPage
	if err := json.Unmarshal(first.Data, &page); err != nil {
		t.Fatalf("解析 PR: %v", err)
	}
	if page.PullRequest.Number != fxPR || page.PullRequest.BaseRef != "main" {
		t.Fatalf("PR DTO 不对: %+v", page.PullRequest)
	}
	if len(page.Files.Records) != 2 {
		t.Fatalf("第一页应有 2 个改动文件: %+v", page.Files)
	}
	seen := map[string]bool{}
	for _, file := range page.Files.Records {
		if strings.TrimSpace(file.Filename) == "" || strings.TrimSpace(file.Status) == "" {
			t.Fatalf("改动文件必须有稳定字段: %+v", file)
		}
		seen[file.Filename] = true
	}
	if !page.Files.HasMore || page.Files.NextCursor == "" {
		t.Fatalf("Link 头有 rel=next 时必须给出下一页游标: %+v", page.Files)
	}

	second, err := e.env.Invoke(t, "read_pr", map[string]any{
		"repo": fxRepo, "number": fxPR, "cursor": page.Files.NextCursor,
	})
	if err != nil {
		t.Fatalf("read_pr 第二页: %v", err)
	}
	var nextPage pullRequestPage
	if err := json.Unmarshal(second.Data, &nextPage); err != nil {
		t.Fatalf("解析第二页: %v", err)
	}
	if len(nextPage.Files.Records) != 2 {
		t.Fatalf("第二页应有 2 个改动文件: %+v", nextPage.Files)
	}
	for _, file := range nextPage.Files.Records {
		if seen[file.Filename] {
			t.Fatalf("第二页与第一页重复: %s", file.Filename)
		}
		seen[file.Filename] = true
	}
	if len(seen) != 4 {
		t.Fatalf("两页应正好覆盖 4 个改动文件，实际 %d", len(seen))
	}
	if got := len(e.api.RequestsFor(filesPath)); got != 2 {
		t.Fatalf("两次读只应发出 2 个 files 请求，实际 %d", got)
	}
	if snap := e.counter.Snapshot(); snap.ExternalWrites() != 0 {
		t.Fatalf("读动作不得产生写副作用: %+v", snap)
	}
}

// TestGitHubWriteIsGatedAndCounted 覆盖 CONN02（创建议题与评论）。
func TestGitHubWriteIsGatedAndCounted(t *testing.T) {
	t.Run("create_issue_denied", func(t *testing.T) {
		e := newGitHubEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) { c.Deny = true })
		res, err := e.env.Invoke(t, "create_issue", map[string]any{"repo": fxRepo, "title": "被拒绝的议题"})
		if err != nil {
			t.Fatalf("策略拒绝应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("拒绝必须未派发: %+v", res)
		}
		if got := e.counter.Snapshot().GitHubIssuesCreated; got != 0 {
			t.Fatalf("拒绝时 fixture 写计数必须为 0，实际 %d", got)
		}
		if got := len(e.api.RequestsFor(issuesPath)); got != 0 {
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
			t.Fatalf("审计含 PAT: %s", raw)
		}
	})

	t.Run("create_issue_allowed", func(t *testing.T) {
		e := newGitHubEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "create_issue", map[string]any{
			"repo": fxRepo, "title": "C08b 验收", "body": "五个适配器",
		})
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone || !res.Dispatched {
			t.Fatalf("允许后应完成: %+v", res)
		}
		if got := e.counter.Snapshot().GitHubIssuesCreated; got != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
		}
		writes := e.api.Writes()
		if len(writes) != 1 || writes[0].Target != fxRepo || writes[0].Text != "C08b 验收" {
			t.Fatalf("fixture 收到的写不对: %+v", writes)
		}
		rows := e.env.Audits(t, 0)
		if len(rows) != 1 || rows[0].ToolName != "connector_github_create_issue" {
			t.Fatalf("允许必须留带工具名的审计: %+v", rows)
		}
	})

	t.Run("comment_pr_allowed", func(t *testing.T) {
		e := newGitHubEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "comment_pr", map[string]any{
			"repo": fxRepo, "number": 1, "body": "LGTM",
		})
		if err != nil {
			t.Fatalf("允许路径: %v", err)
		}
		if res.Outcome != connectors.OutcomeDone {
			t.Fatalf("允许后应完成: %+v", res)
		}
		if got := e.counter.Snapshot().GitHubComments; got != 1 {
			t.Fatalf("允许一次必须恰好 1 次副作用，实际 %d", got)
		}
	})

	t.Run("repo_outside_scope_denied", func(t *testing.T) {
		e := newGitHubEnv(t, fixtures.APIOptions{}, nil)
		res, err := e.env.Invoke(t, "create_issue", map[string]any{"repo": "evil/other", "title": "越界"})
		if err != nil {
			t.Fatalf("越界应以结果返回: %v", err)
		}
		if res.Outcome != connectors.OutcomeFailed || res.Dispatched {
			t.Fatalf("越界必须未派发: %+v", res)
		}
		if got := e.counter.Snapshot().GitHubIssuesCreated; got != 0 {
			t.Fatalf("越界时写计数必须为 0，实际 %d", got)
		}
	})
}

// TestGitHubWriteTimeoutIsOutcomeUnknown 覆盖 CONN06。
func TestGitHubWriteTimeoutIsOutcomeUnknown(t *testing.T) {
	e := newGitHubEnv(t, fixtures.APIOptions{StallAfterApply: true}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	res, err := e.env.InvokeCtx(t, ctx, "create_issue",
		map[string]any{"repo": fxRepo, "title": "会不会建两个"}, "")
	if err != nil {
		t.Fatalf("派发后超时不应是协议错误: %v", err)
	}
	if res.Outcome != connectors.OutcomeUnknown {
		t.Fatalf("派发后超时必须返回 OutcomeUnknown，实际 %+v", res)
	}
	if res.SafeToRetry {
		t.Fatal("未知结果不得声明可安全重试")
	}
	if got := e.counter.Snapshot().GitHubIssuesCreated; got != 1 {
		t.Fatalf("平台已创建一次，实际 %d", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := e.counter.Snapshot().GitHubIssuesCreated; got != 1 {
		t.Fatalf("非幂等动作不得自动重放，写计数变成 %d", got)
	}
	if got := len(e.api.RequestsFor(issuesPath)); got != 1 {
		t.Fatalf("写请求只应发出一次，实际 %d", got)
	}
	rows := e.env.Audits(t, 0)
	if len(rows) != 1 || rows[0].Outcome != string(connectors.OutcomeUnknown) {
		t.Fatalf("未知结果必须留审计: %+v", rows)
	}
}

// TestGitHubRateLimitIsEnforcedAndVisible 覆盖本地限流。
func TestGitHubRateLimitIsEnforcedAndVisible(t *testing.T) {
	e := newGitHubEnv(t, fixtures.APIOptions{}, func(c *connectortest.Config) {
		c.RateLimit = connectors.RateLimit{Requests: 2, PerSeconds: 60}
	})
	args := map[string]any{"repo": fxRepo, "number": 1, "body": "限流用例"}
	for i := 0; i < 2; i++ {
		if res, err := e.env.Invoke(t, "comment_pr", args); err != nil || res.Outcome != connectors.OutcomeDone {
			t.Fatalf("第 %d 次调用应该允许: %+v %v", i+1, res, err)
		}
	}
	before := len(e.api.Requests())
	approvalsBefore := e.env.Approvals()

	res, err := e.env.Invoke(t, "comment_pr", args)
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

// TestGitHubIdempotencyDecisionIsExplicit 覆盖幂等判定。
func TestGitHubIdempotencyDecisionIsExplicit(t *testing.T) {
	for _, id := range []string{"create_issue", "comment_pr"} {
		action, ok := connectors.LookupAction(connectors.KindGitHub, id)
		if !ok {
			t.Fatalf("%s 必须在 catalog 里", id)
		}
		if action.Idempotency != connectors.IdempotencyNone {
			t.Fatalf("GitHub REST 不接受幂等键，%s 必须声明 none: %+v", id, action)
		}
		if action.Replayable("same-key") {
			t.Fatalf("%s 带任何键都不应被判定为可重放", id)
		}
	}

	e := newGitHubEnv(t, fixtures.APIOptions{}, nil)
	args := map[string]any{"repo": fxRepo, "number": 1, "body": "重试用例"}
	for i := 0; i < 2; i++ {
		if _, err := e.env.InvokeWithKey(t, "comment_pr", args, "same-key"); err != nil {
			t.Fatalf("第 %d 次调用: %v", i+1, err)
		}
	}
	if got := e.env.Approvals(); got != 2 {
		t.Fatalf("非幂等重试必须各自重新授权（两次审批），实际 %d", got)
	}
	if got := e.counter.Snapshot().GitHubComments; got != 2 {
		t.Fatalf("相同幂等键不能让 GitHub 去重，实际 %d", got)
	}
}

// TestGitHubHealthNeverGuessesConnected 覆盖 CONN01 的驱动侧。
func TestGitHubHealthNeverGuessesConnected(t *testing.T) {
	e := newGitHubEnv(t, fixtures.APIOptions{}, nil)
	ctx := context.Background()

	health, err := e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status != connectors.HealthConnected {
		t.Fatalf("有效 PAT 应 connected: %+v", health)
	}

	if err := e.env.Creds.Put(ctx, connectortest.DefaultCredentialRef, []byte("github_pat_revoked")); err != nil {
		t.Fatalf("写错凭证: %v", err)
	}
	health, err = e.env.Svc.Health(ctx, e.env.DefinitionID)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Status == connectors.HealthConnected {
		t.Fatalf("失效 PAT 不得报告 connected: %+v", health)
	}
	if health.Remediation != connectors.RemediationAddCredentials {
		t.Fatalf("认证失败应提示补凭证: %+v", health)
	}
}
