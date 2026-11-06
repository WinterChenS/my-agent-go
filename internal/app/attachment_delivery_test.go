package app

// C06f：附件投递的装配级验证（真 storage + 真 blob store + 真 broker）。
//
// internal/run 的测试用替身 reader 钉住 broker 自己的责任；这里把链路接起来：
// 用户通过原生选择器选文件（Go 只收路径）→ blob 落地 → run 携带 capability →
// worker 用环境里的端点取回**同一份字节**。随后换掉 blob，再取一次必须 fail closed。
//
// 全部在 t.TempDir() 里跑：不碰真实 appData、~/.pi、PATH。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
	"my-agent-go/internal/session"
)

// attachmentWorker 在收到 prompt 后用环境变量里的端点取附件，并记下结果。
type attachmentWorker struct {
	events chan pi.RawEvent
	once   sync.Once

	mu       sync.Mutex
	env      []string
	result   []brokerFetch
	closable bool
	done     chan struct{}
}

// brokerFetch 是一次取数的结果。
type brokerFetch struct {
	Status int
	Bytes  int
	SHA256 string
	// ErrorCode 是失败信封里的错误码（成功为空）。
	ErrorCode string
	// ContentType 用来区分"附件字节"与"错误信封"。
	ContentType string
}

func newAttachmentWorker() *attachmentWorker {
	return &attachmentWorker{events: make(chan pi.RawEvent, 8), done: make(chan struct{})}
}

func (w *attachmentWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	var cmd struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(command, &cmd)
	if cmd.Type == "prompt" {
		go w.fetch()
	}
	return json.RawMessage(`{"type":"response","success":true}`), nil
}

