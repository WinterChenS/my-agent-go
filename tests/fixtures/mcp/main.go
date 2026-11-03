// Command mcp 是 F02 用的受控 MCP fixture server（stdio + 行分隔 JSON-RPC）。
//
// 它实现 MCP 的最小可用子集：initialize / tools/list / tools/call。
// 用途是让真实 Pi 连接一个"行为完全可预期"的 MCP server，从而捕获
// Pi 侧的 MCP 加载与工具暴露行为；不连接任何外部服务，也不访问网络。
//
// 边界：这不是通用 MCP 实现（不支持资源、提示、采样、订阅），
// 只覆盖本项目验证 Pi 集成所需的部分。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
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

func main() {
	logPath := flag.String("log", "", "把收到的请求逐行记录到该文件，便于诊断 Pi 侧实际发送的内容")
	flag.Parse()

	var trace io.Writer = io.Discard
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("打开日志文件失败: %v", err)
		}
		defer f.Close()
		trace = f
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<16), 8<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		fmt.Fprintf(trace, "%s\n", line)

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

		switch req.Method {
		case "initialize":
			writeResp(out, response{
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
			})
		case "tools/list":
			writeResp(out, response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"tools": []map[string]any{
						{
							"name":        "fixture_echo",
							"description": "回显输入文本的受控测试工具",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"text": map[string]any{"type": "string", "description": "要回显的文本"},
								},
								"required": []string{"text"},
							},
						},
					},
				},
			})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name != "fixture_echo" {
				writeResp(out, response{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &rpcError{Code: -32602, Message: "unknown tool: " + p.Name},
				})
				continue
			}
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(p.Arguments, &args)
			writeResp(out, response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "echo: " + args.Text},
					},
					"isError": false,
				},
			})
		default:
			writeResp(out, response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
			})
		}
	}
	if err := sc.Err(); err != nil {
		log.Fatalf("读取 stdin 失败: %v", err)
	}
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
