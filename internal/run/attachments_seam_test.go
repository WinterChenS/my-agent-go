package run

// C06f：附件投递 seam 的失败测试。
//
// 这一组回答四个问题，每个都以"worker 自己取到的东西"或"请求被拒绝的事实"作证：
//  1. 带附件的 run 能不能真的读到**同一份字节**（长度 + sha256 都对得上）；
//  2. 内容被换掉（tamper）时是不是一个字节都不交出去；
//  3. prompt / argv / 环境里有没有出现任何附件路径（只允许 capability id 与端点）；
//  4. 端点是不是随 run 一起关闭（run 结束后再取只能失败）。
//
// 纪律：全部在 t.TempDir() 里跑；不碰真实 appData、~/.pi、PATH。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// fakeAttachmentReader 是 AttachmentReader 的替身。
//
// tamper=true 时返回**长度相同但内容不同**的字节：这比"返回错误"更能证明
// broker 自己在交付前又对了一次冻结的 sha256——否则这类替换会被当成正常内容。
type fakeAttachmentReader struct {
	blobRoot string
	bytes    map[string][]byte
	tamper   bool

	mu    sync.Mutex
	opens []string
}

func (f *fakeAttachmentReader) OpenAttachment(id string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opens = append(f.opens, id)
	tamper := f.tamper
	f.mu.Unlock()

	body, ok := f.bytes[id]
	if !ok {
		return nil, contracts.NewAppError(contracts.CodeNotFound, "附件不存在: "+id, false)
	}
	if !tamper {
		return io.NopCloser(strings.NewReader(string(body))), nil
	}
	// 同长度替换：只改内容，不改大小，专门验证 sha256 这一道。
	replaced := make([]byte, len(body))
	copy(replaced, body)
	for i := range replaced {
		replaced[i] = replaced[i] ^ 0xFF
	}
	return io.NopCloser(strings.NewReader(string(replaced))), nil
}

func (f *fakeAttachmentReader) opened() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.opens...)
}

// fetchedAttachment 是 fetchingWorker 取到的一份内容。
type fetchedAttachment struct {
	ID     string
	Status int
	Bytes  int
	Body   []byte
	SHA256 string
	// ContentType / DeliveredSHA 用来区分"附件字节"与"错误信封"：
	// fail-closed 时端点会回一个结构化错误（可定位），但不会回附件内容。
	ContentType  string
	DeliveredSHA string
	Listed       []string
	Err          string
}

// fetchingWorker 在收到 prompt 后**真的**用环境变量里的 URL/令牌去取附件，
// 然后把自己取到的结果记下来——"模型能不能看到这些字节"由它的记录作证。
type fetchingWorker struct {
	events chan pi.RawEvent
	once   sync.Once

	mu      sync.Mutex
	calls   []json.RawMessage
	fetched []fetchedAttachment
	env     []string
	args    []string

	done chan struct{}
}

func newFetchingWorker() *fetchingWorker {
	return &fetchingWorker{events: make(chan pi.RawEvent, 16), done: make(chan struct{})}
}

func (w *fetchingWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	w.mu.Lock()
	w.calls = append(w.calls, command)
	w.mu.Unlock()
	var cmd struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(command, &cmd)
	if cmd.Type == "prompt" {
		go w.fetchAll()
	}
	return json.RawMessage(`{"type":"response","success":true}`), nil
}

func (w *fetchingWorker) fetchAll() {
	url := w.envValue(EnvAttachmentURL)
	token := w.envValue(EnvAttachmentToken)
	session := w.envValue(EnvAttachmentSession)

	// 1) 列清单
	list := fetchedAttachment{}
	if url == "" || token == "" || session == "" {
		list.Err = "环境里缺少附件端点/令牌/会话"
	} else {
		req, _ := http.NewRequest(http.MethodGet, url+"/v1/attachments", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Anvil-Session", session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			list.Err = err.Error()
		} else {
			list.Status = resp.StatusCode
			var body struct {
				Attachments []struct {
					ID string `json:"id"`
				} `json:"attachments"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			for _, it := range body.Attachments {
				list.Listed = append(list.Listed, it.ID)
			}
		}
	}
	w.record(list)

	// 2) 逐个取字节
	for _, id := range list.Listed {
		item := fetchedAttachment{ID: id}
		req, _ := http.NewRequest(http.MethodGet, url+"/v1/attachments/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Anvil-Session", session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			item.Err = err.Error()
			w.record(item)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		item.Status = resp.StatusCode
		item.Bytes = len(body)
		item.Body = body
		item.ContentType = resp.Header.Get("Content-Type")
		item.DeliveredSHA = resp.Header.Get("X-Anvil-SHA256")
		sum := sha256.Sum256(body)
		item.SHA256 = hex.EncodeToString(sum[:])
		w.record(item)
	}

	w.push(pi.RawEvent{Type: pi.TypeAgentSettled, Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`)})
}

