package diagnostics

// C07 诊断域测试。
//
// 守两件事（ART03 / SEC07.diagnostics）：
//  1. usage 的三态：没有 usage、有 tokens 没价格、两者都有。
//     前两种的结论必须落在 JSON 形状上——不是"Go 里是 nil"，而是
//     "写出来的 JSON 里根本没有 costUsd/totalTokens 这个键"。
//     写成 0 等于用确定的语气说一件不知道的事。
//  2. 诊断包脱敏：把 sentinel 塞进 key、Authorization 头、URL query、
//     连接串与错误堆栈，导出文件里命中必须为 0；同时错误码、版本、时间、
//     事件 ID 必须还在——脱敏不能把"故障可定位"一起脱掉。
//
// 断言对象是**写出来的文件字节**，不是内存里的结构：结构写对了但序列化时
// 漏了脱敏，是这个功能最典型的事故形态。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/storage"
)

const testVersion = "0.1.0-c07-test"

type testEnv struct {
	svc  *Service
	rec  *Recorder
	db   *storage.DB
	root string
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移临时库: %v", err)
	}
	now := func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) }
	svc, err := New(db, Options{
		Root:    filepath.Join(root, "diagnostics"),
		Version: testVersion,
		Now:     now,
	})
	if err != nil {
		t.Fatalf("构造诊断服务: %v", err)
	}
	rec, err := NewRecorder(db)
	if err != nil {
		t.Fatalf("构造记录器: %v", err)
	}
	rec.SetClock(now)
	return testEnv{svc: svc, rec: rec, db: db, root: root}
}

// seedRow 是一行 run_usage 的测试夹具。
//
// 数字用 nil 表示"不知道"（落库为 NULL），与记录器在 unavailable 时写的形状一致。
type seedRow struct {
	RunID        string
	SessionID    string
	Availability string
	Input        *int64
	Output       *int64
	Total        *int64
	Cost         *float64
	PriceSource  string
	Estimated    bool
	UpdatedAt    string
}

