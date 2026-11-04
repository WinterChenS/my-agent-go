// Command mcp 是 F02 用的受控 MCP fixture server（stdio + Streamable HTTP）。
//
// 它实现 MCP 的最小可用子集：initialize / tools/list / tools/call。
// 用途是让真实 Pi 连接一个"行为完全可预期"的 MCP server，从而捕获
// Pi 侧的 MCP 加载与工具暴露行为；不连接任何外部服务，也不访问网络。
//
// 边界：这不是通用 MCP 实现（不支持资源、提示、采样、订阅），
// 只覆盖本项目验证 Pi 集成所需的部分。
//
// 可观测开关（C04 需要"真实进程计数"与"副作用计数"，不能只靠界面显示）：
//   - -counter <path>  每次进程启动/退出追加一行（stdio 与 HTTP 都写）
//   - -calls <path>    每次 tools/call 追加一行（鉴权失败的请求不写）
//   - -tools <csv>     工具集合开关，默认 fixture_echo；加 fixture_extra 可测"工具列表变化"
//   - -auth <token>    HTTP 模式要求 Authorization: Bearer <token>，否则 401
//   - -http <addr>     以 Streamable HTTP 模式监听（默认 stdio）
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// protocolVersion 与客户端协商；客户端请求的版本若受支持则原样返回。
const protocolVersion = "2024-11-05"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// fixture 是一份可配置的 server 状态。
type fixture struct {
	tools   []string
	trace   io.Writer
	counter *os.File
	calls   *os.File
	auth    string
}

func main() {
	logPath := flag.String("log", "", "把收到的请求逐行记录到该文件，便于诊断 Pi 侧实际发送的内容")
	counterPath := flag.String("counter", "", "把进程启动/退出追加到该文件（每次一行）")
	callsPath := flag.String("calls", "", "把每次 tools/call 追加到该文件（每次一行）")
	toolsFlag := flag.String("tools", "fixture_echo", "逗号分隔的工具集合，可选 fixture_echo / fixture_extra")
	auth := flag.String("auth", "", "HTTP 模式要求的 Bearer 令牌；为空则不鉴权")
	listen := flag.String("http", "", "监听地址（如 127.0.0.1:0）；为空则走 stdio")
	addrFile := flag.String("addr-file", "", "HTTP 模式把实际监听地址写入该文件（端口 0 时便于测试）")
	flag.Parse()

	f := &fixture{auth: strings.TrimSpace(*auth)}
	var traceW io.Writer = io.Discard
	closers := []io.Closer{}
	if *logPath != "" {
		fh, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("打开日志文件失败: %v", err)
		}
		traceW = fh
		closers = append(closers, fh)
	}
	f.trace = traceW
	if *counterPath != "" {
		fh, err := os.OpenFile(*counterPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("打开计数器文件失败: %v", err)
		}
		f.counter = fh
		closers = append(closers, fh)
	}
	if *callsPath != "" {
		fh, err := os.OpenFile(*callsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("打开调用计数文件失败: %v", err)
		}
		f.calls = fh
		closers = append(closers, fh)
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	for _, name := range strings.Split(*toolsFlag, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			f.tools = append(f.tools, name)
		}
	}
	if len(f.tools) == 0 {
		f.tools = []string{"fixture_echo"}
	}

	if strings.TrimSpace(*listen) != "" {
		f.serveHTTP(*listen, *addrFile)
		return
	}
	f.serveStdio()
}

// serveStdio 是行分隔 JSON-RPC 的 stdio 实现。
func (f *fixture) serveStdio() {
	f.event("start transport=stdio pid=%d ppid=%d tools=%s", os.Getpid(), os.Getppid(), strings.Join(f.tools, "|"))
	defer f.event("exit transport=stdio pid=%d", os.Getpid())

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<16), 8<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		fmt.Fprintf(f.trace, "%s\n", line)

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			// 无法解析时不能猜 id，按 JSON-RPC 规范用 null。
			writeResp(out, response{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "Parse error"},
			})
			continue
		}

		// 通知（无 id）不产生响应。
		if len(req.ID) == 0 {
			continue
		}
		resp, ok := f.handle(req)
		if !ok {
			continue
		}
		writeResp(out, resp)
	}
	if err := sc.Err(); err != nil {
		log.Fatalf("读取 stdin 失败: %v", err)
	}
}

