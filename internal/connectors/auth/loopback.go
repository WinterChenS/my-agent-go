package auth

// RFC 8252 的桌面 OAuth：只允许 loopback 重定向，只接受一次请求。
//
// 明确不做（否则就是"看起来支持"）：
//   - 自定义 scheme（anvil://）接回调：那会被任意应用抢注，且没有"只在本机"的保证；
//   - 非回环地址：redirect_uri 指向别的 host 时，授权码会离开本机；
//   - 多次回调：第二次请求一律拒绝，避免重放第二个 code。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
)

// rejectNonLoopbackRedirect 拒绝对非回环 redirect_uri 的任何使用。
func rejectNonLoopbackRedirect(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "redirect_uri 不合法: "+err.Error(), false)
	}
	if u.Scheme != "http" {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"redirect_uri 只允许 http 回环地址（不能是 "+u.Scheme+"://）", false)
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "::1", "localhost":
	default:
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"redirect_uri 必须是回环地址: "+raw, false)
	}
	if u.Port() == "" {
		return contracts.NewAppError(contracts.CodeUnauthorized, "redirect_uri 必须带端口（桌面客户端用随机端口）", false)
	}
	if u.Fragment != "" || u.User != nil || (u.Path != "" && u.Path != "/callback") {
		return contracts.NewAppError(contracts.CodeUnauthorized, "redirect_uri 形式不受支持: "+raw, false)
	}
	return nil
}

// ValidateRedirectURI 是对外暴露的同一套校验（测试与适配器共用）。
func ValidateRedirectURI(raw string) error { return rejectNonLoopbackRedirect(raw) }

// Callback 是一次回环回调的内容。
type Callback struct {
	Code  string
	State string
}

// Loopback 是只监听回环随机端口的回调接收器。
type Loopback struct {
	ln  net.Listener
	srv *http.Server
	url string

	mu        sync.Mutex
	delivered bool
	results   chan callbackResult
	done      chan struct{}
}

type callbackResult struct {
	cb  Callback
	err error
}

// NewLoopback 在 127.0.0.1 的随机端口上启动回调接收器。
//
// 端口交给 OS 分配：固定端口既会与残留进程冲突，也会让"授权码发给了旧进程"
// 这种故障变得难以解释。
func NewLoopback() (*Loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "回环监听失败: "+err.Error(), false)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	l := &Loopback{
		ln:      ln,
		url:     fmt.Sprintf("http://127.0.0.1:%d/callback", port),
		results: make(chan callbackResult, 1),
		done:    make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", l.handleCallback)
	l.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := l.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			_ = err
		}
	}()
	return l, nil
}

// RedirectURI 是登记到应用里的回环回调地址。
func (l *Loopback) RedirectURI() string { return l.url }

// Port 返回实际监听端口。
func (l *Loopback) Port() int { return l.ln.Addr().(*net.TCPAddr).Port }

// Close 关闭监听与服务器（幂等）。
func (l *Loopback) Close() error {
	select {
	case <-l.done:
		return nil
	default:
	}
	close(l.done)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return l.srv.Shutdown(ctx)
}

func (l *Loopback) handleCallback(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	if l.delivered {
		l.mu.Unlock()
		http.Error(w, "该授权回调已被处理", http.StatusConflict)
		return
	}
	l.delivered = true
	l.mu.Unlock()

	q := r.URL.Query()
	if e := strings.TrimSpace(q.Get("error")); e != "" {
		desc := strings.TrimSpace(q.Get("error_description"))
		l.deliver(callbackResult{err: contracts.NewAppError(
			contracts.CodeUnauthorized, "授权被拒绝: "+e+" "+desc, false)})
		http.Error(w, "授权失败，可以关闭此页面", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(q.Get("code"))
	state := strings.TrimSpace(q.Get("state"))
	if code == "" {
		l.deliver(callbackResult{err: contracts.NewAppError(
			contracts.CodeUnauthorized, "回调缺少 code", false)})
		http.Error(w, "回调缺少 code", http.StatusBadRequest)
		return
	}
	l.deliver(callbackResult{cb: Callback{Code: code, State: state}})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("授权完成，可以关闭此页面。\n"))
}

func (l *Loopback) deliver(res callbackResult) {
	select {
	case l.results <- res:
	default:
	}
}

// Wait 等待一次回调并校验 state。
//
// state 不匹配直接失败（fail-closed）：继续等第二个回调等于给攻击者多次机会，
// 而授权请求本身已经在浏览器里发出，用户重试一次更清楚。
func (l *Loopback) Wait(ctx context.Context, wantState string) (Callback, error) {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return Callback{}, contracts.NewAppError(contracts.CodeRpcTimeout, "等待授权回调被取消", true)
		case res := <-l.results:
			if res.err != nil {
				return Callback{}, res.err
			}
			if wantState == "" || res.cb.State != wantState {
				return Callback{}, contracts.NewAppError(
					contracts.CodeUnauthorized,
					"OAuth state 不匹配，拒绝该回调",
					false,
				)
			}
			return res.cb, nil
		case <-time.After(time.Until(deadline)):
			return Callback{}, contracts.NewAppError(contracts.CodeRpcTimeout, "等待授权回调超时", true)
		}
	}
}
