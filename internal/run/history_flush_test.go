package run

// Close 必须等到索引写完才能返回。
//
// 背景（2026-11-06 实测）：终态迁移是"更新快照 → 写索引"两步。调用方看到终态就
// 关数据库时，仍在路上的那笔写会失败，索引停在 running；重启后 RUN07 偶发看到
// [completed failed] 与 [completed running] 并存。这条测试把"Close 会等"钉死。

import (
	"context"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

// blockingSink 在写入终态时阻塞，直到测试放行。
type blockingSink struct {
	mu        sync.Mutex
	states    []string
	blockOn   string
	gate      chan struct{}
	entered   chan struct{}
	enterOnce sync.Once
}

func (b *blockingSink) RecordRun(_ context.Context, snap contracts.RunSnapshot) error {
	b.mu.Lock()
	b.states = append(b.states, snap.State)
	b.mu.Unlock()
	if snap.State == b.blockOn {
		b.enterOnce.Do(func() { close(b.entered) })
		<-b.gate
	}
	return nil
}

func (b *blockingSink) last() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.states) == 0 {
		return ""
	}
	return b.states[len(b.states)-1]
}

func TestCloseWaitsForTheFinalHistoryWrite(t *testing.T) {
	sink := &blockingSink{
		blockOn: contracts.RunStateCompleted,
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
	}
	w := newFakeWorker()
	opts := testOptions(t)
	opts.History = sink
	svc, snap := runWithWorker(t, opts, w)
	// 驱动到终态：worker 报 stop → 结算 → transition(completed) → recordHistory 阻塞。
	sendRaw(w, "message_end", `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"stop"}}`)
	sendRaw(w, "agent_settled", `{"type":"agent_settled","aborted":false}`)

	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("终态索引写入没有开始（测试前提不成立）")
	}

	// 此刻快照已是终态，但写还没完成。Close 必须等，而不是立刻返回。
	done := make(chan error, 1)
	go func() { done <- svc.Close(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("索引还在写的时候 Close 就返回了（err=%v）：调用方会关掉数据库，终态丢失", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(sink.gate) // 放行写入
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("放行写入后 Close 没有返回")
	}
	if got := sink.last(); got != contracts.RunStateCompleted {
		t.Fatalf("Close 返回时最后一笔写入是 %q，期望 %q（run %s）", got, contracts.RunStateCompleted, snap.RunID)
	}
}
