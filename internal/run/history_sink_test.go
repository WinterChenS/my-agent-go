package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

// recordingSink 是一个只记录快照的 HistorySink，可以按需让每次写入失败。
type recordingSink struct {
	mu      sync.Mutex
	seen    []contracts.RunSnapshot
	failErr error
}

func (s *recordingSink) RecordRun(ctx context.Context, snap contracts.RunSnapshot) error {
	s.mu.Lock()
	s.seen = append(s.seen, snap)
	err := s.failErr
	s.mu.Unlock()
	return err
}

func (s *recordingSink) snapshots() []contracts.RunSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contracts.RunSnapshot, len(s.seen))
	copy(out, s.seen)
	return out
}

// TestRunStateChangesReachHistorySink 覆盖 RUN07 的索引接线：
// 创建时（starting）与每次状态迁移后都要把**新**状态与 seq 交给 sink，
// 且 sink 失败只产生诊断，不把 run 判成失败。
func TestRunStateChangesReachHistorySink(t *testing.T) {
	sink := &recordingSink{}
	w := newFakeWorker()
	opts := testOptions(t)
	opts.History = sink
	svc, snap := runWithWorker(t, opts, w)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	sendRaw(w, "agent_settled", `{"type":"agent_settled","aborted":false}`)
	if !eventually(t, 2*time.Second, func() bool {
		return mustRunState(t, svc, snap.RunID) == contracts.RunStateCompleted
	}) {
		t.Fatalf("run 未在 2 秒内完成，当前 %s", mustRunState(t, svc, snap.RunID))
	}

	seen := sink.snapshots()
	if len(seen) == 0 {
		t.Fatalf("HistorySink 一次都没有被调用（RUN07 要求状态变化落到索引）")
	}
	if seen[0].State != contracts.RunStateStarting {
		t.Fatalf("第一次写入应是创建快照 starting，实际 %q", seen[0].State)
	}
	var states []string
	for _, s := range seen {
		states = append(states, s.State)
	}
	want := []string{contracts.RunStateStarting, contracts.RunStateRunning, contracts.RunStateCompleted}
	if len(states) != len(want) {
		t.Fatalf("状态序列应为 %v，实际 %v", want, states)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("状态序列应为 %v，实际 %v", want, states)
		}
	}
	// 迁移**之后**才写：sink 看到的 seq 必须已经包含这次迁移的事件。
	for i := 1; i < len(seen); i++ {
		if seen[i].LastSeq < seen[i-1].LastSeq {
			t.Fatalf("sink 看到的 last_seq 倒退：%d → %d", seen[i-1].LastSeq, seen[i].LastSeq)
		}
	}
	if seen[len(seen)-1].LastSeq == 0 {
		t.Fatalf("终态快照的 last_seq 不应为 0")
	}
	if seen[len(seen)-1].RunID != snap.RunID {
		t.Fatalf("sink 收到的 run id 不一致：%q vs %q", seen[len(seen)-1].RunID, snap.RunID)
	}

	// sink 失败不能让 run 失败：run 照常完成，只多一条诊断。
	failing := &recordingSink{failErr: errors.New("索引写盘失败")}
	w2 := newFakeWorker()
	opts2 := testOptions(t)
	opts2.History = failing
	svc2, snap2 := runWithWorker(t, opts2, w2)
	t.Cleanup(func() { _ = svc2.Close(context.Background()) })

	sendRaw(w2, "agent_settled", `{"type":"agent_settled","aborted":false}`)
	if !eventually(t, 2*time.Second, func() bool {
		return mustRunState(t, svc2, snap2.RunID) == contracts.RunStateCompleted
	}) {
		t.Fatalf("索引写入失败时 run 仍应完成，当前 %s", mustRunState(t, svc2, snap2.RunID))
	}
	if !hasDiagnosticKind(t, svc2, snap2.RunID, "history.record_failed") {
		t.Fatalf("索引写入失败必须留下 history.record_failed 诊断")
	}
	if errs := eventsOfType(t, svc2, snap2.RunID, EventRunError); len(errs) != 0 {
		t.Fatalf("索引失败不应产生 run.error 事件（它不是 run 失败），实际 %d 条", len(errs))
	}
}
