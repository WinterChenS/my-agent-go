package harnessmcp

import (
	"context"
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

// defaultMaxBodyBytes 是 JSON-RPC 请求体上限。
// 工具参数不该接近这个量级；超过就说明调用方在塞别的东西。
const defaultMaxBodyBytes = 1 << 20

// protocolVersion 是未协商时的默认 MCP 版本（2025-06-18，Streamable HTTP）。
const protocolVersion = "2025-06-18"

// Options 构造自有 MCP 服务。
type Options struct {
	// Tools 是本服务暴露的工具。为空表示只暴露协议骨架（tools/list 返回空）。
	Tools []Tool
	// MaxBodyBytes 请求体上限，默认 1 MiB。
	MaxBodyBytes int64
	// Version 出现在 serverInfo 里，便于从 Pi 日志确认连的是哪个构建。
	Version string
}

// Endpoint 是交给上层的端点描述，只含 URL 与令牌引用，不含令牌本体。
type Endpoint struct {
	URL      string `json:"url"`
	TokenRef string `json:"tokenRef"`
}

// Server 是一个自有 MCP 服务实例（应用级单例）。
type Server struct {
	tools   []Tool
	maxBody int64
	version string

	ln   net.Listener
	http *http.Server
	port int
	url  string

	token    string
	tokenRef string

	mu     sync.Mutex
	calls  []string // 已到达 handler 的工具名，供"未到达"这类断言使用
	closed bool
	done   chan struct{}
}

// Start 在回环随机端口上启动 MCP 服务。
//
// 端口交给 OS 分配：固定端口会与残留进程冲突，而"bind 成功但请求被旧进程接走"
// 这种故障极难排查（本仓库已经踩过一次）。
func Start(ctx context.Context, opts Options) (*Server, error) {
	token, err := newToken()
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, err.Error(), false)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "MCP 服务监听失败: "+err.Error(), false)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "0.0.0-dev"
	}
	s := &Server{
		tools:   opts.Tools,
		maxBody: opts.MaxBodyBytes,
		version: version,
		ln:      ln,
		port:    port,
		url:     fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		token:   token,
		// 引用值不含令牌本体：令牌只经 Env 注入子进程，不进 DTO、日志或前端。
		tokenRef: fmt.Sprintf("harnessmcp:%d", port),
		done:     make(chan struct{}),
	}
	if s.maxBody <= 0 {
		s.maxBody = defaultMaxBodyBytes
	}

	mux := http.NewServeMux()
	mux.Handle("POST /mcp", s.guard(s.handleRPC))
	// Streamable HTTP 允许客户端 GET 开服务端推送流、DELETE 终止会话。
	// 本服务不发起服务端推送，按规范回 405 让客户端退回纯 POST 模式。
	mux.Handle("GET /mcp", s.guard(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		writeRPCError(w, http.StatusMethodNotAllowed, nil, -32601, "本服务不支持服务端推送流")
	}))
	mux.Handle("DELETE /mcp", s.guard(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.Handle("GET /healthz", s.guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "toolCount": len(s.tools)})
	}))

	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = err
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close(context.Background())
		case <-s.done:
		}
	}()
	return s, nil
}

// Endpoint 返回端点描述（不含令牌）。
func (s *Server) Endpoint() Endpoint { return Endpoint{URL: s.url, TokenRef: s.tokenRef} }

// Env 返回写进 worker 环境变量的两条信息。
//
// mcp.json 里用 `${MYAGENT_HARNESS_TOKEN}` 引用令牌，而不是把明文写进配置：
// 配置文件会落盘、会被备份、会被贴进 issue。
func (s *Server) Env() []string {
	return []string{
		"MYAGENT_HARNESS_URL=" + s.url,
		"MYAGENT_HARNESS_TOKEN=" + s.token,
	}
}

// Port 返回实际监听端口。
func (s *Server) Port() int { return s.port }

