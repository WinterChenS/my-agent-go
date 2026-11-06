package app

// C07 诊断绑定层测试。
//
// 这里钉住的都是"装配与口径"层面的事：
//  1. usage 的 unavailable 不能被绑定层翻译成 0；
//  2. run 详情答不出来的段落必须原样传给前端（Unavailable），不能被抹平成空数组；
//  3. 诊断包目标只能在 <appRoot>/diagnostics 内；
//  4. 运行时装配真的接上了记录器：run 快照与事件会落库（否则诊断页永远是空的）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

func TestUsageSummaryAPIUnavailableIsNotZero(t *testing.T) {
	d, _ := newTestDesktop(t)

	empty := d.UsageSummary(UsageSummaryQuery{})
	if empty.Error != nil {
		t.Fatalf("UsageSummary: %+v", empty.Error)
	}
	if empty.Summary.Availability != contracts.UsageUnavailable {
		t.Fatalf("空库应为 unavailable，实际 %q", empty.Summary.Availability)
	}
	raw, err := json.Marshal(empty.Summary)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	if strings.Contains(string(raw), "costUsd") || strings.Contains(string(raw), "totalTokens") {
		t.Fatalf("unavailable 时不应出现 token/cost 键：%s", raw)
	}

	// 有 tokens、没有价格：tokens 照报，cost 仍是 unavailable。
	rec, err := d.diagnosticsRecorder()
	if err != nil {
		t.Fatalf("diagnosticsRecorder: %v", err)
	}
	if err := rec.RecordRun(context.Background(), contracts.RunSnapshot{
		RunID: "run-1", SessionID: "sess-1", WorkspaceID: "ws-1",
		State: contracts.RunStateCompleted,
		Usage: &contracts.RunUsage{
			Availability: contracts.UsageReported,
			InputTokens:  7, OutputTokens: 3, TotalTokens: 10,
		},
	}); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	sum := d.UsageSummary(UsageSummaryQuery{})
	if sum.Error != nil {
		t.Fatalf("UsageSummary: %+v", sum.Error)
	}
	raw, err = json.Marshal(sum.Summary)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	if !strings.Contains(string(raw), `"totalTokens":10`) {
		t.Fatalf("tokens 必须照报：%s", raw)
	}
	if strings.Contains(string(raw), "costUsd") {
		t.Fatalf("没有价格时不应出现 costUsd：%s", raw)
	}

	// 时间范围格式错误必须报错，不能静默当成"不限"。
	bad := d.UsageSummary(UsageSummaryQuery{From: "2026-11-05"})
	if bad.Error == nil || bad.Error.Code != contracts.CodeConfigMissing {
		t.Fatalf("非法时间范围应返回 CONFIG_MISSING，实际 %+v", bad.Error)
	}
}

func TestRunDetailsAPIPassesUnknownThrough(t *testing.T) {
	d, _ := newTestDesktop(t)
	res := d.RunDetails("run-not-recorded")
	if res.Error != nil {
		t.Fatalf("RunDetails: %+v", res.Error)
	}
	if res.Details.Record != nil {
		t.Fatalf("没有记录时 Record 必须为空：%+v", res.Details.Record)
	}
	if len(res.Details.Unavailable) == 0 {
		t.Fatalf("未知段落必须原样传给前端")
	}
	raw, err := json.Marshal(res.Details)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	if !strings.Contains(string(raw), `"section":"record"`) {
		t.Fatalf("record 段落应标记为 unavailable：%s", raw)
	}
}

func TestExportDiagnosticsAPIStaysInItsRoot(t *testing.T) {
	d, root := newTestDesktop(t)

	escape := d.ExportDiagnostics(DiagnosticsExportRequest{Target: ".." + string(os.PathSeparator) + "escape.json"})
	if escape.Error == nil || escape.Error.Code != contracts.CodeUnauthorized {
		t.Fatalf("越界目标应返回 UNAUTHORIZED，实际 %+v", escape.Error)
	}
	ok := d.ExportDiagnostics(DiagnosticsExportRequest{Target: "bundle.json"})
	if ok.Error != nil {
		t.Fatalf("合法目标应通过: %+v", ok.Error)
	}
	if ok.Path != filepath.Join(root, "diagnostics", "bundle.json") {
		t.Fatalf("诊断包路径不符：%s", ok.Path)
	}
	if _, err := os.Stat(ok.Path); err != nil {
		t.Fatalf("诊断包文件不存在: %v", err)
	}
}

// TestRuntimeWiresDiagnosticRecorder 守装配：run 结束后快照与事件真的落库。
//
// 这是"诊断页不是永远空的"的唯一证据：绑定的用法正确但记录器没接上，
// 所有诊断接口都会返回"没有记录"，看起来像"这一轮确实没发生什么"。
func TestRuntimeWiresDiagnosticRecorder(t *testing.T) {
	d := prepareRuntimeDesktop(t)
	work := d.Settings().Workspace
	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s: %s", ws.Error.Code, ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s: %s", res.Error.Code, res.Error.Message)
	}

	worker := newC06aWorker()
	d.mu.Lock()
	d.workerStarter = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		return worker, nil
	}
	d.mu.Unlock()

	snap, err := d.Start("这一步会留下诊断记录")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 结算这一轮，让 run 进入终态（终态才写 usage 结论）。
	worker.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}

	deadline := time.Now().Add(5 * time.Second)
	var (
		state string
		rows  int
	)
	for time.Now().Before(deadline) {
		db, dbErr := d.indexDB()
		if dbErr != nil {
			t.Fatalf("indexDB: %v", dbErr)
		}
		if err := db.SQL().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM run_usage WHERE run_id = ?`, string(snap.RunID)).Scan(&rows); err != nil {
			t.Fatalf("读 run_usage: %v", err)
		}
		if rows > 0 {
			if err := db.SQL().QueryRowContext(context.Background(),
				`SELECT state FROM run_usage WHERE run_id = ?`, string(snap.RunID)).Scan(&state); err != nil {
				t.Fatalf("读 state: %v", err)
			}
			if state == contracts.RunStateCompleted {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rows == 0 {
		t.Fatalf("run 结束后 run_usage 里没有记录：诊断记录器没有接上")
	}
	if state != contracts.RunStateCompleted {
		t.Fatalf("run_usage 应记到终态 completed，实际 %q", state)
	}

	// 事件日志也要有：run.state 会随状态变化落库（message.delta 除外）。
	db, err := d.indexDB()
	if err != nil {
		t.Fatalf("indexDB: %v", err)
	}
	var events int
	if err := db.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND type = 'run.state'`, string(snap.RunID)).Scan(&events); err != nil {
		t.Fatalf("读 run_events: %v", err)
	}
	if events == 0 {
		t.Fatalf("事件日志里没有 run.state 记录：事件订阅没有接上")
	}

	// 诊断导出里应能看到这个 run 的事件 ID（故障可定位）。
	export := d.ExportDiagnostics(DiagnosticsExportRequest{RunIDs: []string{string(snap.RunID)}})
	if export.Error != nil {
		t.Fatalf("ExportDiagnostics: %+v", export.Error)
	}
	raw, err := os.ReadFile(export.Path)
	if err != nil {
		t.Fatalf("读诊断包: %v", err)
	}
	if !strings.Contains(string(raw), string(snap.RunID)+"-") {
		t.Fatalf("诊断包里看不到事件 ID：\n%s", raw)
	}
}