func seedRunUsage(t *testing.T, db *storage.DB, row seedRow) {
	t.Helper()
	var input, output, total, cost any
	if row.Input != nil {
		input = *row.Input
	}
	if row.Output != nil {
		output = *row.Output
	}
	if row.Total != nil {
		total = *row.Total
	}
	if row.Cost != nil {
		cost = *row.Cost
	}
	estimated := 0
	if row.Estimated {
		estimated = 1
	}
	ts := row.UpdatedAt
	if ts == "" {
		ts = "2026-11-05T10:00:00.000000000Z"
	}
	if _, err := db.SQL().ExecContext(context.Background(), `
		INSERT INTO run_usage(run_id, session_id, workspace_id, state, usage_availability,
		    input_tokens, output_tokens, total_tokens, cost_usd, price_source, estimated,
		    started_at, updated_at)
		VALUES(?, ?, 'ws-1', 'completed', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.RunID, row.SessionID, row.Availability,
		input, output, total, cost, row.PriceSource, estimated, ts, ts); err != nil {
		t.Fatalf("写入 run_usage: %v", err)
	}
}

func ptrInt(v int64) *int64       { return &v }
func ptrFloat(v float64) *float64 { return &v }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(raw)
}

// TestUsageUnavailableIsNotZero 覆盖 UsageSummary 的三态。
func TestUsageUnavailableIsNotZero(t *testing.T) {
	ctx := context.Background()

	t.Run("no-usage-recorded", func(t *testing.T) {
		env := newTestEnv(t)
		sum, err := env.svc.UsageSummary(ctx, UsageRange{})
		if err != nil {
			t.Fatalf("UsageSummary: %v", err)
		}
		if sum.Availability != contracts.UsageUnavailable {
			t.Fatalf("没有任何 usage 时应为 unavailable，实际 %q", sum.Availability)
		}
		raw := mustJSON(t, sum)
		if strings.Contains(raw, "totalTokens") || strings.Contains(raw, "inputTokens") {
			t.Fatalf("没有 usage 时不应出现 token 字段：%s", raw)
		}
		if strings.Contains(raw, "costUsd") {
			t.Fatalf("没有 usage 时不应出现 costUsd：%s", raw)
		}
		observeDiagnostics(t, env.db.Path(), env.svc.Root(), nil, map[string]any{"usageCase": "no-usage-recorded"})
	})

	t.Run("usage-recorded-but-unavailable", func(t *testing.T) {
		env := newTestEnv(t)
		// run 跑过，但 provider 没报用量：availability=unavailable、数字是 NULL。
		seedRunUsage(t, env.db, seedRow{RunID: "run-1", SessionID: "sess-1",
			Availability: contracts.UsageUnavailable})

		sum, err := env.svc.UsageSummary(ctx, UsageRange{})
		if err != nil {
			t.Fatalf("UsageSummary: %v", err)
		}
		if sum.Runs != 1 || sum.ReportedRuns != 0 || sum.UnreportedRuns != 1 {
			t.Fatalf("计数不符：%+v", sum)
		}
		if sum.Availability != contracts.UsageUnavailable {
			t.Fatalf("provider 没报用量时应为 unavailable，实际 %q", sum.Availability)
		}
		raw := mustJSON(t, sum)
		if strings.Contains(raw, "totalTokens") || strings.Contains(raw, "costUsd") {
			t.Fatalf("没报用量时不应出现 token/cost 字段：%s", raw)
		}
		observeDiagnostics(t, env.db.Path(), env.svc.Root(), nil, map[string]any{"usageCase": "usage-recorded-but-unavailable"})
	})

	t.Run("tokens-without-price", func(t *testing.T) {
		env := newTestEnv(t)
		seedRunUsage(t, env.db, seedRow{
			RunID: "run-1", SessionID: "sess-1", Availability: contracts.UsageReported,
			Input: ptrInt(120), Output: ptrInt(34), Total: ptrInt(154),
		})

		sum, err := env.svc.UsageSummary(ctx, UsageRange{})
		if err != nil {
			t.Fatalf("UsageSummary: %v", err)
		}
		if sum.Availability != contracts.UsageReported {
			t.Fatalf("有 tokens 时整体应为 reported，实际 %q", sum.Availability)
		}
		raw := mustJSON(t, sum)
		if !strings.Contains(raw, `"totalTokens":154`) {
			t.Fatalf("tokens 必须照报：%s", raw)
		}
		if strings.Contains(raw, "costUsd") {
			t.Fatalf("没有价格时不应出现 costUsd：%s", raw)
		}
		if sum.Cost.Availability != contracts.UsageUnavailable {
			t.Fatalf("没有价格时 cost 应为 unavailable，实际 %q", sum.Cost.Availability)
		}
		if sum.Cost.UnknownRuns != 1 {
			t.Fatalf("应记录 1 个 run 的价格未知，实际 %d", sum.Cost.UnknownRuns)
		}
		observeDiagnostics(t, env.db.Path(), env.svc.Root(), nil, map[string]any{"usageCase": "tokens-without-price"})
	})

	t.Run("tokens-and-price", func(t *testing.T) {
		env := newTestEnv(t)
		seedRunUsage(t, env.db, seedRow{
			RunID: "run-1", SessionID: "sess-1", Availability: contracts.UsageReported,
			Input: ptrInt(120), Output: ptrInt(34), Total: ptrInt(154),
			Cost: ptrFloat(0.0123), PriceSource: "provider",
		})

		sum, err := env.svc.UsageSummary(ctx, UsageRange{})
		if err != nil {
			t.Fatalf("UsageSummary: %v", err)
		}
		raw := mustJSON(t, sum)
		if !strings.Contains(raw, `"costUsd":0.0123`) {
			t.Fatalf("有价格时必须出现 costUsd：%s", raw)
		}
		if !strings.Contains(raw, `"priceSource":"provider"`) {
			t.Fatalf("必须写明价格来源：%s", raw)
		}
		if sum.Cost.Availability != contracts.UsageReported || sum.Cost.USD == nil {
			t.Fatalf("cost 应可用：%+v", sum.Cost)
		}
		observeDiagnostics(t, env.db.Path(), env.svc.Root(), nil, map[string]any{"usageCase": "tokens-and-price"})
	})

	t.Run("mixed-unknown-cost-is-not-summed", func(t *testing.T) {
		env := newTestEnv(t)
		seedRunUsage(t, env.db, seedRow{
			RunID: "run-1", SessionID: "sess-1", Availability: contracts.UsageReported,
			Input: ptrInt(10), Output: ptrInt(2), Total: ptrInt(12),
			Cost: ptrFloat(0.25), PriceSource: "provider",
		})
		// 第二个 run 只有 tokens，没有价格：合计费用不是 0.25，而是"不可知"。
		seedRunUsage(t, env.db, seedRow{
			RunID: "run-2", SessionID: "sess-1", Availability: contracts.UsageReported,
			Input: ptrInt(20), Output: ptrInt(4), Total: ptrInt(24),
		})
		sum, err := env.svc.UsageSummary(ctx, UsageRange{})
		if err != nil {
			t.Fatalf("UsageSummary: %v", err)
		}
		if sum.Tokens.TotalTokens == nil || *sum.Tokens.TotalTokens != 36 {
			t.Fatalf("tokens 应合计 36，实际 %+v", sum.Tokens.TotalTokens)
		}
		if sum.Cost.Availability != contracts.UsageUnavailable || sum.Cost.USD != nil {
			t.Fatalf("有 run 缺价格时合计费用必须 unavailable（不能只加已知的一笔）：%+v", sum.Cost)
		}
		observeDiagnostics(t, env.db.Path(), env.svc.Root(), nil, map[string]any{"usageCase": "mixed-unknown-cost-is-not-summed"})
	})
}

// TestDiagnosticExportRedactsSentinels 覆盖 SEC07.diagnostics。
func TestDiagnosticExportRedactsSentinels(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// 每个 sentinel 都对应一条真实会出现在诊断包里的泄露形态。
	keySentinel := "sk-live-4f9a2b7c1d8e3f6a5b0c9d2e7f1a4b8c"
	apiKeySentinel := "apikey-sentinel-9f8e7d6c5b4a"
	headerSentinel := "BasicHeaderSentinel3f2e1d0c"
	querySentinel := "querySentinel-7c6b5a4f3e2d"
	dsnSentinel := "dsnSentinel-1a2b3c4d5e6f"

	stackTrace := "goroutine 1 [running]:\n" +
		"main.fetch(0xc000010000)\n" +
		"\tC:\\Users\\alice\\app\\main.go:42 +0x1d\n" +
		"upstream: postgres://anvil:" + dsnSentinel + "@db.local:5432/app"
	errorMessage := "upstream 401: Authorization: " + headerSentinel +
		"; GET https://api.test/v1/models?key=" + querySentinel

	// 1) run 行：错误信息里带 Authorization 头、URL query 与错误堆栈。
	seedRunUsage(t, env.db, seedRow{
		RunID: "run-1", SessionID: "sess-1", Availability: contracts.UsageReported,
		Input: ptrInt(10), Output: ptrInt(20), Total: ptrInt(30),
		Cost: ptrFloat(0.5), PriceSource: "provider",
	})
	if _, err := env.db.SQL().ExecContext(ctx,
		`UPDATE run_usage SET state='failed', error_code='PROVIDER_ERROR', error_message=? WHERE run_id='run-1'`,
		errorMessage+"\n"+stackTrace); err != nil {
		t.Fatalf("写入错误结论: %v", err)
	}

	// 2) 事件行：载荷里有 key 形态的秘密（"api_key": "..."），事件 ID/时间必须保留。
	payload := `{"type":"diagnostic","api_key":"` + apiKeySentinel + `","detail":"sk="` + keySentinel + `"}`
	occurred := "2026-11-05T10:00:00.000000000Z"
	if _, err := env.db.SQL().ExecContext(ctx, `
		INSERT INTO run_events(run_id, seq, event_id, session_id, type, occurred_at, payload)
		VALUES('run-1', 7, 'run-1-7', 'sess-1', 'diagnostic', ?, ?)`,
		occurred, payload); err != nil {
		t.Fatalf("写入事件日志: %v", err)
	}

	// 2b) 事件行：JSON 形态的 header（键值之间隔着引号），纯文本规则漏掉的正是它。
	bearerSentinel := "eyJhbGciOiJIUzI1NiJ9.bearerSentinelPayload.c2lnbmF0dXJl"
	if _, err := env.db.SQL().ExecContext(ctx, `
		INSERT INTO run_events(run_id, seq, event_id, session_id, type, occurred_at, payload)
		VALUES('run-1', 8, 'run-1-8', 'sess-1', 'tool.start', ?, ?)`,
		occurred, `{"toolName":"fetch","headers":{"authorization":"Bearer `+bearerSentinel+`"}}`); err != nil {
		t.Fatalf("写入 JSON 形态 header 事件: %v", err)
	}

	// 3) 产物行：展示名与路径里带秘密/用户名（诊断包会引用产物索引）。
	if _, err := env.db.SQL().ExecContext(ctx, `
		INSERT INTO artifacts(id, workspace_id, run_id, display_name, canonical_path, path_key,
		    sha256, size_bytes, media_type, created_at, updated_at)
		VALUES('art-1', 'ws-1', 'run-1', ?, ?, ?, 'deadbeef', 12, 'text/plain', ?, ?)`,
		"report-"+keySentinel+".txt",
		`C:\Users\alice\workspace\report.txt`, `c:\users\alice\workspace\report.txt`,
		occurred, occurred); err != nil {
		t.Fatalf("写入产物索引: %v", err)
	}

	res, err := env.svc.ExportDiagnostics(ctx, ExportRequest{RunIDs: []string{"run-1"}})
	if err != nil {
		t.Fatalf("ExportDiagnostics: %v", err)
	}
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("读导出文件: %v", err)
	}
	text := string(raw)

	// 断言对象是写出来的字节：命中必须为 0。
	for _, s := range []string{keySentinel, apiKeySentinel, headerSentinel, querySentinel, dsnSentinel} {
		if strings.Contains(text, s) {
			t.Fatalf("诊断包里出现了 sentinel %q（脱敏没有覆盖到导出）:\n%s", s, text)
		}
	}
	// 用户名也是个人数据：Windows/Unix home 路径都要被脱敏。
	if strings.Contains(text, "alice") {
		t.Fatalf("诊断包里出现了用户名：\n%s", text)
	}

	// 但定位所需的要素必须保留。
	for _, want := range []string{
		"PROVIDER_ERROR",             // 错误码
		testVersion,                  // 版本
		"run-1-7",                    // 事件 ID
		"2026-11-05T10:00:00",        // 时间
		`"kind":"anvil.diagnostics"`, // 包标识
		"art-1",                      // 产物 ID
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("诊断包缺少定位要素 %q：\n%s", want, text)
		}
	}
	// 脱敏必须仍然产出合法 JSON：坏掉的诊断包无法定位任何东西。
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("诊断包不是合法 JSON: %v\n%s", err, text)
	}
	if res.Bytes != len(raw) {
		t.Fatalf("结果里的字节数与文件不符：%d != %d", res.Bytes, len(raw))
	}
	// 脱敏不能把载荷整段删掉：事件类型与"这里原本有个 api_key"必须还在，
	// 否则"命中 0"可能只是因为那些行根本没被导出。
	if !strings.Contains(text, `"type":"diagnostic"`) {
		t.Fatalf("事件载荷被整段删掉了，脱敏退化成丢数据：\n%s", text)
	}
	if !strings.Contains(text, `api_key`) {
		t.Fatalf("事件载荷里的键名应保留（只替换值）：\n%s", text)
	}
	// 错误信息里的可定位部分要保留，只把秘密换掉。
	if !strings.Contains(text, "upstream 401") {
		t.Fatalf("错误信息被整段删掉了，故障不可定位：\n%s", text)
	}

	// 可见的脱敏命中数要能解释。
	if res.Redactions == 0 {
		t.Fatalf("导出结果里的脱敏命中数不应为 0")
	}

	// SEC07.diagnostics 的外部观察：磁盘上的诊断包重新扫描 sentinel（命中必须为 0）。
	observeDiagnostics(t, env.db.Path(), env.svc.Root(),
		[]string{keySentinel, apiKeySentinel, headerSentinel, querySentinel, dsnSentinel, "alice"},
		map[string]any{"redactions": res.Redactions, "bundleBytes": res.Bytes})
}

