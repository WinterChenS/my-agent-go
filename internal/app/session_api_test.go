package app

// C06b 绑定层测试：目录 API 只能是薄绑定。
//
// 这里守两件事：
//   - 工作区隔离：请求哪个工作区就只返回那个工作区的行，别的项目的会话不能漏出来；
//   - "普通会话"是真的过滤条件：归档（除非明确要）与自动化/调度会话都不在目录里，
//     而不是靠前端不显示。
//
// 全部在 t.TempDir() 里跑：临时 appData + 临时库，不碰真实配置。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/session"
	"my-agent-go/internal/storage"
)

func newSessionWorkspace(t *testing.T, d *Desktop, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("建工作区目录: %v", err)
	}
	res := d.CreateWorkspace(dir)
	if res.Error != nil {
		t.Fatalf("CreateWorkspace: %+v", res.Error)
	}
	return string(res.Item.ID)
}

func listSessionIDs(t *testing.T, res SessionListResult) []string {
	t.Helper()
	if res.Error != nil {
		t.Fatalf("ListSessions 返回错误: %+v", res.Error)
	}
	var out []string
	for _, g := range res.Groups {
		for _, s := range g.Sessions {
			out = append(out, string(s.ID))
		}
	}
	return out
}

func TestSessionAPIBindsQueriesWithoutLeakingRows(t *testing.T) {
	d, root := newTestDesktop(t)
	wsA := newSessionWorkspace(t, d, root, "alpha")
	wsB := newSessionWorkspace(t, d, root, "beta")

	// 先读一次，确保会话库已建好并迁移到位。
	if res := d.ListSessions(SessionListQuery{}); res.Error != nil {
		t.Fatalf("初次 ListSessions: %+v", res.Error)
	}

	create := func(workspaceID string) string {
		t.Helper()
		res := d.CreateSession(workspaceID)
		if res.Error != nil {
			t.Fatalf("CreateSession(%q): %+v", workspaceID, res.Error)
		}
		return string(res.Item.ID)
	}
	a1 := create(wsA)
	a2 := create(wsA)
	b1 := create(wsB)

	// 归档一条：默认查询里不能出现。
	stale := d.ListSessions(SessionListQuery{WorkspaceID: wsA, IncludeArchived: true})
	if stale.Error != nil {
		t.Fatalf("ListSessions: %+v", stale.Error)
	}
	arch := d.ArchiveSession(a2, 1, true)
	if arch.Error != nil {
		t.Fatalf("ArchiveSession: %+v", arch.Error)
	}

	// 自动化/调度会话（C09 写自己的 kind）直接落在同一个库里：
	// 它不该出现在会话目录，即使工作区对得上。
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatalf("打开会话库: %v", err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO sessions(id, workspace_id, title, kind, created_at, updated_at, revision)
		VALUES('ses-scheduler', ?, '每日汇总', 'scheduler', ?, ?, 1)`, wsA, now, now); err != nil {
		t.Fatalf("写调度会话: %v", err)
	}

	// 1. 默认：只 wsA 的非归档普通会话。
	got := listSessionIDs(t, d.ListSessions(SessionListQuery{WorkspaceID: wsA, Limit: 50}))
	if len(got) != 1 || got[0] != a1 {
		t.Fatalf("wsA 默认目录应只有 %q，实际 %v", a1, got)
	}

	// 2. 明确要归档：归档行回来，调度会话仍然不回来。
	withArchived := listSessionIDs(t, d.ListSessions(SessionListQuery{WorkspaceID: wsA, IncludeArchived: true, Limit: 50}))
	if len(withArchived) != 2 {
		t.Fatalf("含归档时应有 2 条，实际 %v", withArchived)
	}
	for _, id := range withArchived {
		if id == "ses-scheduler" {
			t.Fatalf("调度会话不能出现在会话目录里")
		}
	}

	// 3. 别的项目一行都不能漏。
	other := listSessionIDs(t, d.ListSessions(SessionListQuery{WorkspaceID: wsB, IncludeArchived: true, Limit: 50}))
	if len(other) != 1 || other[0] != b1 {
		t.Fatalf("wsB 只应有 %q，实际 %v", b1, other)
	}

	// 4. 不指定工作区时按工作区分组，且仍然只含普通会话。
	all := d.ListSessions(SessionListQuery{IncludeArchived: true, Limit: 50})
	if all.Error != nil {
		t.Fatalf("全量 ListSessions: %+v", all.Error)
	}
	groups := map[string][]string{}
	total := 0
	for _, g := range all.Groups {
		for _, s := range g.Sessions {
			groups[string(g.WorkspaceID)] = append(groups[string(g.WorkspaceID)], string(s.ID))
			total++
			if s.ID == "ses-scheduler" {
				t.Fatalf("调度会话不能出现在会话目录里")
			}
		}
	}
	if total != 3 {
		t.Fatalf("全量应有 3 条普通会话，实际 %d（%v）", total, groups)
	}
	if len(groups[wsA]) == 0 || len(groups[wsB]) == 0 {
		t.Fatalf("全量结果应按工作区分组，实际 %v", groups)
	}
}

func TestSessionAPICreatesInCurrentWorkspace(t *testing.T) {
	d, _ := newTestDesktop(t)
	// 默认设置给的正是 <root>/workspace；不传 workspaceID 时应落到当前工作区，
	// 而不是造一个"无主会话"。
	res := d.CreateSession("")
	if res.Error != nil {
		t.Fatalf("CreateSession(\"\"): %+v", res.Error)
	}
	if res.Item.WorkspaceID == "" {
		t.Fatalf("会话必须归属一个工作区")
	}
	if res.Item.Revision != 1 || res.Item.Title != "" || res.Item.Pinned || res.Item.Archived {
		t.Fatalf("新会话字段不对：%+v", res.Item)
	}
	list := listSessionIDs(t, d.ListSessions(SessionListQuery{WorkspaceID: string(res.Item.WorkspaceID), Limit: 10}))
	if len(list) != 1 || list[0] != string(res.Item.ID) {
		t.Fatalf("新会话应出现在它所在工作区的目录里，实际 %v", list)
	}
	// 不存在的显式工作区要报错，而不是静默落到默认工作区。
	if bad := d.CreateSession("ws-does-not-exist"); bad.Error == nil {
		t.Fatalf("未知工作区必须报错")
	}
}

// C06c 绑定层测试：ResolveAttachments 只能是"薄绑定 + 证明隔离"。
//
// 守三件事：
//   - 字节真的被复制进 <appRoot>/blobs 的内容地址，而不是留在用户目录里；
//   - blob 不在 run 的 workspace 下（否则 Pi 就有写附件的路径）；
//   - 单个坏文件进 Rejected、不让整批失败；未知会话是结构化错误。
func TestSessionAPIBindsAttachmentResolve(t *testing.T) {
	d, root := newTestDesktop(t)
	ws := newSessionWorkspace(t, d, root, "alpha")
	created := d.CreateSession(ws)
	if created.Error != nil {
		t.Fatalf("CreateSession: %+v", created.Error)
	}

	body := []byte("attachment payload\n")
	srcDir := t.TempDir()
	srcA := filepath.Join(srcDir, "note.txt")
	srcB := filepath.Join(srcDir, "copy.txt")
	for _, p := range []string{srcA, srcB} {
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatalf("写源文件: %v", err)
		}
	}

	res := d.ResolveAttachments(string(created.Item.ID), []session.AttachmentRequest{
		{Path: srcA, DisplayName: "note.txt"},
		{Path: srcB, DisplayName: "copy.txt"},
	})
	if res.Error != nil {
		t.Fatalf("ResolveAttachments: %+v", res.Error)
	}
	if len(res.Items) != 2 || len(res.Rejected) != 0 {
		t.Fatalf("两个文件都应落地：items=%+v rejected=%+v", res.Items, res.Rejected)
	}
	sum := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(sum[:])
	for _, item := range res.Items {
		if item.SHA256 != wantSHA || item.Size != int64(len(body)) || item.Status != session.AttachmentReady {
			t.Fatalf("附件字段不对: %+v", item)
		}
		if item.ID == "" || !strings.HasPrefix(item.ID, "att-") {
			t.Fatalf("附件必须返回只读能力 ID: %+v", item)
		}
	}

	// 内容寻址：同内容只落一个 blob，位置在 <appRoot>/blobs 下。
	blobPath := filepath.Join(root, "blobs", wantSHA[:2], wantSHA)
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("blob 未按内容地址落地: %s (%v)", blobPath, err)
	}
	var blobCount int
	_ = filepath.WalkDir(filepath.Join(root, "blobs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		blobCount++
		return nil
	})
	if blobCount != 1 {
		t.Fatalf("同内容应只有一个 blob，实际 %d", blobCount)
	}

	// blob 不能落在 workspace 里（workspace 是 policy 授予 Pi 的目录）。
	workspaceDir := filepath.Join(root, "workspace")
	if rel, err := filepath.Rel(workspaceDir, blobPath); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("blob 不能位于 workspace 内：workspace=%s blob=%s", workspaceDir, blobPath)
	}

	// 单个坏文件：进 Rejected，Item 为空，但不是整批错误。
	bad := d.ResolveAttachments(string(created.Item.ID), []session.AttachmentRequest{
		{Path: filepath.Join(srcDir, "missing.txt"), DisplayName: "missing.txt"},
	})
	if bad.Error != nil {
		t.Fatalf("单个文件不存在不应让整批失败: %+v", bad.Error)
	}
	if len(bad.Items) != 0 || len(bad.Rejected) != 1 {
		t.Fatalf("坏文件必须逐条拒绝：items=%+v rejected=%+v", bad.Items, bad.Rejected)
	}
	if bad.Rejected[0].Code != contracts.CodeNotFound || bad.Rejected[0].Message == "" {
		t.Fatalf("拒绝原因必须是结构化的：%+v", bad.Rejected[0])
	}

	// 未知会话：整批不可处理，返回结构化错误而不是空成功。
	unknown := d.ResolveAttachments("ses-does-not-exist", []session.AttachmentRequest{{Path: srcA}})
	if unknown.Error == nil || unknown.Error.Code != contracts.CodeNotFound {
		t.Fatalf("未知会话必须返回 NOT_FOUND，实际 %+v", unknown.Error)
	}

	// 源文件仍然是原样（附件是复制，不是移动/回写）。
	got, err := os.ReadFile(srcA)
	if err != nil || string(got) != string(body) {
		t.Fatalf("源文件被改动: err=%v body=%q", err, got)
	}
}

// ---------------------------------------------------------------------------
// C06e：会话导出绑定
// ---------------------------------------------------------------------------

// TestExportSessionBindsTranscript 守绑定层是三件事：
//   - 目标落在应用托管的导出目录（<appRoot>/exports），不是用户随手给的任意路径；
//   - 内容来自 agentDir 下的 Pi transcript，而不是任何内存态；
//   - 越界目标/未知格式/未知会话都返回结构化错误，不产生文件。
func TestExportSessionBindsTranscript(t *testing.T) {
	d, root := newTestDesktop(t)
	ws := newSessionWorkspace(t, d, root, "alpha")
	created := d.CreateSession(ws)
	if created.Error != nil {
		t.Fatalf("CreateSession: %+v", created.Error)
	}
	sid := string(created.Item.ID)

	// 写一份 Pi transcript：agentDir 就是本 Desktop 实例实际交给 Pi 的那个目录。
	transcriptDir := filepath.Join(d.agentDir(), "sessions")
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatalf("建 transcript 目录: %v", err)
	}
	sentinel := "sk-live-C06EBINDSENTINEL123456"
	body := strings.Join([]string{
		`{"type":"session","sessionId":"` + sid + `"}`,
		`{"type":"message","id":"msg-1","message":{"role":"user","content":"导出这一条：api_key=` + sentinel + `"}}`,
		`{"type":"tool_execution_start","id":"evt-1","toolName":"fixture_read","args":{"path":"a.txt"}}`,
		`{"type":"tool_execution_end","id":"evt-1","toolName":"fixture_read","result":{"text":"<script>alert(1)</script>"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(transcriptDir, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("写 transcript: %v", err)
	}

	res := d.ExportSession(sid, "html", "sess.html")
	if res.Error != nil {
		t.Fatalf("ExportSession: %+v", res.Error)
	}
	wantPath := filepath.Join(root, "exports", "sess.html")
	if res.Path != wantPath {
		t.Fatalf("导出路径应在应用导出目录下 %s，实际 %s", wantPath, res.Path)
	}
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("读导出文件: %v", err)
	}
	doc := string(raw)
	if strings.Contains(doc, sentinel) {
		t.Fatalf("绑定层导出没有脱敏:\n%s", doc)
	}
	if strings.Contains(strings.ToLower(doc), "<script") {
		t.Fatalf("绑定层导出没有转义:\n%s", doc)
	}
	if !strings.Contains(doc, "evt-1") || !strings.Contains(doc, "fixture_read") {
		t.Fatalf("绑定层导出丢了定位信息:\n%s", doc)
	}

	// 越界目标：拒绝，且根外没有文件。
	outside := filepath.Join(t.TempDir(), "leak.html")
	if bad := d.ExportSession(sid, "html", outside); bad.Error == nil ||
		bad.Error.Code != contracts.CodeUnauthorized {
		t.Fatalf("越界目标必须 UNAUTHORIZED，实际 %+v", bad.Error)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("越界目标被写出来了")
	}

	// 未知格式：结构化拒绝并点名允许的格式。
	pdf := d.ExportSession(sid, "pdf", "x.pdf")
	if pdf.Error == nil || pdf.Error.Code != contracts.CodeUnsupported {
		t.Fatalf("未知格式必须 UNSUPPORTED，实际 %+v", pdf.Error)
	}
	if !strings.Contains(pdf.Error.Message, "markdown") {
		t.Fatalf("错误信息要点名允许的格式，实际 %q", pdf.Error.Message)
	}

	// 未知会话：不许"导出成功但内容为空"。
	missing := d.ExportSession("ses-does-not-exist", "html", "missing.html")
	if missing.Error == nil || missing.Error.Code != contracts.CodeConfigMissing {
		t.Fatalf("未知会话必须 CONFIG_MISSING，实际 %+v", missing.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "exports", "missing.html")); err == nil {
		t.Fatal("未知会话不应产生导出文件")
	}
}
