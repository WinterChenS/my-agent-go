package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

// transcriptSentinel 是只应出现在 transcript 文件里、绝不应出现在数据库里的正文。
const transcriptSentinel = "TRANSCRIPT-SENTINEL-7d1f"

// writeSyntheticTranscript 写一份**合成** transcript。
//
// 诚实说明：这不是 Pi 1.5 的真实会话记录，真实格式没有在本任务里被认证过。
// 它只覆盖本包声明的容错契约：两种可识别的消息形状
// （{"type":"message","message":{...}} 与裸 {"role":...}），以及若干不可识别的行
// （会话头、工具事件）。真实 Pi 记录在改变形状时必须退化成
// "定位信息 + 跳过计数"，而不是猜出一份错误的消息列表。
func writeSyntheticTranscript(t *testing.T, agentDir string, sessionID contracts.SessionID, sentinel string, messages int) string {
	t.Helper()
	dir := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("创建 sessions 目录失败: %v", err)
	}
	roles := []string{"user", "assistant"}
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"session","sessionId":%q}`+"\n", string(sessionID))
	for i := 0; i < messages; i++ {
		text := fmt.Sprintf("第 %d 条合成消息", i+1)
		if i == 0 {
			text += " " + sentinel
		}
		role := roles[i%len(roles)]
		if i%2 == 0 {
			fmt.Fprintf(&b, `{"type":"message","message":{"role":%q,"content":[{"type":"text","text":%q}]}}`+"\n", role, text)
			continue
		}
		fmt.Fprintf(&b, `{"role":%q,"content":%q}`+"\n", role, text)
	}
	b.WriteString(`{"type":"tool_execution_start","toolName":"read","args":{}}` + "\n")

	path := filepath.Join(dir, string(sessionID)+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("写合成 transcript 失败: %v", err)
	}
	return path
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// TestTranscriptReadableAfterRestart 覆盖 RUN07 的第一条事实的另一半：
// 重启后 transcript 仍可读，正文一个字节都没被产品改写，跳过计数如实上报，
// 定位不靠"猜一个空结果"——找不到就是 CONFIG_MISSING。
func TestTranscriptReadableAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	agentDir := filepath.Join(dir, "agent")
	workspaceDir := filepath.Join(dir, "ws")
	path := writeSyntheticTranscript(t, agentDir, historySessionID, transcriptSentinel, 3)
	before := sha256File(t, path)

	db := openTestDB(t, dbPath)
	idx := openTestHistory(t, db, agentDir, workspaceDir)
	if err := idx.RecordRun(ctx, contracts.RunSnapshot{
		RunID: "run-transcript", SessionID: historySessionID, WorkspaceID: historyWorkspaceID,
		State: contracts.RunStateCompleted, LastSeq: 12,
	}); err != nil {
		t.Fatalf("写入索引失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 重启：新句柄 + 新服务。
	db2 := openTestDB(t, dbPath)
	idx2 := openTestHistory(t, db2, agentDir, workspaceDir)

	tr, err := idx2.Transcript(ctx, historySessionID)
	if err != nil {
		t.Fatalf("重启后读取 transcript 失败: %v", err)
	}
	if len(tr.Messages) != 3 {
		t.Fatalf("应有 3 条消息，实际 %d 条: %+v", len(tr.Messages), tr.Messages)
	}
	// 会话头 + 工具事件是不可识别行，必须计入跳过而不是让整份解析失败。
	if tr.Skipped != 2 {
		t.Fatalf("应跳过 2 行（会话头 + 工具事件），实际 %d 行", tr.Skipped)
	}
	if tr.Path != path {
		t.Fatalf("定位信息应为 %q，实际 %q", path, tr.Path)
	}
	if tr.Messages[0].Role != "user" || !strings.Contains(tr.Messages[0].Text, transcriptSentinel) {
		t.Fatalf("第一条消息不符合预期: %+v", tr.Messages[0])
	}
	if tr.Messages[1].Role != "assistant" || tr.Messages[1].Text == "" {
		t.Fatalf("裸 role 形状的消息没有被识别: %+v", tr.Messages[1])
	}

	// 只读：字节与 sha256 都不变（Pi transcript 是独立权威）。
	if after := sha256File(t, path); after != before {
		t.Fatalf("transcript 被改写了：before=%s after=%s", before, after)
	}

	// 找不到 transcript 不能编一个空结果。
	_, err = idx2.Transcript(ctx, "sess-does-not-exist")
	if err == nil {
		t.Fatalf("缺少 transcript 时不得返回空结果")
	}
	appErr, ok := err.(*contracts.AppError)
	if !ok || appErr.Code != contracts.CodeConfigMissing {
		t.Fatalf("缺少 transcript 应返回 CONFIG_MISSING，实际 %#v", err)
	}

	// 文件名不等于 session id 时，靠首行 sessionId 定位。
	otherDir := filepath.Join(dir, "agent-renamed")
	renamed := filepath.Join(otherDir, "sessions", "pi-renamed.jsonl")
	if err := os.MkdirAll(filepath.Dir(renamed), 0o700); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	body := fmt.Sprintf(`{"type":"session","sessionId":%q}`+"\n"+`{"role":"user","content":"只有一行正文"}`+"\n", string(historySessionID))
	if err := os.WriteFile(renamed, []byte(body), 0o600); err != nil {
		t.Fatalf("写改名 transcript 失败: %v", err)
	}
	idx3 := openTestHistory(t, db2, otherDir, workspaceDir)
	tr3, err := idx3.Transcript(ctx, historySessionID)
	if err != nil {
		t.Fatalf("按首行 sessionId 定位失败: %v", err)
	}
	if tr3.Path != renamed || len(tr3.Messages) != 1 {
		t.Fatalf("按首行 sessionId 定位结果不符合预期: path=%q messages=%d", tr3.Path, len(tr3.Messages))
	}
}
