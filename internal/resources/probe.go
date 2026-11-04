package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// Capability 是三态能力：unknown 表示"这次没测"，不是"不支持"。
//
// 用字符串而不是 bool 是刻意的：把没测的能力写成 false（或写成 true）
// 都会让用户对模型产生一个我们并不掌握的判断。
type Capability string

const (
	CapabilityUnknown     Capability = "unknown"
	CapabilitySupported   Capability = "supported"
	CapabilityUnsupported Capability = "unsupported"
)

// ProbeResult 是一次连接测试的结果。
type ProbeResult struct {
	Reachable   bool       `json:"reachable"`
	ToolCalling Capability `json:"toolCalling"`
	Vision      Capability `json:"vision"`
	LatencyMS   int64      `json:"latencyMs"`
	ProviderID  string     `json:"providerId"`
	ModelID     string     `json:"modelId"`
	// Error 只在"确认失败"时出现；可达但某项能力没测时保持 nil。
	Error *contracts.AppError `json:"error,omitempty"`
}

// Prober 用临时 Pi 会话做连接测试。
//
// 为什么不在 Go 里自己发 HTTP：Pi 对各 API 的请求体、鉴权、流式解析都是
// 各自实现的，自己再写一遍等于测的是"我们的实现"而不是"将要运行的实现"。
// 这里起一个真实 Pi，用与生产相同的 models.json，只把工具与扩展关掉。
type Prober struct {
	root         string
	installation pi.PiInstallation
	timeout      time.Duration
}

// NewProber 构造连接测试器。root 是临时目录的父目录（放应用数据下，
// 避开系统临时目录在某些环境下的权限问题）。
func NewProber(root string, inst pi.PiInstallation) *Prober {
	return &Prober{root: root, installation: inst, timeout: 45 * time.Second}
}

// SetTimeout 覆盖单次探测的超时（测试与慢端点用）。
func (p *Prober) SetTimeout(d time.Duration) {
	if d > 0 {
		p.timeout = d
	}
}

const (
	// 最小提示：只要一个确定词，避免模型发挥导致不必要的 token 与延迟。
	probeTextPrompt = "Reply with exactly: PROBE_OK"
	// 工具测试需要模型真的发起一次工具调用；文件内容是判据。
	probeToolPrompt = "Use the read tool to read probe.txt in the current directory, then reply with its exact contents."
)

// Probe 对一条模型定义做连接测试：先测文本可用性，可用后再测工具调用。
//
// 用户点击才会调用，且界面必须提示"可能计费"：这是真实请求。
func (p *Prober) Probe(ctx context.Context, def ModelDefinition, secret string) ProbeResult {
	res := ProbeResult{
		ToolCalling: CapabilityUnknown,
		Vision:      CapabilityUnknown,
		ProviderID:  def.ProviderID,
		ModelID:     def.ModelID,
	}
	if strings.TrimSpace(p.installation.Executable) == "" {
		res.Error = contracts.NewAppError(contracts.CodeConfigMissing,
			"未找到可用的 Pi，无法做连接测试", false)
		return res
	}
	if err := os.MkdirAll(p.root, 0o700); err != nil {
		res.Error = contracts.NewAppError(contracts.CodeInternal, "创建测试目录失败: "+err.Error(), false)
		return res
	}
	dir, err := os.MkdirTemp(p.root, "probe-")
	if err != nil {
		res.Error = contracts.NewAppError(contracts.CodeInternal, "创建测试目录失败: "+err.Error(), false)
		return res
	}
	defer os.RemoveAll(dir)

	work := filepath.Join(dir, "work")
	agentDir := filepath.Join(dir, "agent")
	if err := os.MkdirAll(work, 0o700); err != nil {
		res.Error = contracts.NewAppError(contracts.CodeInternal, "创建测试工作目录失败: "+err.Error(), false)
		return res
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		res.Error = contracts.NewAppError(contracts.CodeInternal, "创建测试 agentDir 失败: "+err.Error(), false)
		return res
	}
	if err := os.WriteFile(filepath.Join(work, "probe.txt"), []byte("probe-ok"), 0o600); err != nil {
		res.Error = contracts.NewAppError(contracts.CodeInternal, "准备测试文件失败: "+err.Error(), false)
		return res
	}

	// 用与生产完全相同的编译路径生成 models.json：这样测过的就是会跑的那份。
	pm, err := compilePiModels([]ModelDefinition{def}, func(m ModelDefinition) (string, string, error) {
		if m.CredentialRef == "" {
			return "", "", nil
		}
		return CredentialEnvName(m.CredentialRef), secret, nil
	})
	if err != nil {
		res.Error = toAppError(err)
		return res
	}
	if err := WritePiModels(agentDir, pm); err != nil {
		res.Error = toAppError(err)
		return res
	}

	text := p.runPass(ctx, def, work, agentDir, pm.Env, probeTextPrompt, false)
	res.LatencyMS = text.latencyMS
	if text.err != nil {
		res.Error = text.err
		return res
	}
	res.Reachable = true

	tool := p.runPass(ctx, def, work, agentDir, pm.Env, probeToolPrompt, true)
	switch {
	case tool.err != nil:
		// 可达性已由文本一关确立；工具一关失败只说明"没测出来"。
		res.ToolCalling = CapabilityUnknown
	case tool.sawToolCall || tool.stopReason == "toolUse":
		res.ToolCalling = CapabilitySupported
	case tool.gotAssistant:
		res.ToolCalling = CapabilityUnsupported
	default:
		res.ToolCalling = CapabilityUnknown
	}
	// Vision 永远是 unknown：这里没有发图。想标 supported 必须先真的发一张图。
	return res
}

