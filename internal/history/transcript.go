package history

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"my-agent-go/internal/contracts"
)

// maxTranscriptLine 是单行上限。Pi 的一条消息可以很大（长工具结果），
// 但 8MB 之上的行更可能是文件被别的程序弄坏了：那时宁可报解析错误，
// 也不要悄悄截断一半内容。
const maxTranscriptLine = 8 << 20

// Transcript 读取某个会话的 Pi transcript（只读）。
//
// 定位规则：扫描 <AgentDir>/sessions/*.jsonl，先按文件名匹配 session id，
// 再按首行可解析出的 sessionId / session_id 字段匹配。两者都没有就返回
// CONFIG_MISSING——找不到 transcript 时编一个空结果，等于把"这个会话没有历史"
// 和"我们没找到历史"说成同一件事。
func (s *Service) Transcript(ctx context.Context, sessionID contracts.SessionID) (Transcript, error) {
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	if strings.TrimSpace(string(sessionID)) == "" {
		return Transcript{}, contracts.NewAppError(contracts.CodeConfigMissing, "缺少 session id，无法定位 transcript", false)
	}
	if s.opts.AgentDir == "" {
		return Transcript{}, contracts.NewAppError(contracts.CodeConfigMissing, "未指定 Pi agentDir，无法定位 transcript", false)
	}

	path, err := s.resolveTranscript(ctx, sessionID)
	if err != nil {
		return Transcript{}, err
	}

	// 只读打开：产品永远不写 Pi 的会话文件（那是独立权威）。
	f, err := os.Open(path)
	if err != nil {
		return Transcript{}, contracts.NewAppError(contracts.CodeConfigMissing,
			"transcript 不可读: "+path+": "+err.Error(), false)
	}
	defer f.Close()

	messages, skipped, err := parseTranscript(f)
	if err != nil {
		return Transcript{}, contracts.NewAppError(contracts.CodeInternal,
			"解析 transcript 失败: "+path+": "+err.Error(), false)
	}
	return Transcript{
		SessionID: sessionID,
		Path:      path,
		Messages:  messages,
		Skipped:   skipped,
	}, nil
}

// resolveTranscript 返回该会话的会话文件路径（locator）。
func (s *Service) resolveTranscript(ctx context.Context, sessionID contracts.SessionID) (string, error) {
	dir := filepath.Join(s.opts.AgentDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeConfigMissing,
			"Pi transcript 目录不可读: "+dir+": "+err.Error(), false)
	}
	// ReadDir 已按文件名排序，因此"多个候选"时有稳定结论。
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	want := strings.TrimSpace(string(sessionID))
	var candidates []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if strings.TrimSuffix(e.Name(), ".jsonl") == want {
			return filepath.Join(dir, e.Name()), nil
		}
		candidates = append(candidates, filepath.Join(dir, e.Name()))
	}

	for _, path := range candidates {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if id, ok := firstLineSessionID(path); ok && id == want {
			return path, nil
		}
	}

	return "", contracts.NewAppError(contracts.CodeConfigMissing,
		"未找到会话 "+want+" 的 Pi transcript（目录 "+dir+"）", false)
}

// firstLineSessionID 读首行可解析出的 sessionId / session_id。
//
// 只读第一行：Pi 的会话头在最前面，逐行扫完整份文件只为定位会让
// "读历史"变成"读全部历史"。首行不是会话头（没有这个字段）就返回 false，
// 不猜。
func firstLineSessionID(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxTranscriptLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var probe struct {
			SessionID  string `json:"sessionId"`
			SessionID2 string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			return "", false
		}
		if probe.SessionID != "" {
			return probe.SessionID, true
		}
		if probe.SessionID2 != "" {
			return probe.SessionID2, true
		}
		return "", false
	}
	return "", false
}

// transcriptLine 是逐行解析的探针。
//
// 两种可识别形状都收在一个结构里，避免"先猜形状再解析"造成两套字段口径。
type transcriptLine struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
	Message *struct {
		Role    string          `json:"role"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// parseTranscript 解析 transcript 行，返回消息与跳过计数。
//
// 容错口径（写进包文档的同一份）：
//   - 空行既不是消息也不是"内容"，不计入 Skipped；
//   - 非 JSON、或不是上述两种消息形状的行，计入 Skipped；
//   - 认得形状但没有正文（例如纯 tool_call 助手消息）仍然是一条消息，
//     Text 为空——把它算成"跳过"会让消息条数与 Pi 的 UI 对不上。
func parseTranscript(r *os.File) ([]Message, int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxTranscriptLine)

	messages := make([]Message, 0, 16)
	skipped := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var probe transcriptLine
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			skipped++
			continue
		}
		switch {
		case probe.Type == "message" && probe.Message != nil && probe.Message.Role != "":
			messages = append(messages, Message{
				Role: probe.Message.Role,
				Text: messageText(probe.Message.Text, probe.Message.Content),
			})
		case probe.Role != "" && probe.Message == nil:
			// 裸 {"role":...}：没有 type:message 外壳的旧/简写形状。
			messages = append(messages, Message{
				Role: probe.Role,
				Text: messageText(probe.Text, probe.Content),
			})
		default:
			skipped++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	return messages, skipped, nil
}

// messageText 取消息正文：优先纯文本字段，其次 content（字符串或
// [{type:"text",text:...}] 数组）。
func messageText(text string, content json.RawMessage) string {
	if strings.TrimSpace(text) != "" {
		return text
	}
	if len(content) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return asString
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type != "" && p.Type != "text" {
				continue
			}
			b.WriteString(p.Text)
		}
		return b.String()
	}
	var single struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &single); err == nil {
		return single.Text
	}
	return ""
}
