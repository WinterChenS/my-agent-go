package session

// C06e 会话导出测试。全部在 t.TempDir() 里跑：临时 appData、临时 agentDir、
// 临时导出根，不碰真实配置、真实 transcript、真实用户名下目录。
//
// 这里守的是导出的四条硬事实：
//   - 内容来自 Pi transcript 原文（不是内存里重建的对话），工具事件与结果不丢；
//   - HTML 里标记只能是文本，导出的标签集合是固定模板（白名单），没有脚本落点；
//   - 秘密按产品既有规则脱敏，sentinel 命中 0，同时保留事件 ID/工具名可定位；
//   - 目标路径越界/链接一律拒绝，写盘是"临时文件 + rename"，不留半份与临时残留。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/history"
	"my-agent-go/internal/storage"
)

const exportSessionID = "sess-export-c06e"

// exportSentinel 是只应出现在 transcript 里的秘密；各条规则都能命中它。
const exportSentinel = "C06ESENTINEL9f3a7b2c"

func requireExportCode(t *testing.T, err error, code string) *contracts.AppError {
	t.Helper()
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("期望 AppError(%s)，实际 %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
	return appErr
}

// writeExportTranscript 写一份合成 transcript。
//
// 形状取自 internal/history/transcript.go 已声明认得的两种消息形状，
// 外加会话头与工具事件（工具事件在 history 里是"不可识别行"，但导出必须保留它们）。
func writeExportTranscript(t *testing.T, agentDir string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("创建 sessions 目录失败: %v", err)
	}
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	path := filepath.Join(dir, exportSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写合成 transcript 失败: %v", err)
	}
	return path
}

