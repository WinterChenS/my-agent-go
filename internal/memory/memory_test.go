package memory

// C05 的记忆存储测试：真实 SQLite（临时目录）+ 真实 FTS5 trigram。
//
// 这里刻意不使用内存数据库或 mock：DB02 的口径是「锁定驱动真的支持 trigram」，
// 用替身测出来的绿不能说明产品路径可用。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

func openService(t *testing.T) *Service {
	t.Helper()
	svc, err := Open(context.Background(), filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return svc
}

func requireCode(t *testing.T, err error, code string) *contracts.AppError {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误 %s，实际 nil", code)
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望 AppError(%s)，实际 %T: %v", code, err, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
	return appErr
}

func save(t *testing.T, svc *Service, d MemoryDraft) Memory {
	t.Helper()
	m, err := svc.Save(context.Background(), d)
	if err != nil {
		t.Fatalf("Save(%q): %v", d.Content, err)
	}
	return m
}

// TestSaveConfirmDeleteLifecycle：显式保存即 active；候选确认后 active；
// 删除是软删除，列表里不再出现，但历史记录不被物理抹掉（审计可追溯）。
func TestSaveConfirmDeleteLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	saved := save(t, svc, MemoryDraft{
		Scope:           ScopeWorkspace,
		WorkspaceID:     "ws-a",
		Kind:            KindConvention,
		Content:         "数据库连接池上限为二十",
		SourceSessionID: "sess-1",
	})
	if saved.Status != StatusActive {
		t.Fatalf("显式保存应直接 active，实际 %s", saved.Status)
	}
	if saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatalf("时间戳缺失: %+v", saved)
	}

	cand := save(t, svc, MemoryDraft{
		Scope:           ScopeWorkspace,
		WorkspaceID:     "ws-a",
		Kind:            KindNote,
		Content:         "数据库连接池候选条目：上限为九十九",
		SourceSessionID: "sess-1",
		Status:          StatusCandidate,
	})
	if cand.Status != StatusCandidate {
		t.Fatalf("候选条目状态应为 candidate，实际 %s", cand.Status)
	}
	if err := svc.Confirm(ctx, cand.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	got, err := svc.Get(ctx, cand.ID)
	if err != nil {
		t.Fatalf("Get after confirm: %v", err)
	}
	if got.Status != StatusActive {
		t.Fatalf("确认后应为 active，实际 %s", got.Status)
	}

	if err := svc.Delete(ctx, cand.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, cand.ID); err == nil {
		t.Fatal("已删除的记忆不应还能被 Get")
	} else {
		requireCode(t, err, contracts.CodeNotFound)
	}
	items, err := svc.List(ctx, "ws-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, it := range items {
		if it.ID == cand.ID {
			t.Fatalf("删除后 List 仍然返回 %s", cand.ID)
		}
	}
	// 删除是软删除：审计记录还在，只是不再参与检索/注入。
	var status string
	if err := svc.DB().SQL().QueryRowContext(ctx,
		`SELECT status FROM memories WHERE id = ?`, cand.ID).Scan(&status); err != nil {
		t.Fatalf("读取被删记忆: %v", err)
	}
	if status != string(StatusDeleted) {
		t.Fatalf("删除应写 status=deleted，实际 %s", status)
	}
}

func TestSaveValidation(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	cases := []struct {
		name  string
		draft MemoryDraft
		code  string
	}{
		{"空内容", MemoryDraft{Scope: ScopeWorkspace, WorkspaceID: "ws-a", Content: "   "}, contracts.CodeUnsupported},
		{"未知 scope", MemoryDraft{Scope: "global", Content: "随便"}, contracts.CodeUnsupported},
		{"workspace 缺 workspaceId", MemoryDraft{Scope: ScopeWorkspace, Content: "数据库连接池上限为二十"}, contracts.CodeUnsupported},
		{"未知 kind", MemoryDraft{Scope: ScopeWorkspace, WorkspaceID: "ws-a", Kind: "diary", Content: "数据库连接池上限为二十"}, contracts.CodeUnsupported},
		{"直接写 deleted", MemoryDraft{Scope: ScopeWorkspace, WorkspaceID: "ws-a", Content: "数据库连接池上限为二十", Status: StatusDeleted}, contracts.CodeUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Save(ctx, c.draft)
			requireCode(t, err, c.code)
		})
	}

	// user 记忆不绑定 workspace：即使调用方传了 workspaceId 也归一成空，
	// 否则 A 项目存的偏好不会在 B 项目出现（MEM02）。
	userMem := save(t, svc, MemoryDraft{
		Scope:       ScopeUser,
		WorkspaceID: "ws-a",
		Content:     "用户偏好：回答先给结论",
	})
	if userMem.WorkspaceID != "" {
		t.Fatalf("user 记忆的 workspaceId 应为空，实际 %q", userMem.WorkspaceID)
	}
}

