package app

// run 索引与诊断记录的复合 sink 测试。
//
// 背景：run.Options.History 只有一个槽位。C07 把诊断记录器装进去之后，history.Service
// 就没有写者了——真实运行过的会话在历史列表里查不到、transcript 定位也找不到。
// 这些测试钉住"两份都要落库"，以及"一份坏了不把另一份一起判死"。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

type fakeSink struct {
	name    string
	calls   int
	lastRun contracts.RunID
	err     error
}

func (f *fakeSink) RecordRun(_ context.Context, snap contracts.RunSnapshot) error {
	f.calls++
	f.lastRun = snap.RunID
	return f.err
}

func TestRunRecorderWritesEverySink(t *testing.T) {
	a := &fakeSink{name: "history"}
	b := &fakeSink{name: "diagnostics"}
	rec := newRunRecorder(namedSink{name: "history", sink: a}, namedSink{name: "diagnostics", sink: b})
	if rec == nil {
		t.Fatal("两个 sink 都在时不应返回 nil")
	}
	if err := rec.RecordRun(context.Background(), contracts.RunSnapshot{RunID: "run-1"}); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	for _, s := range []*fakeSink{a, b} {
		if s.calls != 1 || s.lastRun != "run-1" {
			t.Fatalf("%s 收到 %d 次（run=%q），期望 1 次 run-1", s.name, s.calls, s.lastRun)
		}
	}
}

func TestRunRecorderToleratesOneBrokenSink(t *testing.T) {
	broken := &fakeSink{name: "history", err: errors.New("索引库坏了")}
	ok := &fakeSink{name: "diagnostics"}
	rec := newRunRecorder(namedSink{name: "history", sink: broken}, namedSink{name: "diagnostics", sink: ok})
	if err := rec.RecordRun(context.Background(), contracts.RunSnapshot{RunID: "run-2"}); err != nil {
		t.Fatalf("有其它 sink 成功时不应整体失败: %v", err)
	}
	if ok.calls != 1 {
		t.Fatalf("诊断 sink 应照常写入，实际 %d 次", ok.calls)
	}
}

func TestRunRecorderFailsOnlyWhenEverySinkFails(t *testing.T) {
	rec := newRunRecorder(
		namedSink{name: "history", sink: &fakeSink{err: errors.New("a")}},
		namedSink{name: "diagnostics", sink: &fakeSink{err: errors.New("b")}},
	)
	if err := rec.RecordRun(context.Background(), contracts.RunSnapshot{RunID: "run-3"}); err == nil {
		t.Fatal("全部 sink 失败时必须返回错误（否则上层会以为记录已落地）")
	}
}

func TestRunRecorderIgnoresMissingSinks(t *testing.T) {
	if rec := newRunRecorder(namedSink{name: "history", sink: nil}); rec != nil {
		t.Fatalf("没有任何可用 sink 时应返回 nil（等价于不记录），实际 %s", rec)
	}
	var rec *runRecorder
	if err := rec.RecordRun(context.Background(), contracts.RunSnapshot{}); err != nil {
		t.Fatalf("nil recorder 应安全: %v", err)
	}
}

// TestRuntimeWritesBothRunIndexAndUsage 是复合 sink 的端到端证据。
//
// 一轮真实（fakeworker）run 结束后，同一个 sessions.db 里必须同时有：
//   - runs 行（history.Service 的索引，历史列表与 transcript 定位靠它）；
//   - run_usage 行（诊断记录器，usage/预算/失败结论靠它）。
//
// 只有其中一个 = 装配又回到了单槽位时代。
func TestRuntimeWritesBothRunIndexAndUsage(t *testing.T) {
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

	snap, err := d.Start("这一轮要同时写进索引与诊断")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	worker.events <- pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)}

	db, err := d.indexDB()
	if err != nil {
		t.Fatalf("indexDB: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var indexRows, usageRows int
	for time.Now().Before(deadline) {
		indexRows, usageRows = 0, 0
		if err := db.SQL().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM runs WHERE id = ?`, string(snap.RunID)).Scan(&indexRows); err != nil {
			t.Fatalf("读 runs: %v", err)
		}
		if err := db.SQL().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM run_usage WHERE run_id = ?`, string(snap.RunID)).Scan(&usageRows); err != nil {
			t.Fatalf("读 run_usage: %v", err)
		}
		if indexRows > 0 && usageRows > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if indexRows == 0 {
		t.Fatal("runs 索引里没有这一轮：history sink 没有接上（会话历史会查不到）")
	}
	if usageRows == 0 {
		t.Fatal("run_usage 里没有这一轮：诊断记录器没有接上")
	}
}