// newExportFixture 建好临时库、临时 agentDir、临时导出根，返回导出手柄与根。
func newExportFixture(t *testing.T, lines ...string) (*Exporter, string, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(context.Background(), filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("打开临时库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移临时库: %v", err)
	}
	agentDir := filepath.Join(dir, "agent")
	transcriptPath := writeExportTranscript(t, agentDir, lines...)
	hist, err := history.Open(context.Background(), db, history.Options{
		AgentDir:      agentDir,
		WorkspaceID:   "ws-export",
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("构造历史服务: %v", err)
	}
	exportRoot := filepath.Join(dir, "exports")
	exp, err := NewExporter(hist, exportRoot)
	if err != nil {
		t.Fatalf("构造导出服务: %v", err)
	}
	return exp, exportRoot, transcriptPath
}

// exportFixtureTranscript 是一份同时含用户消息、助手消息（带工具调用 part）、
// 工具开始/结束事件与"裸 role 形状"消息的 transcript。
func exportFixtureTranscript() []string {
	return []string{
		`{"type":"session","sessionId":"` + exportSessionID + `"}`,
		`{"type":"message","id":"msg-1","timestamp":"2026-11-06T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"第一条用户消息：把 README 改一下"}]}}`,
		`{"type":"message","id":"msg-2","timestamp":"2026-11-06T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"我先读一下文件"},{"type":"tool_call","id":"call-1","name":"fixture_read","args":{"path":"README.md"}}]}}`,
		`{"type":"tool_execution_start","id":"evt-1","toolName":"fixture_read","args":{"path":"README.md"}}`,
		`{"type":"tool_execution_end","id":"evt-1","toolName":"fixture_read","result":{"text":"# README\n第一行\n第二行"}}`,
		`{"role":"user","content":"裸形状的追问：保留工具结果，不要摘要"}`,
	}
}

// ---------------------------------------------------------------------------
// 用例 1：导出内容与 transcript 逐条对得上
// ---------------------------------------------------------------------------

func TestExportMatchesTranscript(t *testing.T) {
	lines := exportFixtureTranscript()
	exp, root, transcriptPath := newExportFixture(t, lines...)

	raw, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("读 transcript: %v", err)
	}
	wantRecords := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			wantRecords++
		}
	}

	var jsonDoc exportDocument
	for _, tc := range []struct{ format, ext string }{
		{ExportFormatJSON, ".json"},
		{ExportFormatMarkdown, ".md"},
		{ExportFormatHTML, ".html"},
	} {
		target := filepath.Join(root, exportSessionID+tc.ext)
		res, err := exp.ExportSession(context.Background(), exportSessionID, tc.format, target)
		if err != nil {
			t.Fatalf("导出 %s 失败: %v", tc.format, err)
		}
		if res.Format != tc.format || res.Path == "" || res.Bytes == 0 || res.SHA256 == "" {
			t.Fatalf("导出结果字段不完整: %+v", res)
		}
		if res.Records != wantRecords {
			t.Fatalf("%s：每一条 transcript 行都必须出现在导出里，期望 %d 条，实际 %d 条",
				tc.format, wantRecords, res.Records)
		}
		if res.Messages != 3 {
			t.Fatalf("%s：可识别消息应为 3 条，实际 %d", tc.format, res.Messages)
		}
		got, err := os.ReadFile(res.Path)
		if err != nil {
			t.Fatalf("读导出文件: %v", err)
		}
		if len(got) != res.Bytes {
			t.Fatalf("%s：结果里的字节数 %d 与实际文件 %d 不一致", tc.format, res.Bytes, len(got))
		}

		switch tc.format {
		case ExportFormatJSON:
			if err := json.Unmarshal(got, &jsonDoc); err != nil {
				t.Fatalf("导出的 JSON 无法解析: %v\n%s", err, got)
			}
			if jsonDoc.SessionID != exportSessionID {
				t.Fatalf("JSON 导出的会话 ID 不对: %q", jsonDoc.SessionID)
			}
			if len(jsonDoc.Records) != wantRecords {
				t.Fatalf("JSON records 数量 %d != %d", len(jsonDoc.Records), wantRecords)
			}
			// 每一行的行号必须出现且只出现一次：不允许"跳行"式摘要。
			seen := map[int]bool{}
			for _, rec := range jsonDoc.Records {
				if seen[rec.Line] {
					t.Fatalf("JSON 导出里第 %d 行重复", rec.Line)
				}
				seen[rec.Line] = true
			}
			if len(seen) != wantRecords {
				t.Fatalf("JSON 导出缺行: %v", seen)
			}
			var texts []string
			for _, rec := range jsonDoc.Records {
				texts = append(texts, rec.Text)
			}
			for _, want := range []string{
				"第一条用户消息：把 README 改一下",
				"我先读一下文件",
				"裸形状的追问：保留工具结果，不要摘要",
			} {
				if !containsString(texts, want) {
					t.Fatalf("JSON 导出缺少消息原文 %q: %#v", want, texts)
				}
			}
			// 工具事件是"不可识别行"，但在导出里必须原样保留（含结果文本）。
			if !strings.Contains(string(got), "fixture_read") ||
				!strings.Contains(string(got), "第二行") ||
				!strings.Contains(string(got), "evt-1") {
				t.Fatalf("JSON 导出丢了工具事件/结果:\n%s", got)
			}
			if jsonDoc.TranscriptSHA256 == "" {
				t.Fatal("JSON 导出必须记录来源 transcript 的 hash")
			}
			wantSHA := exportSHA256(raw)
			if jsonDoc.TranscriptSHA256 != wantSHA {
				t.Fatalf("transcript hash 不符：%q != %q", jsonDoc.TranscriptSHA256, wantSHA)
			}
		case ExportFormatMarkdown:
			for _, want := range []string{
				"第一条用户消息：把 README 改一下",
				"我先读一下文件",
				"裸形状的追问：保留工具结果，不要摘要",
				"fixture_read",
				"第二行",
			} {
				if !strings.Contains(string(got), want) {
					t.Fatalf("Markdown 导出缺少 %q:\n%s", want, got)
				}
			}
		case ExportFormatHTML:
			for _, want := range []string{
				"第一条用户消息：把 README 改一下",
				"我先读一下文件",
				"裸形状的追问：保留工具结果，不要摘要",
				"fixture_read",
				"第二行",
			} {
				if !strings.Contains(string(got), html.EscapeString(want)) {
					t.Fatalf("HTML 导出缺少 %q:\n%s", want, got)
				}
			}
		}
	}

	// 助手消息里带工具调用 part：导出不能只剩正文而把 part 丢掉。
	if !strings.Contains(string(mustReadFile(t, filepath.Join(root, exportSessionID+".json"))), "call-1") {
		t.Fatal("助手消息里的 tool_call part 被丢掉了")
	}
	// SES03 的外部观察：三份导出文件的磁盘字节（大小/sha256 从文件算）。
	entries := scanTree(t, root)
	observeTree(t, root, "exports.json", map[string]any{
		"exportFormats":       []string{ExportFormatJSON, ExportFormatMarkdown, ExportFormatHTML},
		"exportFileCount":     len(entries),
		"transcriptRecordCnt": wantRecords,
	})
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	return raw
}

func exportSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// 用例 2：HTML 里的标记只能是文本
// ---------------------------------------------------------------------------

var (
	htmlTagRe  = regexp.MustCompile(`<[^>]*>`)
	htmlAttrRe = regexp.MustCompile(`\s([A-Za-z_:][-A-Za-z0-9_:.]*)\s*=\s*"`)
)

// htmlAllowedTags 是导出模板里唯一允许的标签集合。
// 内容是转义过的，所以文档里任何 `<` 都只能来自模板；出现别的标签就是漏洞。
var htmlAllowedTags = map[string]bool{
	"<!DOCTYPE html>":          true,
	"<html lang=\"zh-CN\">":    true,
	"<head>":                   true,
	"</head>":                  true,
	"<meta charset=\"utf-8\">": true,
	"<title>":                  true, "</title>": true,
	"<style>": true, "</style>": true,
	"<body>": true, "</body>": true,
	"</html>":  true,
	"<header>": true, "</header>": true,
	"<main>": true, "</main>": true,
	"<h1>": true, "</h1>": true,
	"<dl>": true, "</dl>": true,
	"<dt>": true, "</dt>": true,
	"<dd>": true, "</dd>": true,
}

func assertExportHTMLHasNoActiveContent(t *testing.T, doc string) {
	t.Helper()
	lower := strings.ToLower(doc)
	if strings.Contains(lower, "<script") || strings.Contains(lower, "</script") {
		t.Fatalf("导出 HTML 出现脚本标签:\n%s", doc)
	}
	if strings.Contains(lower, "javascript:") {
		// 内容里的 javascript: 只能以转义文本出现；出现在任何标签/属性里都是漏洞。
		for _, tag := range htmlTagRe.FindAllString(doc, -1) {
			if strings.Contains(strings.ToLower(tag), "javascript:") {
				t.Fatalf("导出 HTML 在标签里出现 javascript: URL: %s", tag)
			}
		}
	}
	allowedPrefix := []string{"<article", "</article>", "<h2>", "</h2>", "<pre>", "</pre>", "<pre class=", "<dl class="}
	for _, tag := range htmlTagRe.FindAllString(doc, -1) {
		if htmlAllowedTags[tag] {
			continue
		}
		ok := false
		for _, prefix := range allowedPrefix {
			if strings.HasPrefix(tag, prefix) {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("导出 HTML 出现模板外的标签 %q（内容必须转义成文本）", tag)
		}
		if strings.Contains(strings.ToLower(tag), "on") && htmlAttrOnEvent(tag) {
			t.Fatalf("导出 HTML 出现事件处理属性: %s", tag)
		}
	}
	// 属性必须是固定白名单：没有 href/src/action，也就没有 URL 落点。
	// 注意：只能查**标签里**的属性——正文里的 `src=x`、`javascript:` 是转义后的文本，
	// 它们本身无害，对全文做子串扫描只会把"正确转义"误判成漏洞。
	allowedAttrs := map[string]bool{"lang": true, "charset": true, "class": true, "data-line": true}
	for _, m := range htmlAttrRe.FindAllStringSubmatch(doc, -1) {
		if !allowedAttrs[strings.ToLower(m[1])] {
			t.Fatalf("导出 HTML 出现白名单外的属性 %q", m[1])
		}
	}
	for _, tag := range htmlTagRe.FindAllString(doc, -1) {
		lowerTag := strings.ToLower(tag)
		for _, attr := range []string{"href=", "src=", "action=", "formaction=", "javascript:"} {
			if strings.Contains(lowerTag, attr) {
				t.Fatalf("导出 HTML 的属性位置出现 %q（不应有任何 URL 位置）: %s", attr, tag)
			}
		}
	}
}

// htmlAttrOnEvent 判断一个标签里是否带 onXxx= 事件属性（用带引号的赋值识别，
// 避免把正文里的 onerror=alert(1) 误判成属性）。
func htmlAttrOnEvent(tag string) bool {
	for _, m := range htmlAttrRe.FindAllStringSubmatch(tag, -1) {
		if strings.HasPrefix(strings.ToLower(m[1]), "on") {
			return true
		}
	}
	return false
}

func TestExportHTMLEscapesActiveContent(t *testing.T) {
	payloads := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`[点我](javascript:alert(1))`,
		`</textarea><script>alert(2)</script>`,
	}
	lines := []string{
		`{"type":"session","sessionId":"` + exportSessionID + `"}`,
		`{"type":"message","message":{"role":"user","content":"` + strings.Join(payloads, " ") + `"}}`,
	}
	exp, root, _ := newExportFixture(t, lines...)

	res, err := exp.ExportSession(context.Background(), exportSessionID, ExportFormatHTML, filepath.Join(root, "x.html"))
	if err != nil {
		t.Fatalf("导出 HTML 失败: %v", err)
	}
	doc := string(mustReadFile(t, res.Path))

	assertExportHTMLHasNoActiveContent(t, doc)

	// 标记必须真的以文本出现（转义），而不是被删掉：导出不是"过滤"而是"转义"。
	for _, p := range payloads[:3] {
		if !strings.Contains(doc, html.EscapeString(p)) {
			t.Fatalf("转义后的 %q 不在导出里（导出必须保留原文，只是当文本）:\n%s", p, doc)
		}
	}
	if !strings.Contains(doc, "&lt;/textarea&gt;&lt;script&gt;alert(2)&lt;/script&gt;") {
		t.Fatalf("</textarea> break-out 必须以转义文本出现:\n%s", doc)
	}
	for _, bad := range []string{"<script", "</script", "<img", "</textarea", "<iframe"} {
		if strings.Contains(strings.ToLower(doc), bad) {
			t.Fatalf("导出 HTML 出现未转义的 %q:\n%s", bad, doc)
		}
	}

	// SES03 的外部观察：磁盘上的 HTML 里到底还有没有可执行标签（重新扫描文件）。
	entries := scanTree(t, root)
	forbidden := []string{}
	for _, e := range entries {
		body := string(mustReadFile(t, filepath.Join(root, filepath.FromSlash(e.Path))))
		for _, bad := range []string{"<script", "<img", "</textarea"} {
			if strings.Contains(strings.ToLower(body), bad) {
				forbidden = append(forbidden, e.Path+" "+bad)
			}
		}
	}
	observeTree(t, root, "exports.json", map[string]any{
		"activeContentHits": forbidden,
		"escapedPayloads":   len(payloads),
	})
}