func (w *fetchingWorker) envValue(key string) string {
	for _, kv := range w.env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return ""
}

func (w *fetchingWorker) record(item fetchedAttachment) {
	w.mu.Lock()
	w.fetched = append(w.fetched, item)
	w.mu.Unlock()
}

func (w *fetchingWorker) fetchedItems() []fetchedAttachment {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]fetchedAttachment{}, w.fetched...)
}

func (w *fetchingWorker) promptMessage(t *testing.T) string {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, raw := range w.calls {
		var cmd struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &cmd) == nil && cmd.Type == "prompt" {
			return cmd.Message
		}
	}
	t.Fatal("worker 没有收到 prompt")
	return ""
}

func (w *fetchingWorker) push(ev pi.RawEvent) {
	// 同 clarifyingWorker.push：Close 与推送可能并发，替身不该因此 panic。
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

func (w *fetchingWorker) Events() <-chan pi.RawEvent { return w.events }

func (w *fetchingWorker) Close(ctx context.Context) error {
	w.once.Do(func() {
		close(w.done)
		close(w.events)
	})
	return nil
}

func (w *fetchingWorker) PID() int                   { return 0 }
func (w *fetchingWorker) StderrTail() (string, bool) { return "", false }
func (w *fetchingWorker) ExitCode() (int, bool)      { return 0, false }
func (w *fetchingWorker) Limitations() []string      { return nil }

// attachmentRefFor 按内容构造一条冻结的附件引用。
func attachmentRefFor(id, name string, body []byte) AttachmentRef {
	sum := sha256.Sum256(body)
	return AttachmentRef{
		ID:          id,
		DisplayName: name,
		Size:        int64(len(body)),
		SHA256:      hex.EncodeToString(sum[:]),
		MediaType:   "text/plain; charset=utf-8",
	}
}

// TestRunReadsExactAttachmentBytesThroughBroker 覆盖正向投递。
func TestRunReadsExactAttachmentBytesThroughBroker(t *testing.T) {
	dir := t.TempDir()
	body := []byte("附件正文：这三行必须原样到达模型。\nline2\nline3\n")
	ref := attachmentRefFor("att-1", "notes.txt", body)

	reader := &fakeAttachmentReader{
		blobRoot: filepath.Join(dir, "blobs", "secret-location"),
		bytes:    map[string][]byte{"att-1": body},
	}
	if err := os.MkdirAll(reader.blobRoot, 0o700); err != nil {
		t.Fatalf("建 blob 目录: %v", err)
	}

	opts := testOptions(t)
	opts.Attachments = reader
	w := newFetchingWorker()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w.mu.Lock()
		w.env = append([]string{}, spec.Env...)
		w.args = append([]string{}, spec.Args...)
		w.mu.Unlock()
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap, err := svc.Start(context.Background(), StartRunRequest{
		Message:     "请读这个附件",
		Attachments: []AttachmentRef{ref},
	})
	if err != nil {
		t.Fatalf("启动带附件的 run 失败: %v", err)
	}
	if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("run 应正常完成，实际 %s", state)
	}

	items := w.fetchedItems()
	if len(items) < 2 {
		t.Fatalf("worker 只记录了 %d 次取数（应有一次清单 + 一次取字节）：%+v", len(items), items)
	}
	if got := items[0].Listed; len(got) != 1 || got[0] != "att-1" {
		t.Fatalf("清单应只含这 1 个附件，实际 %v（err=%s）", got, items[0].Err)
	}
	got := items[1]
	if got.Err != "" {
		t.Fatalf("取字节失败: %s", got.Err)
	}
	if got.Status != http.StatusOK {
		t.Fatalf("取字节应 200，实际 %d", got.Status)
	}
	if string(got.Body) != string(body) {
		t.Fatalf("字节不一致：期望 %q，实际 %q", string(body), string(got.Body))
	}
	if got.SHA256 != ref.SHA256 {
		t.Fatalf("worker 看到的 sha256 与引用不一致：期望 %s，实际 %s", ref.SHA256, got.SHA256)
	}

	// 路径不得出现在 prompt / argv / 环境里：只有 capability id 与回环端点。
	prompt := w.promptMessage(t)
	if prompt != "请读这个附件" {
		t.Fatalf("prompt 必须原样，不能掺附件信息，实际 %q", prompt)
	}
	blobSentinel := "secret-location"
	for _, surface := range [][]string{w.args, w.env, {prompt}} {
		for _, s := range surface {
			if strings.Contains(s, blobSentinel) {
				t.Fatalf("附件存储路径泄漏到了 worker 可见面：%s", s)
			}
		}
	}
	// 反过来：端点与令牌必须真的经环境交付（否则 Pi 侧取不到）。
	if w.envValue(EnvAttachmentURL) == "" || w.envValue(EnvAttachmentToken) == "" {
		t.Fatal("附件端点/令牌没有经环境变量交付")
	}

	// run 结束后端点必须关闭：拿着同一套凭据再取只能失败。
	url := w.envValue(EnvAttachmentURL)
	token := w.envValue(EnvAttachmentToken)
	session := w.envValue(EnvAttachmentSession)
	req, _ := http.NewRequest(http.MethodGet, url+"/v1/attachments/att-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Anvil-Session", session)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("run 结束后附件端点仍然交付字节：端点没有随 run 关闭")
		}
	}
}