// TestExportDiagnosticsRefusesOutsideTarget 守导出目标只在诊断根内。
func TestExportDiagnosticsRefusesOutsideTarget(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	for _, target := range []string{
		".." + string(os.PathSeparator) + "escape.json",
		filepath.Join(env.root, "elsewhere.json"),
	} {
		if _, err := env.svc.ExportDiagnostics(ctx, ExportRequest{Target: target}); err == nil {
			t.Fatalf("导出目标 %q 应被拒绝", target)
		}
	}
	res, err := env.svc.ExportDiagnostics(ctx, ExportRequest{Target: "bundle.json"})
	if err != nil {
		t.Fatalf("合法目标应通过: %v", err)
	}
	if res.Path != filepath.Join(env.svc.Root(), "bundle.json") {
		t.Fatalf("导出路径不符：%s", res.Path)
	}

	// SEC07.diagnostics 的外部观察：导出根里到底落了哪些文件（越界目标不在其中）。
	observeDiagnostics(t, env.db.Path(), env.svc.Root(), nil, map[string]any{"refusedOutsideTarget": true})
}

// TestRecorderPersistsSnapshotAsUnavailableOrNumbers 守"落库也不写伪 0"。
func TestRecorderPersistsSnapshotAsUnavailableOrNumbers(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// 情况一：终态但没有任何 usage（provider 没报）。
	if err := env.rec.RecordRun(ctx, contracts.RunSnapshot{
		RunID: "run-none", SessionID: "sess-1", WorkspaceID: "ws-1",
		State:  contracts.RunStateCompleted,
		Usage:  &contracts.RunUsage{Availability: contracts.UsageUnavailable},
		Budget: &contracts.RunBudget{MaxToolCalls: 100, MaxDurationMs: 1800000, ToolCalls: 3},
	}); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	var (
		availability string
		input, total any
		cost         any
	)
	if err := env.db.SQL().QueryRowContext(ctx,
		`SELECT usage_availability, input_tokens, total_tokens, cost_usd FROM run_usage WHERE run_id='run-none'`).
		Scan(&availability, &input, &total, &cost); err != nil {
		t.Fatalf("读 run_usage: %v", err)
	}
	if availability != contracts.UsageUnavailable {
		t.Fatalf("availability 应为 unavailable，实际 %q", availability)
	}
	if input != nil || total != nil || cost != nil {
		t.Fatalf("unavailable 时数字必须是 NULL（不是 0）：input=%v total=%v cost=%v", input, total, cost)
	}

	// 情况二：provider 报了 tokens 但没有价格。
	if err := env.rec.RecordRun(ctx, contracts.RunSnapshot{
		RunID: "run-tokens", SessionID: "sess-1", WorkspaceID: "ws-1",
		State: contracts.RunStateCompleted,
		Usage: &contracts.RunUsage{
			Availability: contracts.UsageReported,
			InputTokens:  100, OutputTokens: 20, TotalTokens: 120,
		},
	}); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	var (
		gotTotal int64
		gotCost  any
	)
	if err := env.db.SQL().QueryRowContext(ctx,
		`SELECT total_tokens, cost_usd FROM run_usage WHERE run_id='run-tokens'`).
		Scan(&gotTotal, &gotCost); err != nil {
		t.Fatalf("读 run_usage: %v", err)
	}
	if gotTotal != 120 {
		t.Fatalf("tokens 应照报：%d", gotTotal)
	}
	if gotCost != nil {
		t.Fatalf("没有价格时 cost_usd 必须是 NULL（不是 0）：%v", gotCost)
	}

	// 情况三：provider 报告了真实 0 费用——有值、值为 0，不能被当成"没有价格"。
	zero := 0.0
	if err := env.rec.RecordRun(ctx, contracts.RunSnapshot{
		RunID: "run-free", SessionID: "sess-1", WorkspaceID: "ws-1",
		State: contracts.RunStateCompleted,
		Usage: &contracts.RunUsage{
			Availability: contracts.UsageReported,
			InputTokens:  5, OutputTokens: 1, TotalTokens: 6,
			CostUSD: &zero, PriceSource: "provider",
		},
	}); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	var freeCost float64
	if err := env.db.SQL().QueryRowContext(ctx,
		`SELECT cost_usd FROM run_usage WHERE run_id='run-free'`).Scan(&freeCost); err != nil {
		t.Fatalf("读 run_free: %v", err)
	}
	if freeCost != 0 {
		t.Fatalf("provider 报告的 0 费用应保留为 0：%v", freeCost)
	}

	// 同一 run 的重复记录只更新行，不产生副本（run_id 是主键）。
	if err := env.rec.RecordRun(ctx, contracts.RunSnapshot{
		RunID: "run-tokens", SessionID: "sess-1", WorkspaceID: "ws-1",
		State: contracts.RunStateFailed,
		Error: &contracts.AppError{Code: contracts.CodeProviderError, Message: "upstream 500"},
		Usage: &contracts.RunUsage{Availability: contracts.UsageUnavailable},
	}); err != nil {
		t.Fatalf("RecordRun(更新): %v", err)
	}
	var (
		rows    int
		state   string
		errCode string
	)
	if err := env.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_usage WHERE run_id='run-tokens'`).Scan(&rows); err != nil {
		t.Fatalf("统计: %v", err)
	}
	if rows != 1 {
		t.Fatalf("同一 run 应只有一行，实际 %d", rows)
	}
	if err := env.db.SQL().QueryRowContext(ctx,
		`SELECT state, error_code FROM run_usage WHERE run_id='run-tokens'`).Scan(&state, &errCode); err != nil {
		t.Fatalf("读更新后的行: %v", err)
	}
	if state != contracts.RunStateFailed || errCode != contracts.CodeProviderError {
		t.Fatalf("更新后应反映最新状态与错误码：%s / %s", state, errCode)
	}
}

// TestRunDetailsMarksUnknown 守"不知道就说不知道"：没有记录的部分必须显式列出，
// 而不是返回一个看起来完整的空结构。
func TestRunDetailsMarksUnknown(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	details, err := env.svc.RunDetails(ctx, "run-unknown")
	if err != nil {
		t.Fatalf("RunDetails: %v", err)
	}
	if details.Record != nil {
		t.Fatalf("没有索引行时 Record 必须为空，而不是零值结构：%+v", details.Record)
	}
	if len(details.Unavailable) == 0 {
		t.Fatalf("不知道的段落必须显式列为 unavailable")
	}
	raw := mustJSON(t, details)
	if !strings.Contains(raw, `"unavailable"`) {
		t.Fatalf("JSON 里必须能看到 unavailable 段落：%s", raw)
	}

	// ART03 的外部观察：库里没有任何 usage 行（第二个句柄读回）。
	observeDiagnostics(t, env.db.Path(), env.svc.Root(), nil, map[string]any{"runDetailsUnknown": true})
}

// TestRecorderKeepsLocatorsInOversizedPayloads 守"超限载荷也要保留定位字段"。
//
// 工具事件的 args/text 很容易把载荷撑过上限。只留一个"被截断"标记会让 run 详情
// 变成一份没有工具名、没有错误码的空壳——诊断价值归零。这里钉住：截断后的载荷
// 仍然是合法 JSON、仍然带 toolName/toolCallId，并且不含原始 args 里的 sentinel。
func TestRecorderKeepsLocatorsInOversizedPayloads(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	sentinel := "oversizedPayloadSentinel0001"
	big := strings.Repeat("x", maxEventPayloadBytes+1024)
	payload := []byte(`{"toolName":"read_file","toolCallId":"call-9","args":{"path":"` + sentinel + `","body":"` + big + `"}}`)
	env.rec.RecordEvent(contracts.Event{
		EventID: "run-2-5", RunID: "run-2", SessionID: "sess-1", Seq: 5,
		Type: "tool.start", OccurredAt: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC),
		Payload: payload,
	})

	var (
		stored    string
		truncated int
	)
	if err := env.db.SQL().QueryRowContext(ctx,
		`SELECT payload, payload_truncated FROM run_events WHERE run_id='run-2' AND seq=5`).
		Scan(&stored, &truncated); err != nil {
		t.Fatalf("读事件行: %v", err)
	}
	if truncated != 1 {
		t.Fatalf("超限载荷应标记截断，实际 %d", truncated)
	}
	if !json.Valid([]byte(stored)) {
		t.Fatalf("截断后的载荷必须是合法 JSON：%s", stored)
	}
	if !strings.Contains(stored, "read_file") || !strings.Contains(stored, "call-9") {
		t.Fatalf("截断后的载荷必须保留工具定位字段：%s", stored)
	}
	if strings.Contains(stored, sentinel) || strings.Contains(stored, big) {
		t.Fatalf("截断后的载荷不应保留原始 args：%s", stored)
	}

	// ART03 的外部观察：库里那一行的截断标记与定位字段（第二个句柄读回）。
	observeDiagnostics(t, env.db.Path(), env.svc.Root(), []string{sentinel}, map[string]any{"payloadTruncated": true})
}