func (w *attachmentWorker) fetch() {
	url, token, sid := w.envValue("ANVIL_ATTACHMENT_URL"), w.envValue("ANVIL_ATTACHMENT_TOKEN"), w.envValue("ANVIL_ATTACHMENT_SESSION")

	// 清单
	listReq, _ := http.NewRequest(http.MethodGet, url+"/v1/attachments", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listReq.Header.Set("X-Anvil-Session", sid)
	var ids []string
	if resp, err := http.DefaultClient.Do(listReq); err == nil {
		var body struct {
			Attachments []struct {
				ID string `json:"id"`
			} `json:"attachments"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		for _, it := range body.Attachments {
			ids = append(ids, it.ID)
		}
	}

	for _, id := range ids {
		req, _ := http.NewRequest(http.MethodGet, url+"/v1/attachments/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Anvil-Session", sid)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.record(brokerFetch{ErrorCode: "transport: " + err.Error()})
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		sum := sha256.Sum256(body)
		fetch := brokerFetch{
			Status: resp.StatusCode, Bytes: len(body), ContentType: resp.Header.Get("Content-Type"),
		}
		if resp.StatusCode == http.StatusOK {
			fetch.SHA256 = hex.EncodeToString(sum[:])
		} else {
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(body, &env)
			fetch.ErrorCode = env.Error.Code
		}
		w.record(fetch)
	}

	w.push(pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)})
}

func (w *attachmentWorker) envValue(key string) string {
	for _, kv := range w.env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return ""
}

func (w *attachmentWorker) record(f brokerFetch) {
	w.mu.Lock()
	w.result = append(w.result, f)
	w.mu.Unlock()
}

func (w *attachmentWorker) fetches() []brokerFetch {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]brokerFetch{}, w.result...)
}

func (w *attachmentWorker) push(ev pi.RawEvent) {
	// Close 与推送可能并发（清理顺序由测试清理期决定），替身不该因此 panic。
	defer func() { _ = recover() }()
	select {
	case <-w.done:
		return
	default:
	}
	select {
	case w.events <- ev:
	case <-w.done:
	case <-time.After(5 * time.Second):
	}
}

func (w *attachmentWorker) Events() <-chan pi.RawEvent { return w.events }

func (w *attachmentWorker) Close(ctx context.Context) error {
	w.once.Do(func() {
		close(w.done)
		close(w.events)
	})
	return nil
}

func (w *attachmentWorker) PID() int                   { return 0 }
func (w *attachmentWorker) StderrTail() (string, bool) { return "", false }
func (w *attachmentWorker) ExitCode() (int, bool)      { return 0, false }
func (w *attachmentWorker) Limitations() []string      { return nil }

// productExtensionsForTest 把产品扩展指到仓库里的真文件（测试进程的 cwd 是
// internal/app，默认定位找不到仓库根下的 pi-extensions）。
func productExtensionsForTest(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录: %v", err)
	}
	repo := filepath.Clean(filepath.Join(wd, "..", ".."))
	for _, name := range []string{"anvil-clarification.ts", "anvil-attachment.ts"} {
		path := filepath.Join(repo, "pi-extensions", name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("找不到产品扩展 %s: %v", path, err)
		}
		switch name {
		case "anvil-clarification.ts":
			t.Setenv("ANVIL_CLARIFICATION_EXT", path)
		case "anvil-attachment.ts":
			t.Setenv("ANVIL_ATTACHMENT_EXT", path)
		}
	}
}

// waitRunTerminal 等到 run 进入终态。
func waitRunTerminal(t *testing.T, svc *run.Service, id contracts.RunID) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	state := ""
	for time.Now().Before(deadline) {
		snap, err := svc.Get(id)
		if err == nil {
			state = snap.State
			if snap.State == contracts.RunStateCompleted || snap.State == contracts.RunStateFailed ||
				snap.State == contracts.RunStateCancelled || snap.State == contracts.RunStateInterrupted {
				return state
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run 没有在时限内进入终态（当前 %q）", state)
	return state
}

// TestAttachmentBytesReachRunThroughBroker 覆盖装配后的完整链路。
func TestAttachmentBytesReachRunThroughBroker(t *testing.T) {
	productExtensionsForTest(t)
	d := prepareRuntimeDesktop(t)
	work := d.Settings().Workspace

	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s: %s", ws.Error.Code, ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	sess := d.CreateSession("")
	if sess.Error != nil {
		t.Fatalf("创建会话失败: %s: %s", sess.Error.Code, sess.Error.Message)
	}

	// 用户选中的文件：放在工作区**之外**（普通会话不授予用户目录的持续访问，
	// 附件是用户显式选择后的快照）。
	body := []byte("附件正文：必须原样到达模型。\n第二行\n")
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "notes.txt")
	if err := os.WriteFile(src, body, 0o600); err != nil {
		t.Fatalf("写源文件: %v", err)
	}
	resolved := d.ResolveAttachments(string(sess.Item.ID), []session.AttachmentRequest{{Path: src}})
	if resolved.Error != nil {
		t.Fatalf("接收附件失败: %s: %s", resolved.Error.Code, resolved.Error.Message)
	}
	if len(resolved.Items) != 1 {
		t.Fatalf("应接收 1 个附件，实际 %d（拒绝项 %+v）", len(resolved.Items), resolved.Rejected)
	}
	item := resolved.Items[0]

	// 每次启动 worker 都造一个新的替身：worker 实例在 Close 之后不可复用，
	// 复用会让第二轮直接看到已关闭的事件流（那不是被测行为）。
	var (
		workersMu sync.Mutex
		workers   []*attachmentWorker
	)
	d.mu.Lock()
	d.workerStarter = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w := newAttachmentWorker()
		w.mu.Lock()
		w.env = append([]string{}, spec.Env...)
		w.mu.Unlock()
		workersMu.Lock()
		workers = append(workers, w)
		workersMu.Unlock()
		return w, nil
	}
	d.mu.Unlock()
	workerAt := func(i int) *attachmentWorker {
		t.Helper()
		workersMu.Lock()
		defer workersMu.Unlock()
		if i >= len(workers) {
			t.Fatalf("第 %d 个 worker 不存在（共 %d 个）", i, len(workers))
		}
		return workers[i]
	}

	snap, err := d.StartSessionRun(string(sess.Item.ID), "请读这个附件", []string{item.ID})
	if err != nil {
		t.Fatalf("提交带附件的 run 失败: %v", err)
	}
	svc := mustRuntime(t, d)
	if state := waitRunTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("run 应 completed，实际 %s", state)
	}

	fetches := workerAt(0).fetches()
	if len(fetches) != 1 {
		t.Fatalf("worker 应取到 1 个附件，实际 %d 次：%+v", len(fetches), fetches)
	}
	if fetches[0].Status != http.StatusOK {
		t.Fatalf("取附件应 200，实际 %d（%+v）", fetches[0].Status, fetches[0])
	}
	if fetches[0].Bytes != len(body) {
		t.Fatalf("字节长度应为 %d，实际 %d", len(body), fetches[0].Bytes)
	}
	if fetches[0].SHA256 != item.SHA256 {
		t.Fatalf("worker 看到的 sha256 应与登记的元数据一致：期望 %s，实际 %s", item.SHA256, fetches[0].SHA256)
	}

	// 源文件必须原样（不回写）。
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读回源文件失败: %v", err)
	}
	if string(after) != string(body) {
		t.Fatal("源文件被回写了：附件接收必须是只读快照")
	}

	// ── 换掉 blob：必须 fail closed ──
	store, err := d.attachmentStore()
	if err != nil {
		t.Fatalf("取附件存储: %v", err)
	}
	blobPath := store.BlobPath(item.SHA256)
	if err := os.WriteFile(blobPath, []byte(strings.Repeat("x", len(body))), 0o600); err != nil {
		t.Fatalf("替换 blob: %v", err)
	}

	// 用同一个 capability 再跑一轮：blob 已被破坏，交付必须 fail closed。
	svc2 := mustRuntime(t, d)
	snap2, err := d.StartSessionRun(string(sess.Item.ID), "再读一次", []string{item.ID})
	if err != nil {
		t.Fatalf("第二次提交失败: %v", err)
	}
	if state := waitRunTerminal(t, svc2, snap2.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("第二次 run 应 completed（读不到附件不等于 run 失败），实际 %s", state)
	}
	fetches2 := workerAt(1).fetches()
	if len(fetches2) != 1 {
		t.Fatalf("第二次应取 1 次，实际 %d：%+v", len(fetches2), fetches2)
	}
	if fetches2[0].Status == http.StatusOK {
		t.Fatal("blob 被替换后仍然交付了字节")
	}
	if fetches2[0].SHA256 != "" {
		t.Fatal("失败响应不该带内容摘要")
	}
	if fetches2[0].ErrorCode == "" {
		t.Fatalf("失败响应应带可定位的错误码，实际 %+v", fetches2[0])
	}
	if !strings.Contains(fetches2[0].ContentType, "application/json") {
		t.Fatalf("失败响应应是结构化错误，实际 %q", fetches2[0].ContentType)
	}
}

// TestStartSessionRunRejectsUnknownAttachmentID 守"ID 必须仍然有效"。
func TestStartSessionRunRejectsUnknownAttachmentID(t *testing.T) {
	productExtensionsForTest(t)
	d := prepareRuntimeDesktop(t)
	work := d.Settings().Workspace
	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s", ws.Error.Code)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s", res.Error.Code)
	}
	sess := d.CreateSession("")
	if sess.Error != nil {
		t.Fatalf("创建会话失败: %s", sess.Error.Code)
	}

	_, err := d.StartSessionRun(string(sess.Item.ID), "读附件", []string{"att-does-not-exist"})
	if err == nil {
		t.Fatal("无效的附件 ID 被接受了：run 会带着一个永远读不到的引用跑起来")
	}
	if !strings.Contains(err.Error(), "附件") {
		t.Fatalf("错误应指明是附件问题，实际 %v", err)
	}
}