// TestRunFailsClosedWhenAttachmentBytesChanged 覆盖"内容被换掉"。
func TestRunFailsClosedWhenAttachmentBytesChanged(t *testing.T) {
	dir := t.TempDir()
	body := []byte("原始内容")
	ref := attachmentRefFor("att-1", "notes.txt", body)
	reader := &fakeAttachmentReader{
		blobRoot: filepath.Join(dir, "blobs"),
		bytes:    map[string][]byte{"att-1": body},
		tamper:   true,
	}

	opts := testOptions(t)
	opts.Attachments = reader
	w := newFetchingWorker()
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w.mu.Lock()
		w.env = append([]string{}, spec.Env...)
		w.mu.Unlock()
		return w, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap, err := svc.Start(context.Background(), StartRunRequest{
		Message:     "读附件",
		Attachments: []AttachmentRef{ref},
	})
	if err != nil {
		t.Fatalf("启动 run 失败: %v", err)
	}
	if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("附件读取失败不应让 run 失败（模型该被告知读不到），实际 %s", state)
	}

	items := w.fetchedItems()
	if len(items) < 2 {
		t.Fatalf("worker 没有尝试取字节：%+v", items)
	}
	got := items[1]
	if got.Status == http.StatusOK {
		t.Fatal("内容与冻结的 sha256 不一致，却仍然交付成功")
	}
	if got.Status < 400 {
		t.Fatalf("校验失败应给出 4xx/5xx，实际 %d", got.Status)
	}
	// 交付的只能是错误信封：不能是附件内容，也不能带附件的摘要头。
	if string(got.Body) == string(body) {
		t.Fatal("校验失败时仍然把（被替换的）附件内容交了出去")
	}
	if got.DeliveredSHA != "" {
		t.Fatalf("校验失败却带了 X-Anvil-SHA256 头：%s", got.DeliveredSHA)
	}
	if !strings.Contains(got.ContentType, "application/json") {
		t.Fatalf("失败响应应是结构化错误（json），实际 %q", got.ContentType)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got.Body, &envelope); err != nil || envelope.Error.Code == "" {
		t.Fatalf("失败响应缺少可定位的错误码：%q", string(got.Body))
	}
}