// ---------------------------------------------------------------------------
// 用例 3：sentinel 命中 0，同时保留事件 ID / 工具名
// ---------------------------------------------------------------------------

func TestExportRedactsSentinels(t *testing.T) {
	lines := []string{
		`{"type":"session","sessionId":"` + exportSessionID + `"}`,
		`{"type":"message","message":{"role":"user","content":"我的 api_key=` + exportSentinel + ` 请不要外泄"}}`,
		`{"type":"tool_execution_start","id":"evt-args","toolName":"fixture_write","args":{"token":"` + exportSentinel + `","path":"a.txt"}}`,
		`{"type":"tool_execution_end","id":"evt-url","toolName":"fixture_fetch","result":{"url":"https://api.example.test/v1/run?token=` + exportSentinel + `","status":200}}`,
		`{"type":"tool_execution_end","id":"evt-auth","toolName":"fixture_http","result":{"text":"Authorization: Bearer ` + exportSentinel + `"}}`,
	}
	exp, root, _ := newExportFixture(t, lines...)

	for _, tc := range []struct{ format, ext string }{
		{ExportFormatHTML, ".html"},
		{ExportFormatMarkdown, ".md"},
		{ExportFormatJSON, ".json"},
	} {
		res, err := exp.ExportSession(context.Background(), exportSessionID, tc.format, filepath.Join(root, "redacted"+tc.ext))
		if err != nil {
			t.Fatalf("%s 导出失败: %v", tc.format, err)
		}
		got := string(mustReadFile(t, res.Path))
		if n := strings.Count(got, exportSentinel); n != 0 {
			t.Fatalf("%s 导出仍含 sentinel %d 次:\n%s", tc.format, n, got)
		}
		if res.Redactions == 0 {
			t.Fatalf("%s 导出没有报告任何脱敏命中，说明规则没跑", tc.format)
		}
		// 脱敏不能把故障定位信息一起抹掉。
		for _, want := range []string{"evt-args", "evt-url", "evt-auth", "fixture_write", "fixture_fetch", "fixture_http"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s 导出丢了定位信息 %q:\n%s", tc.format, want, got)
			}
		}
		if tc.format == ExportFormatJSON {
			var doc exportDocument
			if err := json.Unmarshal([]byte(got), &doc); err != nil {
				t.Fatalf("脱敏后的 JSON 不再是合法 JSON: %v", err)
			}
		}
	}
	// SES03 的外部观察：磁盘上每份导出文件对 sentinel 的命中数（重新读文件数）。
	entries := scanTree(t, root)
	hits := sentinelHits(t, entries, root, exportSentinel)
	observeTree(t, root, "exports.json", map[string]any{
		"sentinel":     "C06E sentinel",
		"sentinelHits": hits,
		"exportFiles":  len(entries),
	})
}

