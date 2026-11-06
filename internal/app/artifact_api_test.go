package app

// C07 产物绑定层测试：绑定只做薄封装，真实边界由 internal/artifact 负责。
//
// 这里钉住三件"只能在装配层发现"的事情：
//  1. 工作区真实路径来自 C01 的工作区服务（不是绑定层自己拼字符串）；
//  2. 产物索引与 usage/事件共用会话库（同一个 <appRoot>/sessions.db 的 007 迁移）；
//  3. 预览/导出只接受产物 id——绑定层没有"按任意本机路径打开文件"的入口。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"my-agent-go/internal/contracts"
)

func TestArtifactAPIRoundTrip(t *testing.T) {
	d, root := newTestDesktop(t)

	wsDir := filepath.Join(root, "artifacts-ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatalf("建工作区: %v", err)
	}
	ws := d.CreateWorkspace(wsDir)
	if ws.Error != nil {
		t.Fatalf("CreateWorkspace: %+v", ws.Error)
	}
	wsID := string(ws.Item.ID)

	body := []byte("产物内容：第一版\n")
	path := filepath.Join(wsDir, "report.md")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("写产物: %v", err)
	}

	imported := d.ImportArtifact(path, wsID, "run-1")
	if imported.Error != nil {
		t.Fatalf("ImportArtifact: %+v", imported.Error)
	}
	if imported.Item.WorkspaceID != ws.Item.ID {
		t.Fatalf("工作区归属不符：%q != %q", imported.Item.WorkspaceID, ws.Item.ID)
	}
	if imported.Item.RunID != "run-1" {
		t.Fatalf("run 归属不符：%q", imported.Item.RunID)
	}
	if imported.Item.Verification != nil {
		t.Fatalf("导入不应自称已核对：%+v", imported.Item.Verification)
	}

	got := d.GetArtifact(imported.Item.ID)
	if got.Error != nil {
		t.Fatalf("GetArtifact: %+v", got.Error)
	}
	if got.Item.Verification == nil || got.Item.Verification.Status != "verified" {
		t.Fatalf("刚导入应为 verified，实际 %+v", got.Item.Verification)
	}

	list := d.ListRunArtifacts("run-1")
	if list.Error != nil {
		t.Fatalf("ListRunArtifacts: %+v", list.Error)
	}
	if len(list.Items) != 1 || list.Items[0].ID != imported.Item.ID {
		t.Fatalf("按 run 列举不符：%+v", list.Items)
	}

	preview := d.PreviewArtifact(imported.Item.ID)
	if preview.Error != nil {
		t.Fatalf("PreviewArtifact: %+v", preview.Error)
	}
	if preview.Preview.Kind != "text" {
		t.Fatalf("文本产物预览 kind 应为 text，实际 %s", preview.Preview.Kind)
	}

	export := d.ExportArtifact(imported.Item.ID, "report.md")
	if export.Error != nil {
		t.Fatalf("ExportArtifact: %+v", export.Error)
	}
	if export.Path != filepath.Join(root, "exports", "report.md") {
		t.Fatalf("导出目标应在 <appRoot>/exports 内：%s", export.Path)
	}
	if _, err := os.Stat(export.Path); err != nil {
		t.Fatalf("导出文件不存在: %v", err)
	}

	// 工作区外的路径：结构化拒绝，且不产生索引行。
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("外部文件"), 0o600); err != nil {
		t.Fatalf("写外部文件: %v", err)
	}
	denied := d.ImportArtifact(outside, wsID, "")
	if denied.Error == nil {
		t.Fatalf("工作区外的路径必须被拒绝")
	}
	if denied.Error.Code != contracts.CodeUnauthorized {
		t.Fatalf("越界导入应返回 UNAUTHORIZED，实际 %s（%s）", denied.Error.Code, denied.Error.Message)
	}
	if all := d.ListArtifacts(ArtifactListQuery{WorkspaceID: wsID}); len(all.Items) != 1 {
		t.Fatalf("被拒的导入不应留下索引行，实际 %d 条", len(all.Items))
	}

	// 文件被改写：绑定层如实转出 drifted（不拿旧 hash 冒充现状）。
	if err := os.WriteFile(path, []byte("产物内容：被外部改写\n"), 0o600); err != nil {
		t.Fatalf("改写产物: %v", err)
	}
	drifted := d.GetArtifact(imported.Item.ID)
	if drifted.Error != nil {
		t.Fatalf("GetArtifact(drift): %+v", drifted.Error)
	}
	if drifted.Item.Verification == nil || drifted.Item.Verification.Status != "drifted" {
		t.Fatalf("改写后应报 drifted，实际 %+v", drifted.Item.Verification)
	}
	if drifted.Item.Verification.ObservedSHA256 == drifted.Item.SHA256 {
		t.Fatalf("drifted 时实际 hash 不应等于索引 hash")
	}
}

// TestArtifactAPISharesIndexDatabase 证明产物索引与 usage/事件在同一张库里。
func TestArtifactAPISharesIndexDatabase(t *testing.T) {
	d, root := newTestDesktop(t)
	if _, err := d.artifactService(); err != nil {
		t.Fatalf("artifactService: %v", err)
	}
	dbPath := filepath.Join(root, "sessions.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("产物服务应复用 <appRoot>/sessions.db: %v", err)
	}
	db, err := d.indexDB()
	if err != nil {
		t.Fatalf("indexDB: %v", err)
	}
	for _, table := range []string{"artifacts", "run_usage", "run_events"} {
		var name string
		if err := db.SQL().QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("索引库里没有 007 的表 %s: %v", table, err)
		}
	}
}