// serveHTTP 实现 Streamable HTTP 的最小可用子集：
// POST 单条 JSON-RPC 消息，请求有 id 时用 application/json 直接返回响应，通知返回 202。
// 不提供 GET 的 SSE 流（规范允许服务端不提供）。
func (f *fixture) serveHTTP(addr, addrFile string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	actual := ln.Addr().String()
	if addrFile != "" {
		if err := os.WriteFile(addrFile, []byte(actual), 0o644); err != nil {
			log.Fatalf("写入地址文件失败: %v", err)
		}
	}
	fmt.Println("listening on", actual)
	f.event("start transport=http addr=%s pid=%d tools=%s", actual, os.Getpid(), strings.Join(f.tools, "|"))
	defer f.event("exit transport=http pid=%d", os.Getpid())

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", f.handleHTTP)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	// 收到终止信号后干净退出：Pi 停止 stdio server 时会发 SIGTERM，
	// HTTP 模式下由测试显式 Kill/Interrupt。这里只保证 exit 行一定写出来。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (f *fixture) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if f.auth != "" {
		want := "Bearer " + f.auth
		if r.Header.Get("Authorization") != want {
			// 401 + WWW-Authenticate 是 MCP 客户端识别"需要鉴权"的标准信号。
			w.Header().Set("WWW-Authenticate", `Bearer realm="fixture"`)
			http.Error(w, "unauthorized: bad or missing bearer token", http.StatusUnauthorized)
			return
		}
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		// 会话终止：无状态实现直接接受。
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet:
		// 不提供 SSE 流。
		http.Error(w, "SSE stream not offered", http.StatusMethodNotAllowed)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	fmt.Fprintf(f.trace, "%s\n", body)

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(response{
			JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: -32700, Message: "Parse error"},
		})
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp, ok := f.handle(req)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handle 处理一条带 id 的 JSON-RPC 请求。
func (f *fixture) handle(req request) (response, bool) {
	switch req.Method {
	case "initialize":
		return response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "myagent-fixture-mcp",
					"version": "1.0.0",
				},
			},
		}, true
	case "tools/list":
		tools := make([]map[string]any, 0, len(f.tools))
		for _, name := range f.tools {
			tools = append(tools, f.describe(name))
		}
		return response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"tools": tools},
		}, true
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if !f.has(p.Name) {
			return response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "unknown tool: " + p.Name},
			}, true
		}
		f.call(p.Name)
		var args struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(p.Arguments, &args)
		text := "echo: " + args.Text
		if p.Name == "fixture_extra" {
			text = "extra: " + args.Text
		}
		return response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": text},
				},
				"isError": false,
			},
		}, true
	default:
		return response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}, true
	}
}

func (f *fixture) describe(name string) map[string]any {
	desc := "回显输入文本的受控测试工具"
	if name == "fixture_extra" {
		desc = "C04 工具列表变化用的第二个受控工具"
	}
	return map[string]any{
		"name":        name,
		"description": desc,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "要回显的文本"},
			},
			"required": []string{"text"},
		},
	}
}

func (f *fixture) has(name string) bool {
	for _, t := range f.tools {
		if t == name {
			return true
		}
	}
	return false
}

// event 把生命周期事件追加到计数器文件。
func (f *fixture) event(format string, args ...any) {
	if f.counter == nil {
		return
	}
	fmt.Fprintf(f.counter, format+"\n", args...)
}

// call 记录一次真实到达的 tools/call（鉴权失败的请求在 HTTP 层就被挡掉，不会到这里）。
func (f *fixture) call(name string) {
	if f.calls == nil {
		return
	}
	fmt.Fprintf(f.calls, "call %s\n", name)
}

// writeResp 写出一条响应并立即 flush：MCP 客户端按行阻塞读取，缓冲会直接卡死。
func writeResp(w *bufio.Writer, r response) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	w.Write(b)
	w.WriteByte('\n')
	w.Flush()
}
