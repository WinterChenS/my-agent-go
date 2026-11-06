package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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

	// PI03 的外部观察（http-server）：fixture provider **在 HTTP handler 里**把收到的
	// 第一个请求体写进磁盘；不是测试事后拼出来的。测试只读回那个文件。
	//
	// frames.jsonl 记录的是产品事件流里的**原始记录行**（由 events goroutine 逐条落盘），
	// 用于与已提交的 golden 捕获做骨架比对。
	obsDir := piObservationDir()
	requestPath := ""
	framesPath := ""
	if obsDir != "" {
		if err := os.MkdirAll(obsDir, 0o755); err != nil {
			t.Fatalf("创建观察目录失败: %v", err)
		}
		requestPath = filepath.Clean(filepath.Join(obsDir, "provider-request.json"))
		framesPath = filepath.Clean(filepath.Join(obsDir, "frames.jsonl"))
		_ = os.Remove(requestPath)
		_ = os.Remove(framesPath)
	}

	inner := openaisse.NewHandler(filepath.Join(obsDir, "provider-requests"))
	var handler http.Handler = inner
	if obsDir != "" {
		var once sync.Once
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/chat/completions" {
				body, readErr := io.ReadAll(r.Body)
				if readErr == nil {
					r.Body = io.NopCloser(bytes.NewReader(body))
					once.Do(func() { _ = os.WriteFile(requestPath, body, 0o644) })
				}
			}
			inner.ServeHTTP(w, r)
		})
	}
	srv := httptest.NewServer(handler)
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
		var frames *os.File
		if framesPath != "" {
			// 记录的是产品事件流里的原始记录行；写失败不阻断测试，但会让后面的读回失败。
			frames, _ = os.OpenFile(framesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if frames != nil {
				defer func() { _ = frames.Close() }()
			}
		}
		for ev := range w.Events() {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			if frames != nil {
				_, _ = frames.Write(append(append([]byte(nil), ev.Record...), '\n'))
			}
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

	if obsDir == "" {
		return // 本地 focused 运行：不落盘
	}

	// ── 从磁盘读回外部观察值 ──
	reqRaw, rerr := os.ReadFile(requestPath)
	if rerr != nil {
		t.Fatalf("读回 fixture provider 自己写下的请求体失败: %v", rerr)
	}
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqRaw, &req); err != nil {
		t.Fatalf("provider 收到的请求体不是合法 JSON: %v", err)
	}
	promptBytes := 0
	for _, m := range req.Messages {
		if m.Role == "user" {
			promptBytes = len(m.Content)
		}
	}
	if promptBytes == 0 {
		t.Fatalf("provider 收到的请求里没有 user 消息：Pi 没有把 prompt 发出去（原文 %s）", clip(string(reqRaw), 300))
	}

	framesRaw, ferr := os.ReadFile(framesPath)
	if ferr != nil {
		t.Fatalf("读回产品事件流落盘文件失败: %v", ferr)
	}
	capturedLines := nonEmptyLines(string(framesRaw))
	if len(capturedLines) == 0 {
		t.Fatal("frames.jsonl 为空：没有捕获到任何协议记录")
	}
	// response 记录由 Worker 直接交给等待中的 Call（不进事件流），
	// 而 golden 是原始 stdout 捕获（含 response）。比对事件骨架时两边都去掉它，
	// 剩下的才是"agent 事件序列"这一层；response 的正确归属由上面的 Call 断言负责。
	capturedSkeleton := filterOut(protocolSkeleton(t, capturedLines), "response")

	// golden 是已提交的真实 Pi 1.5.0 捕获；这里只从磁盘读，不写回。
	goldenPath := filepath.Join("testdata", "v1.5.0", "text.jsonl")
	goldenRaw, gerr := os.ReadFile(goldenPath)
	if gerr != nil {
		t.Fatalf("读取 golden 捕获失败: %v", gerr)
	}
	goldenSkeleton := filterOut(protocolSkeleton(t, nonEmptyLines(string(goldenRaw))), "response")
	if err := containsSubsequence(capturedSkeleton, goldenSkeleton); err != nil {
		t.Fatalf("本次真实 Pi 的事件骨架与 golden 不一致：%v / 捕获：%v / golden：%v", err, capturedSkeleton, goldenSkeleton)
	}

	writePiObservation(t, map[string]any{
		"promptBytes":           promptBytes,
		"model":                 req.Model,
		"streamed":              req.Stream,
		"requestBytes":          len(reqRaw),
		"framesCaptured":        len(capturedLines),
		"capturedSkeleton":      capturedSkeleton,
		"goldenFile":            goldenPath,
		"goldenSha256":          sha256Of(goldenRaw),
		"goldenSkeleton":        goldenSkeleton,
		"goldenSkeletonInLive":  true,
		"piExitCode":            code,
		"workerEvents":          len(evs),
		"providerRequestSource": "httptest handler 内部写盘",
	}, nil)
}

// nonEmptyLines 去掉空行（协议记录一行一条，空行不是记录）。
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// protocolSkeleton 把记录行压成"事件类型序列"，去掉 system/user 消息的 start/end，
// 只留协议骨架。这样不同进程产出的记录（id/时间戳不同）仍可比较。
func protocolSkeleton(t *testing.T, lines []string) []string {
	t.Helper()
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("记录不是合法 JSON: %v（原文 %s）", err, clip(line, 200))
		}
		typ, _ := m["type"].(string)
		if typ == "message_start" || typ == "message_end" {
			continue
		}
		if ev, ok := m["assistantMessageEvent"].(map[string]any); ok {
			if sub, ok := ev["type"].(string); ok {
				typ = typ + "/" + sub
			}
		}
		out = append(out, typ)
	}
	return out
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