// ---------------------------------------------------------------------------
// 用例 4：目标路径越界/链接/目录一律拒绝，成功导出不留临时文件
// ---------------------------------------------------------------------------

func TestExportWriteIsAtomicAndRefusesEscapes(t *testing.T) {
	lines := exportFixtureTranscript()
	exp, root, _ := newExportFixture(t, lines...)
	ctx := context.Background()

	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "leak.html")

	cases := []struct {
		name   string
		target string
		code   string
	}{
		{"相对路径越界", filepath.Join("..", "escape.html"), contracts.CodeUnauthorized},
		{"绝对路径越界", outsideTarget, contracts.CodeUnauthorized},
		{"点段拼接越界", filepath.Join(root, "..", "escape2.html"), contracts.CodeUnauthorized},
		{"NUL 字节", filepath.Join(root, "a\x00b.html"), contracts.CodeUnauthorized},
		{"目录目标", root, contracts.CodeUnsupported},
		{"父目录不存在", filepath.Join(root, "no-such-dir", "x.html"), contracts.CodeNotFound},
	}
	for _, tc := range cases {
		res, err := exp.ExportSession(ctx, exportSessionID, ExportFormatHTML, tc.target)
		if err == nil {
			t.Fatalf("%s：越界/非法目标必须被拒绝，实际成功: %+v", tc.name, res)
		}
		requireExportCode(t, err, tc.code)
	}
	if _, err := os.Stat(outsideTarget); err == nil {
		t.Fatalf("越界目标被写出来了: %s", outsideTarget)
	}

	// 导出根里的链接（Windows 目录联接 / 其它平台目录符号链接）指向根外时必须拒绝。
	linkDir := filepath.Join(root, "linked")
	makeAttachmentDirLink(t, linkDir, outside)
	_, err := exp.ExportSession(ctx, exportSessionID, ExportFormatHTML, filepath.Join(linkDir, "leak.html"))
	requireExportCode(t, err, contracts.CodeUnauthorized)
	if _, err := os.Stat(filepath.Join(outside, "leak.html")); err == nil {
		t.Fatal("经过链接写出了根外文件")
	}

	// 成功导出：文件完整、不留临时文件、不产生别的残留。
	target := filepath.Join(root, "ok.html")
	res, err := exp.ExportSession(ctx, exportSessionID, ExportFormatHTML, target)
	if err != nil {
		t.Fatalf("正常导出失败: %v", err)
	}
	if res.Path != target {
		t.Fatalf("导出路径应为 %s，实际 %s", target, res.Path)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读导出目录: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "ok.html" || name == "linked" {
			continue
		}
		t.Fatalf("导出目录里出现残留（临时文件没清掉？）: %s", name)
	}

	// 原子写：重复导出到同一路径得到的是完整的新内容，而不是半份拼接。
	first := mustReadFile(t, target)
	res2, err := exp.ExportSession(ctx, exportSessionID, ExportFormatHTML, target)
	if err != nil {
		t.Fatalf("重复导出失败: %v", err)
	}
	second := mustReadFile(t, target)
	if string(first) != string(second) {
		t.Fatalf("同一 transcript 的两次导出内容不一致（%d vs %d 字节）", len(first), len(second))
	}
	if res2.SHA256 != res.SHA256 {
		t.Fatalf("同一输入的导出 hash 应当一致: %s vs %s", res2.SHA256, res.SHA256)
	}
	// SES03 的外部观察：导出目录里没有临时残留、越界目标不存在（都在磁盘上核）。
	entries = entries[:0]
	rootEntries, rerr := os.ReadDir(root)
	if rerr != nil {
		t.Fatalf("读导出目录: %v", rerr)
	}
	names := []string{}
	for _, e := range rootEntries {
		names = append(names, e.Name())
	}
	outsideEntries, _ := os.ReadDir(outside)
	outsideNames := []string{}
	for _, e := range outsideEntries {
		outsideNames = append(outsideNames, e.Name())
	}
	observeTree(t, root, "exports.json", map[string]any{
		"exportRootEntries":  names,
		"outsideEntries":     outsideNames,
		"tempFilesLeft":      names,
		"repeatedExportSame": res2.SHA256 == res.SHA256,
	})
}