// Calls 返回已到达 handler 的工具名快照。
//
// "工具被拒绝"必须用这个断言，而不是看界面文案：策略拦下时 Pi 根本不会发出
// tools/call，本服务也就不该有任何记录。
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// Close 关闭服务。
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	close(s.done)
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.http.Shutdown(shutdownCtx)
}

// guard 是认证与来源校验的统一入口，顺序固定：Host → Origin → 令牌。
//
// 任何一步失败都在触达工具之前返回，因此不会产生副作用。
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, s.port) {
			writeRPCError(w, http.StatusForbidden, nil, -32000, "Host 不是本机回环端点")
			return
		}
		if !originAllowed(r.Header.Get("Origin"), s.port) {
			writeRPCError(w, http.StatusForbidden, nil, -32000, "请求来自其它来源")
			return
		}
		if !tokenMatches(bearerToken(r), s.token) {
			writeRPCError(w, http.StatusUnauthorized, nil, -32001, "令牌无效")
			return
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			writeRPCError(w, http.StatusServiceUnavailable, nil, -32002, "MCP 服务已关闭")
			return
		}
		next(w, r)
	}
}

// rpcRequest 是 JSON-RPC 2.0 请求。
//
// ID 用 RawMessage 原样回显：MCP 客户端可能发数字也可能发字符串，
// 归一成 string 会让客户端认不出自己的响应。
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody+1))
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, -32700, "读取请求体失败")
		return
	}
	if int64(len(body)) > s.maxBody {
		writeRPCError(w, http.StatusRequestEntityTooLarge, nil, -32003, "请求体超过上限")
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, -32700, "不是合法 JSON-RPC")
		return
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		writeRPCError(w, http.StatusBadRequest, req.ID, -32600, "只支持 JSON-RPC 2.0")
		return
	}

	// 通知（无 id）不需要响应体。初始化完成通知必须回 202，否则客户端会一直等。
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	if isNotification {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		s.writeResult(w, req.ID, s.initializeResult(req.Params))
	case "ping":
		s.writeResult(w, req.ID, map[string]any{})
	case "tools/list":
		s.writeResult(w, req.ID, map[string]any{"tools": s.toolDescriptors()})
	case "tools/call":
		s.handleToolsCall(w, r, req)
	default:
		writeRPCError(w, http.StatusOK, req.ID, -32601, "未知方法: "+req.Method)
	}
}

// initializeResult 协商协议版本。
//
// 回显客户端请求的版本：MCP 客户端按"服务端返回的版本"决定后续行为，
// 硬写一个固定版本会让较新的客户端降级或直接断开。空则给默认版本。
func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	v := strings.TrimSpace(p.ProtocolVersion)
	if v == "" {
		v = protocolVersion
	}
	return map[string]any{
		"protocolVersion": v,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "myagent-harness",
			"version": s.version,
		},
	}
}

func (s *Server) toolDescriptors() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, describeTool(t))
	}
	return out
}

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeRPCError(w, http.StatusOK, req.ID, -32602, "tools/call 参数不合法")
			return
		}
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		writeRPCError(w, http.StatusOK, req.ID, -32602, "缺少工具名")
		return
	}
	tool, ok := toolByName(s.tools, name)
	if !ok {
		writeRPCError(w, http.StatusOK, req.ID, -32602, "未知工具: "+name)
		return
	}

	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()

	if tool.Handler == nil {
		s.writeResult(w, req.ID, toolCallResult("工具未实现", true))
		return
	}
	res, err := tool.Handler(r.Context(), p.Arguments)
	if err != nil {
		// 工具错误是"业务失败"，不是协议错误：必须用 isError 结果返回，
		// 让模型看到失败原因并自行决定下一步，而不是让客户端以为协议坏了。
		msg := err.Error()
		if ae, ok := err.(*contracts.AppError); ok && ae != nil {
			msg = ae.Message
		}
		s.writeResult(w, req.ID, toolCallResult(msg, true))
		return
	}
	s.writeResult(w, req.ID, toolCallResult(res.Text, res.IsError))
}

func toolCallResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *Server) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string) {
	writeJSON(w, status, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
