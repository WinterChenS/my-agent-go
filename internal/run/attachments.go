// C06f：附件投递 seam（run 侧）。
//
// 为什么是"broker + 工具"而不是"把附件塞进 prompt"或"给 Pi 一个文件路径"：
//
//   - 塞进 prompt 只对图片成立（Pi 的 prompt 只有 `images` 字段），而且会把正文与
//     附件混成一条不可审计的消息；
//   - 给路径（哪怕是只读路径）等于把"可读"和"可写"的边界交给文件系统权限，而这
//     正是 product policy 要自己说了算的事；
//   - 所以字节只经一个 app-owned、只监听回环、每 run 一枚令牌、只读的端点交付。
//     Pi 侧的薄扩展（pi-extensions/anvil-attachment.ts）用环境变量里的 URL+令牌
//     去取，模型拿到的只是工具结果文本，进程里从未出现附件路径。
//
// 与 internal/session 的分工：blob store 与 integrity 校验在 session（那里有
// `OpenAttachment`：同一个打开句柄上验 size/sha256 之后才交付字节）。本文件只是
// 把那一次读取"挂到网上"，不实现第二套存储、不重复实现校验。run 侧的额外职责
// 只有一条：把实际交付的字节再对一次**冻结在 binding 里的 sha256**，防止 store
// 里的能力与这次 run 请求声称的内容不是同一份。
//
// 认证与来源校验与 policybridge 同构（Host → Origin → 令牌 → 会话）。这段逻辑
// 在这里是第二份实现，代价是刻意的：policybridge 的这几个判定是包内私有，
// 为了共用而把它们导出，会让"授权端点"和"附件端点"互相牵制；两边的规则都由
// 各自的测试钉住（见 attachments_broker_test.go 与 policybridge/server_test.go）。
package run

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
)

// AttachmentReader 是 run 侧对 app 托管附件存储的唯一读取入口。
//
// 由绑定层实现（生产：internal/session.AttachmentStore）。本包不认识 blob store，
// 也不自己校验文件——校验发生在实现里，且必须在返回任何字节之前完成。
type AttachmentReader interface {
	// OpenAttachment 返回已经校验过的字节；任何校验失败都必须返回错误，
	// 且不得返回部分内容（fail closed）。
	OpenAttachment(id string) (io.ReadCloser, error)
}

// 附件端点默认上限。
const (
	defaultAttachmentBodyLimit = 64 << 10 // 请求体：只接受空体，不需要大空间
	defaultAttachmentTimeout   = 30 * time.Second
)

// attachmentBroker 是一次 run 独占的只读附件端点。
type attachmentBroker struct {
	reader     AttachmentReader
	binding    ExecutionBinding
	sessionIDs string

	ln   net.Listener
	http *http.Server
	port int
	url  string

	token    string
	closeCh  chan struct{}
	closeOne sync.Once

	mu     sync.Mutex
	closed bool
}

// startAttachmentBroker 在回环随机端口上启动端点。
//
// 端口交给 OS 分配：固定端口会与残留实例冲突，而"能 bind 但请求被旧进程接走"
// 这种故障极难排查（policybridge 踩过同一个坑）。
func startAttachmentBroker(ctx context.Context, reader AttachmentReader, binding ExecutionBinding) (*attachmentBroker, error) {
	if reader == nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "附件 broker 缺少存储实现", false)
	}
	if len(binding.Attachments) == 0 {
		return nil, contracts.NewAppError(contracts.CodeInternal, "没有附件却要启动 broker", false)
	}
	token, err := newAttachmentToken()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "附件端点监听失败: "+err.Error(), false)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	b := &attachmentBroker{
		reader:     reader,
		binding:    binding,
		sessionIDs: string(binding.SessionID()),
		ln:         ln,
		port:       port,
		url:        fmt.Sprintf("http://127.0.0.1:%d", port),
		token:      token,
		closeCh:    make(chan struct{}),
	}

	mux := http.NewServeMux()
	// 只读：只有 GET。取字节与列清单分开，清单里没有任何路径。
	mux.Handle("GET /v1/attachments", b.guard(b.handleList))
	mux.Handle("GET /v1/attachments/{id}", b.guard(b.handleRead))

	b.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      defaultAttachmentTimeout,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		if serveErr := b.http.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			// 监听层故障只影响交付；调用方会在取字节时拿到连接错误并 fail closed。
			_ = serveErr
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = b.Close()
		case <-b.closeCh:
		}
	}()
	return b, nil
}

// Env 返回注入 worker 子进程的端点与令牌。
//
// 这是令牌唯一的对外交付方式（与 policybridge 同一口径）。
func (b *attachmentBroker) Env() []string {
	return []string{
		EnvAttachmentURL + "=" + b.url,
		EnvAttachmentToken + "=" + b.token,
		EnvAttachmentSession + "=" + b.sessionIDs,
	}
}

// Port 返回实际监听端口（诊断/断言"确实是随机端口"）。
func (b *attachmentBroker) Port() int { return b.port }

// Close 关闭端点。重复调用无副作用；关闭后再取字节只会拿到失败。
func (b *attachmentBroker) Close() error {
	var err error
	b.closeOne.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		close(b.closeCh)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = b.http.Shutdown(shutdownCtx)
	})
	return err
}

