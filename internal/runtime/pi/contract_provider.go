package pi

// 契约探测自带的 stub provider。
//
// 为什么探测需要一个自己的模型：真实 Pi 在没有任何可用模型时会直接拒绝 prompt
// （"No API key found for the selected model."），rpc/settled/abort 三项必然失败，
// 于是一个完全可用的 Pi 被判 incompatible（2026-11-06 生产路径实测）。用户是否
// 配了模型、配了谁的密钥，都与"这个二进制会不会说我们的协议"无关，所以探测自带。
//
// 三条硬约束：
//   - 只绑回环：stub 用 127.0.0.1:0，探测不产生任何外网流量；
//   - 只写隔离 agentDir：models.json 写在探测自己的临时目录里，绝不碰用户配置；
//   - 随探测结束关闭：失败/取消路径同样关闭（调用方 defer Close）。
//
// 这里刻意不复用 internal/fixtures/openaisse：那是测试夹具，产品代码依赖测试夹具
// 会让"探测装置"和"被测对象"共享同一份实现，将来夹具改了探测就悄悄变了。

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"my-agent-go/internal/contracts"
)

const (
	// contractStubProviderID 是探测专用 provider 的名字。
	//
	// 用一个不会与用户配置撞名的 id：即使某个用户的 models.json 里也有同名 provider，
	// 探测写在隔离 agentDir 里的这份才是被读的那份，撞名也不会串到真实配置。
	contractStubProviderID = "anvil-probe"
	// contractStubModelID 是探测专用模型名。
	contractStubModelID = "anvil-probe-model"
	// contractStubChunkDelay 是每个流式分片之间的间隔。
	//
	// 为什么不能立刻把所有分片吐完：abort 检查要求 abort 落在回合**进行中**。
	// 瞬时完成的回合会让 abort 变成"回合结束后再取消"，那不是被测 Pi 的缺陷。
	contractStubChunkDelay = 120 * time.Millisecond
	// contractStubChunks 是每回合的分片数（× 间隔 = 回合至少持续的时间）。
	contractStubChunks = 6
)

// contractStub 是一个只监听回环的 OpenAI 兼容端点。
type contractStub struct {
	ProviderID string
	ModelID    string
	URL        string
	server     *http.Server
	listener   net.Listener
}

// startContractStubProvider 在回环上起 stub，并把 models.json 写进隔离 agentDir。
func startContractStubProvider(agentDir string) (*contractStub, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeCompatibility,
			"契约探测无法在回环上启动 stub provider: "+err.Error(), false)
	}
	mux := http.NewServeMux()
	stub := &contractStub{
		ProviderID: contractStubProviderID,
		ModelID:    contractStubModelID,
		URL:        "http://" + ln.Addr().String(),
		listener:   ln,
	}
	mux.HandleFunc("/v1/chat/completions", stub.handleChat)
	mux.HandleFunc("/v1/models", stub.handleModels)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	stub.server = srv
	go func() { _ = srv.Serve(ln) }()

	if err := writeContractStubModels(agentDir, stub.URL); err != nil {
		_ = srv.Close()
		return nil, err
	}
	return stub, nil
}

// Close 关掉 stub（幂等）。
func (s *contractStub) Close() {
	if s == nil || s.server == nil {
		return
	}
	_ = s.server.Close()
}

// writeContractStubModels 把探测专用 provider 写进隔离 agentDir 的 models.json。
//
// 这是 Pi 的原生配置格式（与产品写 models.json 的口径一致）：探测只写这一份，
// 且只写在调用方给的隔离目录里（ContractProbe 已经强制要求非空 AgentDir）。
func writeContractStubModels(agentDir, baseURL string) error {
	doc := map[string]any{
		"$schema": "https://pi.dev/schemas/models.schema.json",
		"providers": map[string]any{
			contractStubProviderID: map[string]any{
				"baseUrl": baseURL + "/v1",
				"api":     "openai-completions",
				// 探测不发真实请求给任何第三方：这里只是让 Pi 认为有凭证。
				"apiKey": "anvil-contract-probe",
				"models": []map[string]any{{
					"id":            contractStubModelID,
					"name":          "Anvil contract probe",
					"input":         []string{"text"},
					"contextWindow": 8000,
					"maxTokens":     1024,
					"reasoning":     false,
					"cost":          map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
				}},
			},
		},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "序列化探测 models.json 失败: "+err.Error(), false)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建隔离 agentDir 失败: "+err.Error(), false)
	}
	path := filepath.Join(agentDir, "models.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写探测 models.json 失败: "+err.Error(), false)
	}
	return nil
}

// handleModels 让 Pi 能列出探测模型。
func (s *contractStub) handleModels(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": contractStubModelID, "object": "model", "owned_by": "anvil-contract-probe"},
		},
	})
}

// handleChat 回放一段可断言的最小流式回合（多分片 + 结束帧 + usage）。
func (s *contractStub) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	writeChunk := func(payload map[string]any) bool {
		raw, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for i := 0; i < contractStubChunks; i++ {
		// 分片之间保持间隔，让 abort 有机会落在回合进行中；
		// 客户端断开就立刻停（取消路径不该继续写）。
		select {
		case <-r.Context().Done():
			return
		case <-time.After(contractStubChunkDelay):
		}
		delta := map[string]any{"content": "ok"}
		if i == 0 {
			delta = map[string]any{"role": "assistant", "content": "ok"}
		}
		if !writeChunk(map[string]any{
			"id": "anvil-probe", "object": "chat.completion.chunk", "model": contractStubModelID,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}},
		}) {
			return
		}
	}

	if !writeChunk(map[string]any{
		"id": "anvil-probe", "object": "chat.completion.chunk", "model": contractStubModelID,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
	}) {
		return
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}