// ---------------------------------------------------------------------------
// 用例 5：未知格式必须结构化拒绝，并点名允许的格式
// ---------------------------------------------------------------------------

func TestExportUnknownFormatRejected(t *testing.T) {
	exp, root, _ := newExportFixture(t, exportFixtureTranscript()...)
	_, err := exp.ExportSession(context.Background(), exportSessionID, "pdf", filepath.Join(root, "x.pdf"))
	appErr := requireExportCode(t, err, contracts.CodeUnsupported)
	for _, want := range []string{"html", "markdown", "json"} {
		if !strings.Contains(appErr.Message, want) {
			t.Fatalf("错误信息要点名允许的格式 %q，实际 %q", want, appErr.Message)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "x.pdf")); statErr == nil {
		t.Fatal("未知格式不能产生任何文件")
	}

	// 大小写与首尾空白不该变成"未知格式"，但也不能变成任意格式。
	res, err := exp.ExportSession(context.Background(), exportSessionID, " HTML ", filepath.Join(root, "case.html"))
	if err != nil {
		t.Fatalf("大小写/空白应被规范化: %v", err)
	}
	if res.Format != ExportFormatHTML {
		t.Fatalf("格式应规范化为 html，实际 %q", res.Format)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("规范化后的导出文件不存在: %v", err)
	}
}

// 时间戳字段必须稳定地出现在 JSON 导出里（可定位到导出时刻，而不是"看起来能用"）。
func TestExportJSONCarriesExportTime(t *testing.T) {
	exp, root, _ := newExportFixture(t, exportFixtureTranscript()...)
	fixed := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	exp.SetClock(func() time.Time { return fixed })

	res, err := exp.ExportSession(context.Background(), exportSessionID, ExportFormatJSON, filepath.Join(root, "ts.json"))
	if err != nil {
		t.Fatalf("导出 JSON 失败: %v", err)
	}
	var doc exportDocument
	if err := json.Unmarshal(mustReadFile(t, res.Path), &doc); err != nil {
		t.Fatalf("解析 JSON 导出: %v", err)
	}
	if !doc.ExportedAt.Equal(fixed) {
		t.Fatalf("导出时间应为注入的时钟值，实际 %s", doc.ExportedAt)
	}
	if doc.Format != ExportFormatJSON || doc.FormatVersion != exportFormatVersion {
		t.Fatalf("导出文档头不对: %+v", doc)
	}
}
