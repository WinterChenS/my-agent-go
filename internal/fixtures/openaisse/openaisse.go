// Package openaisse 提供 Pi F02 协议捕获所需的受控 OpenAI Chat Completions 流式端点。
//
// 设计要点：
//   - 完全离线：不访问任何网络，按脚本回放 SSE 增量；
//   - 走 Pi 内置 openai-completions 实现：因此捕获到的是 Pi 真实的请求转换、
//     工具调用拼装、usage 结算与事件上报链路，而不是自定义 provider 的旁路；
//   - 可复用：tests/fixtures/provider 的命令行壳与 Go 测试共用同一 Handler。
//
// 边界：回放内容不是真实模型输出。用它产生的协议记录必须在证据中标注 fixture。
package openaisse

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Handler 是 fixture 的 HTTP 入口。
type Handler struct {
	recordDir string
	mu        sync.Mutex
	seq       int
}

// NewHandler 创建 fixture handler。recordDir 非空时，收到的请求体会按序号落盘，
// 用于核对 Pi 实际发送的 payload。目录不存在时会自动创建——否则 WriteFile 静默失败，
// 表现为"请求目录为空"，很容易误判成客户端没发请求。
func NewHandler(recordDir string) *Handler {
	if recordDir != "" {
		_ = os.MkdirAll(recordDir, 0o755)
	}
	return &Handler{recordDir: recordDir}
}

// ServeHTTP 实现 /v1/chat/completions、/v1/models 与 /healthz。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/chat/completions":
		h.handleChat(w, r)
	case "/v1/models":
		h.handleModels(w)
	case "/healthz":
		_, _ = w.Write([]byte("ok"))
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleModels(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "fixture-model", "object": "model", "owned_by": "myagent-fixture"},
		},
	})
}

// chatRequest 只声明 fixture 关心的字段，其余忽略。
type chatRequest struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
}