// TestRunRejectsInvalidAttachmentRefBeforeStart 守 admission 校验。
func TestRunRejectsInvalidAttachmentRefBeforeStart(t *testing.T) {
	opts := testOptions(t)
	opts.Attachments = &fakeAttachmentReader{bytes: map[string][]byte{}}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	cases := []struct {
		name string
		ref  AttachmentRef
	}{
		{name: "缺少 id", ref: AttachmentRef{Size: 1, SHA256: strings.Repeat("a", 64)}},
		{name: "sha256 非十六进制", ref: AttachmentRef{ID: "att-x", Size: 1, SHA256: strings.Repeat("z", 64)}},
		{name: "sha256 长度不对", ref: AttachmentRef{ID: "att-y", Size: 1, SHA256: "abc"}},
		{name: "负长度", ref: AttachmentRef{ID: "att-z", Size: -1, SHA256: strings.Repeat("a", 64)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Start(context.Background(), StartRunRequest{
				Message: "读附件", Attachments: []AttachmentRef{tc.ref},
			}); err == nil {
				t.Fatal("非法附件引用被接受：run 会带着一个永远读不到的工具跑起来")
			}
		})
	}

	// 重复引用同一个 ID 也要拒（同一份内容被当成两个附件是界面事故）。
	ref := attachmentRefFor("att-1", "a.txt", []byte("x"))
	if _, err := svc.Start(context.Background(), StartRunRequest{
		Message: "读附件", Attachments: []AttachmentRef{ref, ref},
	}); err == nil {
		t.Fatal("重复引用同一附件被接受")
	}

	// 没有存储却带附件：必须拒绝，不能"静默把附件丢掉"。
	optsNoStore := testOptions(t)
	svcNoStore := New(optsNoStore)
	t.Cleanup(func() { _ = svcNoStore.Close(context.Background()) })
	if _, err := svcNoStore.Start(context.Background(), StartRunRequest{
		Message: "读附件", Attachments: []AttachmentRef{ref},
	}); err == nil {
		t.Fatal("没有附件存储却接受了带附件的 run（附件会被静默丢掉）")
	}
}

// TestAttachmentBrokerRejectsMissingCredentialsAndUnknownID 守端点的认证面。
func TestAttachmentBrokerRejectsMissingCredentialsAndUnknownID(t *testing.T) {
	body := []byte("payload")
	ref := attachmentRefFor("att-1", "a.txt", body)
	reader := &fakeAttachmentReader{bytes: map[string][]byte{"att-1": body}}

	opts := testOptions(t)
	opts.Attachments = reader
	w := newFetchingWorker()
	// 让 worker 不要自动取数：本用例由测试自己发请求。
	opts.StartWorker = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w.mu.Lock()
		w.env = append([]string{}, spec.Env...)
		w.mu.Unlock()
		return &idleWorker{events: make(chan pi.RawEvent, 4)}, nil
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	if _, err := svc.Start(context.Background(), StartRunRequest{
		Message: "读附件", Attachments: []AttachmentRef{ref},
	}); err != nil {
		t.Fatalf("启动 run 失败: %v", err)
	}
	url := w.envValue(EnvAttachmentURL)
	token := w.envValue(EnvAttachmentToken)
	session := w.envValue(EnvAttachmentSession)
	if url == "" || token == "" || session == "" {
		t.Fatal("端点环境未交付")
	}

	do := func(setup func(*http.Request)) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, url+"/v1/attachments/att-1", nil)
		setup(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	if got := do(func(r *http.Request) {
		r.Header.Set("X-Anvil-Session", session)
	}); got != http.StatusUnauthorized {
		t.Fatalf("缺令牌应 401，实际 %d", got)
	}
	if got := do(func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Anvil-Session", "sess-other")
	}); got != http.StatusForbidden {
		t.Fatalf("换了会话应 403，实际 %d", got)
	}
	// 已知凭据但 id 不在这次 run 的冻结清单里：不能因为"存储认识"就交付。
	req, _ := http.NewRequest(http.MethodGet, url+"/v1/attachments/att-other", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Anvil-Session", session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未冻结的附件 id 应 404，实际 %d", resp.StatusCode)
	}
	if opened := reader.opened(); len(opened) != 0 {
		t.Fatalf("未冻结的 id 不该触达存储，实际读取了 %v", opened)
	}
}

// idleWorker 只回响应、不发事件（本用例只关心端点的认证面）。
type idleWorker struct {
	events chan pi.RawEvent
	once   sync.Once
}

func (w *idleWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"type":"response","success":true}`), nil
}
func (w *idleWorker) Events() <-chan pi.RawEvent { return w.events }
func (w *idleWorker) Close(ctx context.Context) error {
	w.once.Do(func() { close(w.events) })
	return nil
}
func (w *idleWorker) PID() int                   { return 0 }
func (w *idleWorker) StderrTail() (string, bool) { return "", false }
func (w *idleWorker) ExitCode() (int, bool)      { return 0, false }
func (w *idleWorker) Limitations() []string      { return nil }

// 编译期确认替身实现了需要的接口。
var (
	_ pi.Worker      = (*fetchingWorker)(nil)
	_ pi.Worker      = (*idleWorker)(nil)
	_ pi.UIResponder = (*clarifyingWorker)(nil)
	_                = fmt.Sprint
)
