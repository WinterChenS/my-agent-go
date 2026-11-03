package pi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/fixtures/openaisse"
)

// TestWorkerWithRealPi 对应 F03 的最后一条：真实 Pi 1.5.0 + 受控 provider 跑一次
// 完整的 Call/Events/Close 链路，验证 worker 层在真实进程上成立，而不只是在 fakeworker 上成立。
//
// 需要显式开启：MYAGENT_PI_WORKER_INTEGRATION=1。
// provider 是 fixture（openaisse），不是真实云模型；本用例不证明模型侧行为。
func TestWorkerWithRealPi(t *testing.T) {
	if os.Getenv("MYAGENT_PI_WORKER_INTEGRATION") != "1" {
		t.Skip("设置 MYAGENT_PI_WORKER_INTEGRATION=1 才跑真实 Pi 的 worker 集成")
	}
	if runtime.GOOS != "windows" {
		t.Skip("当前集成脚本按 Windows 路径编写")
	}

	root := repoRoot(t)
	node := captureNodePath(t)
	cli := piCLIPath(t, root)

	srv := httptest.NewServer(openaisse.NewHandler(""))
	defer srv.Close()

	work := t.TempDir()
	agentDir := filepath.Join(work, "agent")
	writeFixtureModels(t, agentDir, srv.URL)

	w, err := StartWorker(context.Background(), WorkerSpec{
		Installation: PiInstallation{
			Executable:   node,
			Args:         []string{cli, "--mode", "rpc", "--session-dir", filepath.Join(work, "sessions"), "--no-extensions", "--provider", "fixture", "--model", "fixture-model"},
			Origin:       OriginExternal,
			Version:      "1.5.0",
			Architecture: "amd64",
			Runnable:     true,
		},
		WorkingDir: work,
		AgentDir:   agentDir,
		Env:        []string{"PI_OFFLINE=1"},
		// 这里刻意保留默认 CloseGrace（800ms）：它来自 F02 的崩溃对照实验，
		// 本用例同时是"默认宽限能让真实 Pi 正常退出"的回归验证。
		KillTimeout:  5 * time.Second,
		CleanupLimit: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("启动真实 Pi worker 失败: %v", err)
	}

	settledCh := make(chan RawEvent, 4)
	var mu sync.Mutex
	var events []RawEvent
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range w.Events() {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			if ev.Type == TypeAgentSettled {
				select {
				case settledCh <- ev:
				default:
				}
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1) get_state：验证命令 id 由 worker 分配（调用方传的 id 会被覆盖）。
	stateRaw, err := w.Call(ctx, json.RawMessage(`{"id":"caller-supplied","type":"get_state"}`))
	if err != nil {
		t.Fatalf("get_state 失败: %v", err)
	}
	var stateResp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(stateRaw, &stateResp); err != nil {
		t.Fatalf("get_state 响应不是 JSON: %v", err)
	}
	if stateResp.ID != "myagent-1" {
		t.Fatalf("命令 id 应由 worker 分配为 myagent-1，实际 %q（不能沿用调用方 id）", stateResp.ID)
	}
	if stateResp.Command != "get_state" {
		t.Fatalf("响应 command 应为 get_state，实际 %q", stateResp.Command)
	}

	// 2) prompt：真实触发一轮 agent loop。
	promptRaw, err := w.Call(ctx, json.RawMessage(`{"type":"prompt","message":"请回答：[[scenario:text]]"}`))
	if err != nil {
		t.Fatalf("prompt 失败: %v", err)
	}
	var promptResp struct {
		ID   string `json:"id"`
		Data struct {
			Disposition string `json:"disposition"`
		} `json:"data"`
	}
	_ = json.Unmarshal(promptRaw, &promptResp)
	if promptResp.Data.Disposition != "started" {
		t.Fatalf("prompt 的 disposition 应为 started，实际 %q（原文 %s）",
			promptResp.Data.Disposition, clip(string(promptRaw), 300))
	}

	// 3) 等最终 idle。accepted 不等于完成，必须等到 agent_settled。
	var settled RawEvent
	select {
	case settled = <-settledCh:
	case <-time.After(45 * time.Second):
		t.Fatalf("45 秒内没有收到 agent_settled，无法确认已完成；已有事件：%s", summarizeEvents(snapshotEvents(&mu, &events)))
	}

	outcome := ReadSettledOutcome(settled)
	if !outcome.IsCompleted() {
		t.Fatalf("agent_settled 应判 completed（aborted=%v）", outcome.Aborted)
	}

	// 4) 有序关闭：关 stdin 后 Pi 应自行退出，退出码 0，不能记 CleanupFailed。
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer closeCancel()
	if err := w.Close(closeCtx); err != nil {
		t.Fatalf("Close 失败（真实 Pi 未有序退出或进程树未清空）: %v", err)
	}

	<-eventsDone
	evs := snapshotEvents(&mu, &events)
	var sawDelta bool
	for _, ev := range evs {
		if strings.Contains(string(ev.Record), `"text_delta"`) {
			sawDelta = true
			break
		}
	}
	if !sawDelta {
		t.Fatalf("未观察到 text_delta，事件流可能是空的；实际：%s", summarizeEvents(evs))
	}

	code, known := w.ExitCode()
	if !known {
		t.Fatal("Close 后应能拿到退出码")
	}
	if code != 0 {
		t.Fatalf("真实 Pi 有序退出码应为 0，实际 %d（stderr 尾部：%s）", code, stderrTailString(w))
	}

	t.Logf("事件 %d 条，退出码 %d，ClosGrace 默认 800ms，限制：%v",
		len(evs), code, w.Limitations())
}

func snapshotEvents(mu *sync.Mutex, events *[]RawEvent) []RawEvent {
	mu.Lock()
	defer mu.Unlock()
	out := make([]RawEvent, len(*events))
	copy(out, *events)
	return out
}

func stderrTailString(w Worker) string {
	tail, truncated := w.StderrTail()
	if truncated {
		tail = "…(截断)…" + tail
	}
	return clip(tail, 500)
}