// guard 是认证与来源校验的统一入口：Host → Origin → 令牌 → 会话 → 是否已关闭。
//
// 任何一步失败都在触达存储与工具之前返回，因此不会产生任何字节交付。
func (b *attachmentBroker) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !brokerHostAllowed(r.Host, b.port) {
			writeBrokerError(w, http.StatusForbidden, "FORBIDDEN_HOST", "Host 不是本机回环端点")
			return
		}
		if !brokerOriginAllowed(r.Header.Get("Origin"), b.port) {
			writeBrokerError(w, http.StatusForbidden, "FORBIDDEN_ORIGIN", "请求来自其它来源")
			return
		}
		if !brokerTokenMatches(brokerBearer(r), b.token) {
			writeBrokerError(w, http.StatusUnauthorized, "UNAUTHORIZED", "令牌无效")
			return
		}
		if got := r.Header.Get("X-Anvil-Session"); got != b.sessionIDs {
			writeBrokerError(w, http.StatusForbidden, "FORBIDDEN_SESSION", "会话不匹配")
			return
		}
		b.mu.Lock()
		closed := b.closed
		b.mu.Unlock()
		if closed {
			writeBrokerError(w, http.StatusServiceUnavailable, "CLOSED", "附件端点已关闭")
			return
		}
		next(w, r)
	}
}

// attachmentView 是清单条目：只有元数据，没有路径。
type attachmentView struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	MediaType   string `json:"mediaType,omitempty"`
}

// handleList 返回这次 run 冻结的附件清单。
//
// 清单来自 binding（admission 时冻结），不是来自存储的"当前状态"：
// run 期间存储里可能又登记了新附件，但那不属于这一次 run。
func (b *attachmentBroker) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBrokerError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "只支持 GET")
		return
	}
	items := make([]attachmentView, 0, len(b.binding.Attachments))
	for _, ref := range b.binding.Attachments {
		items = append(items, attachmentView{
			ID: ref.ID, DisplayName: ref.DisplayName, Size: ref.Size,
			SHA256: ref.SHA256, MediaType: ref.MediaType,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"attachments": items})
}

// handleRead 交付一个附件的字节。
//
// 顺序（任何一步失败都不交出字节）：
//  1. id 必须在这次 run 冻结的清单里——不是"存储认识这个 id"就行；
//  2. 存储按自己的登记重新校验 size/sha256，通过才返回字节；
//  3. 交付前再对一次冻结的 sha256 与长度：请求说的是 A，交出去的就必须是 A。
func (b *attachmentBroker) handleRead(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	ref, ok := b.lookup(id)
	if !ok {
		// 未知 id 与"没登记在这次 run"是同一类拒绝：不泄露其它 run 的附件存在性。
		writeBrokerError(w, http.StatusNotFound, "NOT_FOUND", "这次 run 没有这个附件")
		return
	}
	body, err := b.reader.OpenAttachment(id)
	if err != nil {
		writeBrokerError(w, http.StatusConflict, brokerErrorCode(err), err.Error())
		return
	}
	defer func() { _ = body.Close() }()

	// 完整读出来再发：长度与 hash 都要在写出第一个字节之前确定，
	// 否则"发了一半才发现被改"会把半份未校验内容交给模型。
	data, readErr := io.ReadAll(io.LimitReader(body, ref.Size+1))
	if readErr != nil {
		writeBrokerError(w, http.StatusConflict, "VERIFICATION_FAILED", "读取附件失败: "+readErr.Error())
		return
	}
	if int64(len(data)) != ref.Size {
		writeBrokerError(w, http.StatusConflict, "VERIFICATION_FAILED", fmt.Sprintf(
			"交付长度与冻结的引用不一致（引用 %d，实际 %d）", ref.Size, len(data)))
		return
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), ref.SHA256) {
		writeBrokerError(w, http.StatusConflict, "VERIFICATION_FAILED",
			"交付内容与冻结的 sha256 不一致，拒绝交付")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Anvil-SHA256", ref.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (b *attachmentBroker) lookup(id string) (AttachmentRef, bool) {
	for _, ref := range b.binding.Attachments {
		if ref.ID == id {
			return ref, true
		}
	}
	return AttachmentRef{}, false
}

// brokerErrorCode 把存储返回的结构化错误转成端点错误码（保留可定位性）。
func brokerErrorCode(err error) string {
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && strings.TrimSpace(appErr.Code) != "" {
		return appErr.Code
	}
	return "VERIFICATION_FAILED"
}

// ---- 认证小工具（与 policybridge 同构） ----

func newAttachmentToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "生成附件令牌失败: "+err.Error(), false)
	}
	return hex.EncodeToString(buf), nil
}

// brokerTokenMatches 用常量时间比较令牌。
func brokerTokenMatches(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func brokerBearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(v) <= len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(v[len(prefix):])
}

// brokerHostAllowed 校验 Host 指向本端点（防 DNS rebinding）。
func brokerHostAllowed(hostport string, port int) bool {
	host, p, err := net.SplitHostPort(hostport)
	if err != nil || p != fmt.Sprint(port) {
		return false
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// brokerOriginAllowed 校验 Origin：没有 Origin（非浏览器）放行，有则必须同源。
func brokerOriginAllowed(origin string, port int) bool {
	if origin == "" {
		return true
	}
	for _, scheme := range []string{"http://", "https://"} {
		if rest, ok := strings.CutPrefix(origin, scheme); ok && brokerHostAllowed(rest, port) {
			return true
		}
	}
	return false
}

func writeBrokerError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