// TestSecretLikeContentRejected 覆盖"不把整段工具日志/密钥当记忆"：
// 形如凭证的 fixture 必须被拒绝，且错误信息不能回显密钥本体。
func TestSecretLikeContentRejected(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	cases := []struct {
		name    string
		content string
		secret  string
		reason  string
	}{
		{
			"OpenAI 风格 key",
			"调试日志：key=" + openAIStyleFakeKey(),
			openAIStyleFakeKey(),
			"OpenAI",
		},
		{
			"GitHub token",
			"tool log: ghp_fakefakefake1234567890",
			"ghp_fakefakefake1234567890",
			"GitHub",
		},
		{
			"私钥块",
			"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmU=",
			"b3BlbnNzaC1rZXktdjEAAAAABG5vbmU=",
			"私钥",
		},
		{
			"Bearer 头",
			"Authorization: Bearer fake-bearer-token-0123456789",
			"fake-bearer-token-0123456789",
			"Authorization",
		},
		{
			"AWS access key",
			"配置：aws_access_key_id = AKIAFAKEFAKEFAKEFAKE",
			"AKIAFAKEFAKEFAKEFAKE",
			"AWS",
		},
		{
			"泛化的 key=value",
			"工具日志：api_key = fake-api-key-value-0123456789",
			"fake-api-key-value-0123456789",
			"key=value",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Save(ctx, MemoryDraft{
				Scope:       ScopeWorkspace,
				WorkspaceID: "ws-a",
				Content:     c.content,
			})
			appErr := requireCode(t, err, contracts.CodeUnsupported)
			if !strings.Contains(appErr.Message, "疑似凭证") {
				t.Fatalf("拒绝原因应说明是疑似凭证，实际 %q", appErr.Message)
			}
			if !strings.Contains(appErr.Message, c.reason) {
				t.Fatalf("拒绝原因应包含 %q，实际 %q", c.reason, appErr.Message)
			}
			if strings.Contains(appErr.Message, c.secret) {
				t.Fatalf("错误信息回显了密钥内容：%q", appErr.Message)
			}
		})
	}

	// 误伤检查：普通的约定/偏好文本必须能保存（不能靠"看起来像秘密"一刀切）。
	saved := save(t, svc, MemoryDraft{
		Scope:       ScopeWorkspace,
		WorkspaceID: "ws-a",
		Content:     "数据库密码规则：至少十二位，且不写进代码",
	})
	if saved.ID == "" {
		t.Fatal("普通文本应能保存")
	}
}

// openAIStyleFakeKey 在运行时拼接一个明显是假的、但形状正确的 key。
//
// 不在源码里写完整字面量：编辑/日志链路会把形如真实密钥的字符串当成秘密处理，
// 那样 fixture 本身就会在到达测试前变形。
func openAIStyleFakeKey() string {
	return "sk" + "-" + "fixtureonly" + "0123456789abcdef"
}