// messageEnvelope 用于从消息里取出 role / content。
type messageEnvelope struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type toolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求失败", http.StatusBadRequest)
		return
	}
	h.record(body)

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "请求不是合法 JSON", http.StatusBadRequest)
		return
	}

	sc := selectScenario(req)
	if sc.HTTPStatus != 0 {
		http.Error(w, sc.FailText, sc.HTTPStatus)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	emit := func(c chunk) {
		b, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	base := chunk{ID: "chatcmpl-fixture", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: req.Model}

	// 首条记录必须携带 role，符合 OpenAI 流式约定。
	first := base
	first.Choices = []chunkChoice{{Index: 0, Delta: delta{Role: "assistant"}}}
	emit(first)

	for _, piece := range sc.Deltas {
		c := base
		c.Choices = []chunkChoice{{Index: 0, Delta: delta{Content: piece}}}
		emit(c)
	}

	// 工具调用的 arguments 按片段分批发，模拟真实模型的分块增量。
	for i, frag := range sc.ToolArgs {
		c := base
		tc := toolCall{Index: 0}
		if i == 0 {
			tc.ID = "call_fixture_1"
			tc.Type = "function"
			tc.Function.Name = sc.ToolName
		}
		tc.Function.Arguments = frag
		c.Choices = []chunkChoice{{Index: 0, Delta: delta{ToolCalls: []toolCall{tc}}}}
		emit(c)
	}

	finish := sc.Finish
	last := base
	last.Choices = []chunkChoice{{Index: 0, Delta: delta{}, FinishReason: &finish}}
	last.Usage = &usage{
		PromptTokens:     sc.PromptTokens,
		CompletionTokens: sc.CompletionTokens,
		TotalTokens:      sc.PromptTokens + sc.CompletionTokens,
	}
	emit(last)

	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (h *Handler) record(body []byte) {
	if h.recordDir == "" {
		return
	}
	h.mu.Lock()
	h.seq++
	n := h.seq
	h.mu.Unlock()
	_ = os.WriteFile(filepath.Join(h.recordDir, fmt.Sprintf("request-%03d.json", n)), body, 0o644)
}

// Scenario 描述一次回放行为。
type Scenario struct {
	Name     string
	Deltas   []string
	ToolName string
	ToolArgs []string
	Finish   string
	// PromptTokens / CompletionTokens 是上报给 Pi 的 usage。
	// Pi 依据 usage 判断上下文占用，因此需要诱发压缩的场景必须报大值。
	PromptTokens     int
	CompletionTokens int
	HTTPStatus       int
	FailText         string
}

// selectScenario 根据最后一条用户消息里的 [[scenario:xxx]] 标记选择脚本。
//
// 必须同时观察是否已有 role=tool 的回传结果：若已经执行过工具还继续索要工具调用，
// Pi 会进入无限工具循环（实测 122 轮后仍在继续）。因此工具结果回传后收敛为文本收尾。
func selectScenario(req chatRequest) Scenario {
	marker := "text"
	seenToolResult := false
	for i := len(req.Messages) - 1; i >= 0; i-- {
		var m messageEnvelope
		if json.Unmarshal(req.Messages[i], &m) != nil {
			continue
		}
		switch m.Role {
		case "tool":
			seenToolResult = true
		case "user":
			marker = parseMarker(string(m.Content))
		}
	}

	sc := BuildScenario(marker, len(req.Tools) > 0)
	if sc.Finish == "tool_calls" && seenToolResult {
		return withDefaultUsage(Scenario{
			Name:   sc.Name + "-followup",
			Deltas: []string{"命令已执行完毕，本轮结束。"},
			Finish: "stop",
		})
	}
	return withDefaultUsage(sc)
}

// withDefaultUsage 填充常规场景的 usage 默认值。
// 需要诱发上下文压缩的场景自行声明大 usage，不受影响。
func withDefaultUsage(sc Scenario) Scenario {
	if sc.PromptTokens == 0 {
		sc.PromptTokens = 12
		sc.CompletionTokens = 7
	}
	return sc
}

func parseMarker(raw string) string {
	text := ExtractText(raw)
	const open, closeTok = "[[scenario:", "]]"
	i := strings.Index(text, open)
	if i < 0 {
		return "text"
	}
	rest := text[i+len(open):]
	j := strings.Index(rest, closeTok)
	if j < 0 {
		return "text"
	}
	return strings.TrimSpace(rest[:j])
}

// ExtractText 兼容 content 为字符串或分段数组两种形态。
func ExtractText(raw string) string {
	if raw == "" || raw == "null" {
		return ""
	}
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}

// BuildScenario 构造具名回放脚本。hasTools 表示 Pi 本次请求是否声明了工具。
func BuildScenario(name string, hasTools bool) Scenario {
	switch name {
	case "unicode":
		// 覆盖分帧边界：多字节、U+2028、emoji、CRLF。
		return Scenario{
			Name:   name,
			Deltas: []string{"中文", "多字节😀", "行分隔\u2028不该断行", "制表\t和\r\n回车", "结尾"},
			Finish: "stop",
		}
	case "tool":
		fn := "bash"
		_ = hasTools
		// 参数故意拆成两段，验证跨增量的 JSON 拼装。
		return Scenario{
			Name:     name,
			Deltas:   []string{"我来执行一个命令。"},
			ToolName: fn,
			ToolArgs: []string{`{"command":"echo fixture-tool-ran"`, `,"timeout":10000}`},
			Finish:   "tool_calls",
		}
	case "tool-file":
		// 与 "tool" 的区别：命令带真实的文件副作用。
		// 上一条用 echo 输出做断言时，"被拦住了"和"跑过了"在记录里都出现同一个字符串
		// （工具参数里就含它），根本无法区分——所以断言必须落在操作系统层面。
		return Scenario{
			Name:     name,
			Deltas:   []string{"执行一个会产生文件的命令。"},
			ToolName: "bash",
			ToolArgs: []string{`{"command":"echo ran > tool-ran.txt"}`},
			Finish:   "tool_calls",
		}
	case "tool-write":
		// write 通道：参数与 Pi 1.5.0 的 writeSchema 一致（path/content）。
		// 断言落在文件系统上——"被拦了"和"跑了"在记录里都含同一个文件名。
		return Scenario{
			Name:     name,
			Deltas:   []string{"写一个文件。"},
			ToolName: "write",
			ToolArgs: []string{`{"path":"write-ran.txt","content":"written by write tool"}`},
			Finish:   "tool_calls",
		}
	case "tool-edit":
		// edit 通道：参数与 editSchema 一致（path/edits[].oldText/newText）。
		// 目标文件由用例预先创建，内容必须是唯一的 before 串，否则 Pi 会拒绝。
		return Scenario{
			Name:     name,
			Deltas:   []string{"改一个文件。"},
			ToolName: "edit",
			ToolArgs: []string{`{"path":"edit-target.txt","edits":[{"oldText":"before","newText":"after"}]}`},
			Finish:   "tool_calls",
		}
	case "tool-ext":
		// 扩展工具通道：fixture_marker 由 tests/fixtures/extension/fixture-tool.ts 注册。
		// 该工具被执行时会写 extension-ran.txt，因此"被拦"与"跑了"可落在文件系统上判定。
		// 参数名用 marker（非 path），使策略层不做文件范围绑定——本用例只考察通道本身。
		return Scenario{
			Name:     name,
			Deltas:   []string{"调用扩展工具。"},
			ToolName: "fixture_marker",
			ToolArgs: []string{`{"marker":"extension-ran.txt"}`},
			Finish:   "tool_calls",
		}
	case "tool-harness":
		// 自有 MCP 通道：Pi 把 MCP 工具改名成 mcp__<server>__<tool>，
		// 我们的 server 叫 harness，因此全名是 mcp__harness__harness_probe。
		// 工具由 internal/harnessmcp 提供，执行时在 workspace 写 marker，
		// 判定同样落在文件系统上。
		return Scenario{
			Name:     name,
			Deltas:   []string{"调用自有 MCP 工具。"},
			ToolName: "mcp__harness__harness_probe",
			ToolArgs: []string{`{"path":"harness-ran.txt"}`},
			Finish:   "tool_calls",
		}
	case "tool-codemode":
		// codemode 嵌套通道：脚本里再调 bash。外层是 codemode 工具调用，内层是 bash
		// 工具调用——两条都会触发 tool_call 钩子，且内层带 parentToolCallId。
		// 这条通道最容易被"只看顶层工具"的实现漏掉，所以单独覆盖。
		// 需要 --tools +codemode 才会激活（该工具默认 defaultActive:false）。
		return Scenario{
			Name:     name,
			Deltas:   []string{"用脚本调用一个工具。"},
			ToolName: "codemode",
			ToolArgs: []string{`{"code":"const r = await tools.bash({ command: \"echo codemode > codemode-ran.txt\" }); return r.exit_code;"}`},
			Finish:   "tool_calls",
		}
	case "tool-mcp":
		// 调用受控 MCP server 的工具。用于验证"外部 MCP 通道同样在执行前被拦"：
		// fixture MCP 会把收到的每条请求写进日志，因此"是否到达 handler"可判定。
		return Scenario{
			Name:     name,
			Deltas:   []string{"调用 MCP 工具。"},
			ToolName: "mcp__fixture__fixture_echo",
			ToolArgs: []string{`{"text":"mcp-call"}`},
			Finish:   "tool_calls",
		}
	case "empty":
		return Scenario{Name: name, Finish: "stop"}
	case "error500":
		return Scenario{Name: name, HTTPStatus: http.StatusInternalServerError, FailText: "fixture upstream failure"}
	case "heavy":
		// 上报接近 contextWindow 的 usage，用于让 Pi 认为上下文已满，
		// 从而使后续的 compact 命令有实际可压缩的内容。
		return Scenario{
			Name:             name,
			Deltas:           []string{"已处理一大段上下文。"},
			Finish:           "stop",
			PromptTokens:     7600,
			CompletionTokens: 1900,
		}
	case "bulk":
		// 同时满足压缩的两个口径：正文足够长（按字符估算 token 时够大），
		// 且上报大 usage（Pi 若按 usage 累计上下文也能触发）。
		// 目的是让上下文超过 keepRecentTokens（默认 20000），
		// 否则 compact 会返回 "Nothing to compact (session too small)"。
		piece := strings.Repeat("这是一段用于压满上下文窗口的填充文本。", 40)
		deltas := make([]string, 0, 10)
		for i := 0; i < 10; i++ {
			deltas = append(deltas, piece)
		}
		return Scenario{
			Name:             name,
			Deltas:           deltas,
			Finish:           "stop",
			PromptTokens:     7600,
			CompletionTokens: 1900,
		}
	case "long":
		// 用于诱发 compaction：足量文本，配合很小的 contextWindow。
		piece := strings.Repeat("这是一段用于压满上下文窗口的填充文本。", 40)
		deltas := make([]string, 0, 30)
		for i := 0; i < 30; i++ {
			deltas = append(deltas, piece)
		}
		return Scenario{Name: name, Deltas: deltas, Finish: "stop"}
	default:
		return Scenario{
			Name:   "text",
			Deltas: []string{"fixture 收到请求。", "这是脚本化回放，未请求任何真实模型。"},
			Finish: "stop",
		}
	}
}