// passOutcome 是一趟探测的原始观察。
type passOutcome struct {
	gotAssistant bool
	stopReason   string
	sawToolCall  bool
	latencyMS    int64
	err          *contracts.AppError
}

// runPass 起一个临时 Pi，发一条最小提示，等到 settled 或超时。
func (p *Prober) runPass(ctx context.Context, def ModelDefinition, work, agentDir string, env []string, prompt string, withTools bool) passOutcome {
	args := append([]string{"--mode", "rpc"}, pi.IsolationSpec{}.Args()...)
	// 测试会话不留痕：不给 --session-dir，并显式 --no-session。
	args = append(args, "--no-session")
	if withTools {
		args = append(args, "--tools", "read")
	} else {
		args = append(args, "--no-tools")
	}
	args = append(args, "--provider", def.ProviderID, "--model", def.ModelID)

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	w, err := pi.StartWorker(callCtx, pi.WorkerSpec{
		Installation: p.installation,
		WorkingDir:   work,
		AgentDir:     agentDir,
		Env:          env,
		Args:         args,
	})
	if err != nil {
		return passOutcome{err: toAppError(err)}
	}
	defer func() {
		closeCtx, cc := context.WithTimeout(context.Background(), 15*time.Second)
		defer cc()
		_ = w.Close(closeCtx)
	}()

	// 关掉自动重试：429/5xx 自动重试会把"限流"测成"超时"，
	// 用户拿到的分类就不再是他需要修的那件事。
	retryOff := json.RawMessage(`{"type":"set_auto_retry","enabled":false}`)
	_, _ = w.Call(callCtx, retryOff)

	started := time.Now()
	msg, _ := json.Marshal(map[string]string{"type": "prompt", "message": prompt})
	if _, err := w.Call(callCtx, msg); err != nil {
		return passOutcome{err: toAppError(err)}
	}

	deadline := time.After(p.timeout)
	events := w.Events()
	for {
		select {
		case <-ctx.Done():
			return passOutcome{err: contracts.NewAppError(contracts.CodeRpcCancelled, "连接测试已取消", false)}
		case <-deadline:
			return passOutcome{err: contracts.NewAppError(contracts.CodeRpcTimeout,
				fmt.Sprintf("连接测试超时（%s 内没有拿到模型响应）", p.timeout), true)}
		case ev, ok := <-events:
			if !ok {
				// 事件流关闭而没 settled：进程退出。用 stderr 尾部做分类依据。
				tail, _ := w.StderrTail()
				code, known := w.ExitCode()
				return passOutcome{err: classifyExit(tail, code, known)}
			}
			if ev.Type == pi.TypeAgentSettled {
				return passOutcome{err: contracts.NewAppError(
					contracts.CodeProviderError, "Pi 在返回任何模型消息前本轮就结束了", false)}
			}
			if ev.Type != "message_end" {
				continue
			}
			var rec struct {
				Message struct {
					Role         string `json:"role"`
					StopReason   string `json:"stopReason"`
					ErrorMessage string `json:"errorMessage"`
					Content      []struct {
						Type string `json:"type"`
					} `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(ev.Record, &rec); err != nil {
				continue
			}
			if rec.Message.Role != "assistant" {
				continue
			}
			out := passOutcome{
				gotAssistant: true,
				stopReason:   rec.Message.StopReason,
				latencyMS:    time.Since(started).Milliseconds(),
			}
			for _, c := range rec.Message.Content {
				if c.Type == "toolCall" {
					out.sawToolCall = true
				}
			}
			if rec.Message.StopReason == "error" {
				out.err = classifyProviderError(rec.Message.ErrorMessage)
				return out
			}
			// 正常停下（stop/length/toolUse）：等到 settled 之前就返回没必要，
			// 但 latency 只统计到首个完整 assistant 消息，已经是用户能感知的部分。
			if rec.Message.StopReason == "aborted" {
				out.err = contracts.NewAppError(contracts.CodeProviderError, "本轮被中断，测试结论不可用", false)
				return out
			}
			// 继续等 settled：工具那一趟可能还要执行工具再产出最终消息。
			// 这里把已见到的结果暂存，settled 时返回。
			for {
				select {
				case <-ctx.Done():
					return out
				case <-deadline:
					// 已拿到完整 assistant 消息，超时不再算失败。
					return out
				case ev2, ok := <-events:
					if !ok {
						return out
					}
					if ev2.Type == pi.TypeAgentSettled {
						return out
					}
					if ev2.Type == "message_end" {
						if merged, ok := mergeAssistant(out, ev2); ok {
							out = merged
						}
					}
				}
			}
		}
	}
}

// mergeAssistant 把后续的 assistant 消息并入结果（宁可记下工具调用这个事实）。
func mergeAssistant(cur passOutcome, ev pi.RawEvent) (passOutcome, bool) {
	var rec struct {
		Message struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
			Content      []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(ev.Record, &rec); err != nil || rec.Message.Role != "assistant" {
		return cur, false
	}
	for _, c := range rec.Message.Content {
		if c.Type == "toolCall" {
			cur.sawToolCall = true
		}
	}
	if rec.Message.StopReason == "error" {
		cur.err = classifyProviderError(rec.Message.ErrorMessage)
	}
	return cur, true
}

// classifyProviderError 把 provider 的错误文本归到可执行的类别上。
//
// 分类的意义在于"用户下一步该做什么"：401 改凭证、429 等一会儿、
// 超时查网络或端点——三者混成"连接失败"等于让用户自己猜。
func classifyProviderError(msg string) *contracts.AppError {
	text := strings.ToLower(strings.TrimSpace(msg))
	if text == "" {
		return contracts.NewAppError(contracts.CodeProviderError, "provider 返回错误但未给出原因", false)
	}
	switch {
	case strings.Contains(text, "401"), strings.Contains(text, "403"),
		strings.Contains(text, "unauthorized"), strings.Contains(text, "forbidden"),
		strings.Contains(text, "invalid api key"), strings.Contains(text, "authentication"):
		return contracts.NewAppError(contracts.CodeUnauthorized, "鉴权失败：请检查凭证是否正确或已过期（"+clip(msg, 200)+"）", false)
	case strings.Contains(text, "429"), strings.Contains(text, "rate limit"),
		strings.Contains(text, "too many requests"), strings.Contains(text, "quota"):
		return contracts.NewAppError(contracts.CodeRateLimited, "被限流或额度不足，稍后重试（"+clip(msg, 200)+"）", true)
	case strings.Contains(text, "timeout"), strings.Contains(text, "timed out"),
		strings.Contains(text, "etimedout"), strings.Contains(text, "deadline exceeded"):
		return contracts.NewAppError(contracts.CodeRpcTimeout, "请求超时（"+clip(msg, 200)+"）", true)
	case strings.Contains(text, "enotfound"), strings.Contains(text, "econnrefused"),
		strings.Contains(text, "econnreset"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "network"), strings.Contains(text, "getaddrinfo"),
		strings.Contains(text, "socket hang up"), strings.Contains(text, "unreachable"):
		return contracts.NewAppError(contracts.CodeProviderUnreachable, "端点不可达：请检查 baseUrl 与网络（"+clip(msg, 200)+"）", true)
	default:
		return contracts.NewAppError(contracts.CodeProviderError, "provider 返回错误："+clip(msg, 300), false)
	}
}

// classifyExit 处理"Pi 进程直接退出、没有给结构化错误"的情况。
func classifyExit(stderr string, code int, known bool) *contracts.AppError {
	if tail := strings.TrimSpace(stderr); tail != "" {
		if classified := classifyProviderError(tail); classified.Code != contracts.CodeProviderError {
			return classified
		}
	}
	detail := "Pi 进程在返回模型响应前退出"
	if known {
		detail += fmt.Sprintf("（退出码 %d）", code)
	}
	tail := strings.TrimSpace(clip(stderr, 300))
	if tail != "" {
		detail += "：" + tail
	}
	return contracts.NewAppError(contracts.CodeProviderError, detail, false)
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// toAppError 把任意错误收敛成契约错误，保留原有 Code。
func toAppError(err error) *contracts.AppError {
	if err == nil {
		return nil
	}
	if ae, ok := err.(*contracts.AppError); ok {
		return ae
	}
	return contracts.NewAppError(contracts.CodeInternal, err.Error(), false)
}

// Secret 返回凭证原文；ref 为空表示该模型不需要凭证。
func (s *Service) Secret(ctx context.Context, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", nil
	}
	if s.creds == nil {
		return "", contracts.NewAppError(contracts.CodeCredentialStoreUnavailable,
			"没有可用的 OS 凭证库，无法读取凭证", false)
	}
	b, err := s.creds.Get(ctx, ref)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