// TestNoImplicitModelOrNetworkCall 覆盖 MEM03 的"默认无后台抽取模型调用"：
// 保存/检索/构建上下文期间不允许发出任何 HTTP 请求。
func TestNoImplicitModelOrNetworkCall(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })
	var calls int
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("测试期不允许网络调用")
	})

	saved := save(t, svc, MemoryDraft{
		Scope:   ScopeUser,
		Content: "用户偏好：回答先给结论",
	})
	if _, err := svc.Search(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "回答先给结论"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, err := svc.BuildContext(ctx, MemoryQuery{WorkspaceID: "ws-a", Text: "回答先给结论"}); err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	if err := svc.Confirm(ctx, saved.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if calls != 0 {
		t.Fatalf("记忆路径发起了 %d 次 HTTP 调用；默认不应有后台模型/网络调用", calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestMigrationRerun：迁移与探测可重复执行，不重复记录、不报错。
func TestMigrationRerun(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	svc, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer svc.Close()

	before, err := svc.DB().SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := svc.DB().Migrate(ctx); err != nil {
			t.Fatalf("第 %d 次重跑迁移: %v", i+1, err)
		}
	}
	after, err := svc.DB().SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if after != before {
		t.Fatalf("迁移重跑改变了 schema 版本: %d → %d", before, after)
	}
	var rows int
	if err := svc.DB().SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil {
		t.Fatalf("统计迁移记录: %v", err)
	}
	migrations, err := storage.LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if rows != len(migrations) {
		t.Fatalf("迁移记录数应为 %d，实际 %d", len(migrations), rows)
	}

	// 探测本身也必须能重复执行（启动时会做一次）。
	if err := svc.VerifyTrigram(ctx); err != nil {
		t.Fatalf("重复探测 trigram: %v", err)
	}
}

// TestExportIsReadableJSON：导出是可读 JSON，且不含被删除条目。
func TestExportIsReadableJSON(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	keep := save(t, svc, MemoryDraft{Scope: ScopeWorkspace, WorkspaceID: "ws-a", Content: "数据库连接池上限为二十"})
	drop := save(t, svc, MemoryDraft{Scope: ScopeWorkspace, WorkspaceID: "ws-a", Content: "数据库连接池上限为五"})
	if err := svc.Delete(ctx, drop.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	doc, err := svc.Export(ctx, "ws-a")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !utf8.Valid(raw) {
		t.Fatal("导出 JSON 不是合法 UTF-8")
	}
	var parsed ExportDocument
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("导出不是合法 JSON: %v", err)
	}
	if parsed.SchemaVersion != 1 {
		t.Fatalf("导出 schemaVersion 应为 1，实际 %d", parsed.SchemaVersion)
	}
	if len(parsed.Items) != 1 || parsed.Items[0].ID != keep.ID {
		t.Fatalf("导出应只含 1 条未删除记忆，实际 %+v", parsed.Items)
	}
	if !strings.Contains(string(raw), "数据库连接池上限为二十") {
		t.Fatal("导出应包含中文原文")
	}
}

// 时间解析兼容旧行（SQLite datetime('now')）与产品写入（RFC3339）。
func TestParseStoredTime(t *testing.T) {
	for _, s := range []string{
		"2026-11-04T10:00:00Z",
		"2026-11-04 10:00:00",
		"",
	} {
		if _, err := parseStoredTime(s); err != nil {
			t.Fatalf("parseStoredTime(%q): %v", s, err)
		}
	}
	if got := formatTime(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)); got != "2026-11-04T10:00:00Z" {
		t.Fatalf("写入时间格式应为 RFC3339 UTC，实际 %q", got)
	}
}

// TestTrigramProbeReportsRealFacts 把 DB02 的探测事实打印出来（证据用）：
// SQLite 版本、FTS5 编译选项、memory_fts 的真实定义、以及一次中文子串命中。
func TestTrigramProbeReportsRealFacts(t *testing.T) {
	ctx := context.Background()
	svc := openService(t)

	var version string
	if err := svc.DB().SQL().QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		t.Fatalf("sqlite_version: %v", err)
	}
	var fts5 int
	if err := svc.DB().SQL().QueryRowContext(ctx,
		`SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err != nil {
		t.Fatalf("编译选项: %v", err)
	}
	var schema string
	if err := svc.DB().SQL().QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='memory_fts'`).Scan(&schema); err != nil {
		t.Fatalf("memory_fts 定义: %v", err)
	}
	if !strings.Contains(strings.ToLower(schema), "trigram") {
		t.Fatalf("memory_fts 不是 trigram 分词器: %s", schema)
	}
	if err := svc.VerifyTrigram(ctx); err != nil {
		t.Fatalf("VerifyTrigram: %v", err)
	}

	save(t, svc, MemoryDraft{Scope: ScopeUser, Content: "数据库连接池上限为二十"})
	var hits int
	if err := svc.DB().SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_fts WHERE memory_fts MATCH '连接池上'`).Scan(&hits); err != nil {
		t.Fatalf("真实中文 trigram 查询: %v", err)
	}
	if hits != 1 {
		t.Fatalf("memory_fts 中文子串应命中 1 条，实际 %d", hits)
	}
	t.Logf("sqlite=%s ENABLE_FTS5=%d", version, fts5)
	t.Logf("memory_fts DDL=%s", strings.Join(strings.Fields(schema), " "))
	t.Logf("MATCH '连接池上' hits=%d", hits)
}
