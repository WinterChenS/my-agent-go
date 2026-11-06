// Package session 的 C06e 部分：会话导出（html | markdown | json）。
//
// 权威边界
//   - 导出内容**只**来自 Pi transcript 文件（`<AgentDir>/sessions/<session id>.jsonl`），
//     不从内存里重建一份"对话模型"。transcript 的定位（哪个文件属于这个会话）复用
//     internal/history 的解析，本包不另写一套定位规则；
//   - 每一行 transcript 都进导出：可识别消息按 role/正文渲染，工具事件与其它
//     不可识别行以脱敏后的完整 JSON 保留——导出不做摘要，也不丢工具结果；
//   - 导出是只读 transcript：本包没有任何写 Pi 会话文件的代码路径。
//
// 转义与脱敏
//   - HTML 输出把正文/工具名/ID 全部当文本转义（html.EscapeString），模板之外不产生
//     任何标签或 URL 属性，所以 transcript 里的 `<script>`、`onerror=`、
//     `javascript:` 只能以字面文本出现；
//   - 脱敏复用产品既有规则（internal/redact，逐条来自 internal/runtime/pi/worker.go），
//     对所有写出去的字符串执行：正文、角色、事件 ID、工具名以及事件载荷里的每个
//     字符串值。事件 ID/工具名本身保留，故障仍然可定位。
//
// 写盘
//   - 目标必须落在导出根（生产为 `<appRoot>/exports`）内：`..` 段、UNC/设备路径、
//     备用数据流、越界绝对路径、经过符号链接/目录联接的路径一律结构化拒绝，
//     拒绝发生在读 transcript 与写盘之前；
//   - 写入顺序是"同目录临时文件 → fsync → rename"，因此目标文件只有"旧的完整版本"
//     和"新的完整版本"两种状态，不存在半份；失败路径不留临时文件。
//
// 不变式检查：可识别消息与 internal/history 的解析结果逐条比对（role + 正文）。
// 两个解析器一旦分叉，导出直接失败，而不是悄悄生成一份与产品其它视图不一致的文件。

package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/history"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/redact"
)

const (
	// ExportFormatHTML/Markdown/JSON 是允许的导出格式（接口契约里的 html|markdown|json）。
	ExportFormatHTML     = "html"
	ExportFormatMarkdown = "markdown"
	ExportFormatJSON     = "json"

	// exportFormatVersion 是 JSON 导出文档的格式版本。
	exportFormatVersion = 1

	// maxExportLineBytes 与 history 的单行上限同口径（8 MiB）：更长的行更可能是
	// 文件被弄坏了，宁可报错也不悄悄截断一半内容。
	maxExportLineBytes = 8 << 20

	// maxExportTranscriptBytes 是单次导出的 transcript 大小上限。导出在内存里组装
	// 整份文档，没有上限时一个异常大的文件就能把进程撑爆；超限是"拒绝"，不是截断。
	maxExportTranscriptBytes = 64 << 20

	// exportTempPrefix 是同目录临时文件前缀，便于在导出目录里一眼认出残留。
	exportTempPrefix = ".export-"

	// ExportKindMessage / ExportKindEvent 是导出记录的两种类型。
	ExportKindMessage = "message"
	ExportKindEvent   = "event"
)

// ExportResult 是一次导出的结果。Path 是真实写入路径（已解析链接、已在导出根内）。
type ExportResult struct {
	SessionID  string `json:"sessionId"`
	Format     string `json:"format"`
	Path       string `json:"path"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	Records    int    `json:"records"`
	Messages   int    `json:"messages"`
	Redactions int    `json:"redactions"`
	// TranscriptPath / TranscriptSHA256 是来源证据：导出内容属于哪份 Pi 文件、
	// 该文件当时的 hash 是多少。它们是定位信息，不复制正文。
	TranscriptPath   string    `json:"transcriptPath"`
	TranscriptSHA256 string    `json:"transcriptSha256"`
	WrittenAt        time.Time `json:"writtenAt"`
}

// exportRecord 是导出里的一条记录（一行 transcript）。
type exportRecord struct {
	Line      int             `json:"line"`
	Kind      string          `json:"kind"`
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Text      string          `json:"text,omitempty"`
	ToolName  string          `json:"toolName,omitempty"`
	EventID   string          `json:"eventId,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// exportDocument 是 JSON 导出的文档结构。
type exportDocument struct {
	Format           string         `json:"format"`
	FormatVersion    int            `json:"formatVersion"`
	SessionID        string         `json:"sessionId"`
	TranscriptPath   string         `json:"transcriptPath"`
	TranscriptSHA256 string         `json:"transcriptSha256"`
	ExportedAt       time.Time      `json:"exportedAt"`
	Redactions       int            `json:"redactions"`
	Records          []exportRecord `json:"records"`
}

// exportMeta 是三种格式共用的文档头信息。
type exportMeta struct {
	SessionID        string
	TranscriptPath   string
	TranscriptSHA256 string
	ExportedAt       time.Time
	Redactions       int
	Records          int
	Messages         int
}

// Exporter 是会话导出服务。并发安全（无可变状态，除测试用的时钟）。
type Exporter struct {
	hist  *history.Service
	root  string
	guard *policy.PathGuard
	now   func() time.Time
}

// NewExporter 构造导出服务。root 是导出根（生产为 `<appRoot>/exports`），
// 目标路径只能落在这个根内。根不存在会被创建（0o700）。
func NewExporter(hist *history.Service, root string) (*Exporter, error) {
	if hist == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "导出服务缺少 transcript 读取器", false)
	}
	if strings.TrimSpace(root) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "导出根目录为空", false)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "导出根目录无法绝对化: "+err.Error(), false)
	}
	abs = filepath.Clean(abs)
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建导出根目录失败: "+abs+" : "+err.Error(), false)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, contracts.NewAppError(contracts.CodeInternal, "导出根不是目录: "+abs, false)
	}
	// 越界判定复用 internal/policy 的 PathGuard：同一套链接解析与"真实路径"语义，
	// 不另写一套字符串前缀比较。
	guard, err := policy.NewPathGuard(abs)
	if err != nil {
		return nil, err
	}
	return &Exporter{hist: hist, root: abs, guard: guard, now: time.Now}, nil
}

// Root 返回导出根（只读定位，供绑定层与验收断言使用）。
func (e *Exporter) Root() string { return e.root }

// SetClock 注入时钟，仅用于测试确定性。
func (e *Exporter) SetClock(now func() time.Time) {
	if now != nil {
		e.now = now
	}
}

// ExportSession 把会话导出到 target。
//
// 顺序是刻意的：格式 → 目标路径 → transcript → 渲染 → 原子写。
// 前两步都不产生任何副作用，所以非法格式/越界目标不会读到 transcript，更不会写盘。
func (e *Exporter) ExportSession(ctx context.Context, sessionID string, format string, target string) (ExportResult, error) {
	if e == nil {
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "导出服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	normalized, err := normalizeExportFormat(format)
	if err != nil {
		return ExportResult{}, err
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return ExportResult{}, contracts.NewAppError(contracts.CodeConfigMissing, "缺少会话 ID，无法导出", false)
	}
	targetPath, err := e.resolveTarget(target)
	if err != nil {
		return ExportResult{}, err
	}

	tr, err := e.hist.Transcript(ctx, contracts.SessionID(sid))
	if err != nil {
		return ExportResult{}, err
	}
	raw, err := readTranscriptSource(tr.Path)
	if err != nil {
		return ExportResult{}, err
	}
	sum := sha256.Sum256(raw)
	meta := exportMeta{
		SessionID:        sid,
		TranscriptPath:   tr.Path,
		TranscriptSHA256: hex.EncodeToString(sum[:]),
		ExportedAt:       e.now().UTC(),
	}

	records, messages, redactions, err := buildExportRecords(raw, tr.Messages, meta.TranscriptSHA256)
	if err != nil {
		return ExportResult{}, err
	}
	meta.Records = len(records)
	meta.Messages = messages
	meta.Redactions = redactions

	// 文档头里的定位信息也走同一套脱敏（路径里通常带用户名）。
	meta.SessionID = textWithHits(&meta.Redactions, meta.SessionID)
	meta.TranscriptPath = textWithHits(&meta.Redactions, meta.TranscriptPath)

	doc, err := renderExport(normalized, meta, records)
	if err != nil {
		return ExportResult{}, err
	}
	if err := writeFileAtomic(targetPath, doc); err != nil {
		return ExportResult{}, err
	}
	outSum := sha256.Sum256(doc)
	return ExportResult{
		// 结果里的 SessionID 用调用方给的原值：它是目录键，不是 transcript 内容。
		SessionID:        sid,
		Format:           normalized,
		Path:             targetPath,
		Bytes:            len(doc),
		SHA256:           hex.EncodeToString(outSum[:]),
		Records:          meta.Records,
		Messages:         meta.Messages,
		Redactions:       meta.Redactions,
		TranscriptPath:   tr.Path,
		TranscriptSHA256: meta.TranscriptSHA256,
		WrittenAt:        meta.ExportedAt,
	}, nil
}

// normalizeExportFormat 只认三个格式（大小写与首尾空白不敏感）。
func normalizeExportFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case ExportFormatHTML:
		return ExportFormatHTML, nil
	case ExportFormatMarkdown:
		return ExportFormatMarkdown, nil
	case ExportFormatJSON:
		return ExportFormatJSON, nil
	default:
		// 点名允许的格式：前端要能直接把这句话显示给用户，不需要自己维护一份清单。
		return "", contracts.NewAppError(contracts.CodeUnsupported,
			"不支持的导出格式 "+format+"；允许的格式："+ExportFormatHTML+"、"+
				ExportFormatMarkdown+"、"+ExportFormatJSON, false)
	}
}

// resolveTarget 把 target 解析成"确实在导出根内的普通文件路径"。
//
// 拒绝顺序与 C06c 的附件同源：先词法（不依赖文件系统）、再链接（不跟随）、
// 再根内判定（policy.PathGuard 解析真实路径），最后才看目标本身。
func (e *Exporter) resolveTarget(target string) (string, error) {
	raw := strings.TrimSpace(target)
	if raw == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "导出目标路径为空", false)
	}
	if strings.ContainsRune(raw, 0) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "导出目标路径包含 NUL 字节", false)
	}
	if err := lexicalRejectAttachmentPath(raw); err != nil {
		return "", err
	}
	if hasDotDotSegment(raw) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标不允许用 .. 拼接（请使用导出目录下的名字）: "+raw, false)
	}
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(e.root, raw)
	}
	abs = filepath.Clean(abs)

	// 链接一律拒绝：导出是"用户点了一次导出"，没有任何理由经过链接写文件。
	// 逐段 Lstat 判定与 C06c 的附件同源（同一份实现，这里直接复用）。
	if link, err := firstLinkComponent(abs); err != nil {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标无法解析: "+abs+" : "+err.Error(), false)
	} else if link != "" {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标经过符号链接/目录联接，拒绝写入: "+link, false)
	}

	dec, err := e.guard.Check(abs)
	if err != nil {
		return "", err
	}

	// 父目录必须已存在：不根据用户给的名字顺手建目录树。
	dir := filepath.Dir(dec.Real)
	fi, err := os.Stat(dir)
	switch {
	case err == nil:
		if !fi.IsDir() {
			return "", contracts.NewAppError(contracts.CodeUnsupported,
				"导出目标的父路径不是目录: "+dir, false)
		}
	case errors.Is(err, fs.ErrNotExist):
		return "", contracts.NewAppError(contracts.CodeNotFound, "导出目标目录不存在: "+dir, false)
	default:
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标目录不可访问: "+dir+"（"+err.Error()+"）", false)
	}

	if fi, err := os.Lstat(dec.Real); err == nil {
		if fi.IsDir() {
			return "", contracts.NewAppError(contracts.CodeUnsupported,
				"导出目标是目录，不是文件: "+dec.Real, false)
		}
		if !fi.Mode().IsRegular() {
			return "", contracts.NewAppError(contracts.CodeUnsupported,
				"导出目标不是普通文件（管道/设备等不能作为导出目标）: "+dec.Real, false)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标不可访问: "+dec.Real+"（"+err.Error()+"）", false)
	}
	return dec.Real, nil
}

// readTranscriptSource 读入 transcript 原始字节（只读）。
//
// 上限定在 maxExportTranscriptBytes：超限是明确拒绝，不截断——半份 transcript
// 导出的"完整历史"比报错更危险。
func readTranscriptSource(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing,
			"transcript 不可读: "+path+": "+err.Error(), false)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "无法读取 transcript 元数据: "+path, false)
	}
	if !fi.Mode().IsRegular() {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "transcript 不是普通文件: "+path, false)
	}
	if fi.Size() > maxExportTranscriptBytes {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
			"transcript 超过导出上限（%d > %d 字节）: %s", fi.Size(), maxExportTranscriptBytes, path), false)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxExportTranscriptBytes+1))
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取 transcript 失败: "+path+": "+err.Error(), false)
	}
	if len(raw) > maxExportTranscriptBytes {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
			"transcript 超过导出上限（> %d 字节）: %s", maxExportTranscriptBytes, path), false)
	}
	return raw, nil
}

// exportEnvelope 是一行的通用外壳。
//
// 字段名覆盖 Pi 会话文件里常见的写法；认不出的行不猜语义，按原始 JSON 保留。
type exportEnvelope struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	EventID    string          `json:"eventId"`
	ToolCallID string          `json:"toolCallId"`
	MessageID  string          `json:"messageId"`
	ToolName   string          `json:"toolName"`
	Role       string          `json:"role"`
	Text       string          `json:"text"`
	Timestamp  string          `json:"timestamp"`
	Content    json.RawMessage `json:"content"`
	Message    *struct {
		Role    string          `json:"role"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// buildExportRecords 把 transcript 原文变成有序记录。
//
// 每一行都产生一条记录（含"看起来不像消息"的行）——导出不做摘要。
// 返回消息条数与脱敏命中总数。
func buildExportRecords(raw []byte, authoritative []history.Message, sourceSHA string) ([]exportRecord, int, int, error) {
	records := make([]exportRecord, 0, 32)
	messageIndexes := make([]int, 0, 8)
	redactions := 0

	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), maxExportLineBytes)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if strings.TrimSpace(text) == "" {
			continue
		}
		var env exportEnvelope
		if err := json.Unmarshal([]byte(text), &env); err != nil {
			// 不是 JSON 的行也要导出：作为 event 记录，载荷是脱敏后的原文字符串。
			payload, hits, perr := redactedJSONString(text)
			if perr != nil {
				return nil, 0, 0, perr
			}
			redactions += hits
			records = append(records, exportRecord{
				Line:    line,
				Kind:    ExportKindEvent,
				Type:    "unparsed",
				Payload: payload,
			})
			continue
		}

		rec := exportRecord{
			Line:      line,
			Type:      env.Type,
			ToolName:  env.ToolName,
			EventID:   firstNonEmpty(env.ID, env.EventID, env.ToolCallID, env.MessageID),
			Timestamp: env.Timestamp,
		}
		if role, body, ok := messageLine(env); ok {
			rec.Kind = ExportKindMessage
			rec.Role = role
			rec.Text = body
			messageIndexes = append(messageIndexes, len(records))
			// 消息正文只覆盖 text part；助手消息里的工具调用/结果 part 也要保留，
			// 否则"工具结果"就被导出悄悄丢掉了。
			if messageHasNonTextParts(env) {
				payload, hits, perr := redactRawJSON(text)
				if perr != nil {
					return nil, 0, 0, perr
				}
				redactions += hits
				rec.Payload = payload
			}
		} else {
			rec.Kind = ExportKindEvent
			if rec.Type == "" {
				rec.Type = "event"
			}
			payload, hits, perr := redactRawJSON(text)
			if perr != nil {
				return nil, 0, 0, perr
			}
			redactions += hits
			rec.Payload = payload
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, 0, contracts.NewAppError(contracts.CodeInternal,
			"解析 transcript 失败（来源 "+sourceSHA+"）: "+err.Error(), false)
	}

	// 不变式：可识别消息必须与 internal/history 的解析结果逐条一致。
	// 两个解析器分叉时导出直接失败——宁可没有文件，也不要一份与产品其它视图不一致的导出。
	if len(messageIndexes) != len(authoritative) {
		return nil, 0, 0, contracts.NewAppError(contracts.CodeInternal, fmt.Sprintf(
			"transcript 解析口径不一致：导出识别到 %d 条消息，history 识别到 %d 条，拒绝导出",
			len(messageIndexes), len(authoritative)), false)
	}
	for i, idx := range messageIndexes {
		if records[idx].Role != authoritative[i].Role || records[idx].Text != authoritative[i].Text {
			return nil, 0, 0, contracts.NewAppError(contracts.CodeInternal, fmt.Sprintf(
				"transcript 解析口径不一致：第 %d 条消息（行 %d）role/正文与 history 不一致，拒绝导出",
				i+1, records[idx].Line), false)
		}
	}

	// 定位信息与正文一样走脱敏：事件 ID/工具名保留，但不能因为"是 ID"就跳过规则。
	for i := range records {
		records[i].Role, _ = redact.Text(records[i].Role)
		records[i].Text = textWithHits(&redactions, records[i].Text)
		records[i].Type, _ = redact.Text(records[i].Type)
		records[i].ToolName, _ = redact.Text(records[i].ToolName)
		records[i].EventID, _ = redact.Text(records[i].EventID)
		records[i].Timestamp, _ = redact.Text(records[i].Timestamp)
	}
	return records, len(messageIndexes), redactions, nil
}

// textWithHits 脱敏并把命中数累加到计数器上。
func textWithHits(total *int, s string) string {
	out, n := redact.Text(s)
	*total += n
	return out
}

// messageLine 判定一行是否是 history 认得的消息形状，并取出 role 与正文。
//
// 判定与 internal/history/transcript.go 的 parseTranscript 同源（本任务不改 history，
// 故保留同口径；buildExportRecords 末尾会逐条比对结果，分叉即失败）。
func messageLine(env exportEnvelope) (string, string, bool) {
	switch {
	case env.Type == "message" && env.Message != nil && env.Message.Role != "":
		return env.Message.Role, exportMessageText(env.Message.Text, env.Message.Content), true
	case env.Role != "" && env.Message == nil:
		return env.Role, exportMessageText(env.Text, env.Content), true
	default:
		return "", "", false
	}
}

// exportMessageText 与 history.messageText 同源：优先纯文本字段，
// 其次 content（字符串或 [{type:"text",text:...}] 数组）。
func exportMessageText(text string, content json.RawMessage) string {
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

// messageHasNonTextParts 判断消息 content 里是否有正文之外的 part
// （工具调用、工具结果、图片等）。有则整行原样保留，避免"正文之外的内容"被丢掉。
func messageHasNonTextParts(env exportEnvelope) bool {
	content := env.Content
	if env.Type == "message" && env.Message != nil {
		content = env.Message.Content
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return false // content 不是数组（字符串 / {text:...}）：正文已覆盖
	}
	for _, p := range parts {
		if p.Type != "" && p.Type != "text" {
			return true
		}
	}
	return false
}

// redactRawJSON 把一行 JSON 变成脱敏后的载荷，分两步：
//
//  1. 先在**原始字节**上跑规则：JSON 解码会把键名与值拆成两个独立字符串，
//     只对解码后的值跑正则就漏掉 `"token":"..."` 这种相邻形态；
//  2. 再在**解码后的字符串值**上跑一遍：原文里 `"api_key": "..."` 的引号是
//     转义过的（`\\"api_key\\": \\"...`），第 1 步看不出键值关系，这一步补上。
//
// 第 1 步只在结果仍是合法 JSON 时采用：像 bearer 规则的 `\S+` 可能越过字符串边界
// 把引号一起吃掉，那样就退回原文，交给第 2 步兜底——宁可少脱敏一次也不能产出
// 一份解析不了的导出。
func redactRawJSON(line string) (json.RawMessage, int, error) {
	hits := 0
	candidate := line
	if rawRedacted, n := redact.Text(line); json.Valid([]byte(rawRedacted)) {
		candidate = rawRedacted
		hits = n
	}

	dec := json.NewDecoder(strings.NewReader(candidate))
	dec.UseNumber() // 保留数字原始写法，避免大整数被 float64 改写
	var value any
	if err := dec.Decode(&value); err != nil {
		return redactedJSONString(line)
	}
	redacted, extra := redactAny(value)
	out, err := json.Marshal(redacted)
	if err != nil {
		return nil, 0, contracts.NewAppError(contracts.CodeInternal, "重新编码导出载荷失败: "+err.Error(), false)
	}
	return json.RawMessage(out), hits + extra, nil
}

// redactedJSONString 把"无法当 JSON 解析"的行按纯文本脱敏后作为 JSON 字符串保留。
func redactedJSONString(line string) (json.RawMessage, int, error) {
	redacted, hits := redact.Text(line)
	out, err := json.Marshal(redacted)
	if err != nil {
		return nil, 0, contracts.NewAppError(contracts.CodeInternal, "编码导出载荷失败: "+err.Error(), false)
	}
	return json.RawMessage(out), hits, nil
}

// redactAny 递归脱敏 JSON 值里的所有字符串（键名是结构，不动）。
func redactAny(v any) (any, int) {
	switch t := v.(type) {
	case string:
		return redact.Text(t)
	case []any:
		hits := 0
		for i := range t {
			var n int
			t[i], n = redactAny(t[i])
			hits += n
		}
		return t, hits
	case map[string]any:
		hits := 0
		for k, val := range t {
			var n int
			t[k], n = redactAny(val)
			hits += n
		}
		return t, hits
	default:
		return v, 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

// renderExport 按格式渲染导出文档。
func renderExport(format string, meta exportMeta, records []exportRecord) ([]byte, error) {
	switch format {
	case ExportFormatJSON:
		return renderJSONExport(meta, records)
	case ExportFormatMarkdown:
		return renderMarkdownExport(meta, records), nil
	case ExportFormatHTML:
		return renderHTMLExport(meta, records), nil
	default:
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "不支持的导出格式 "+format, false)
	}
}

func renderJSONExport(meta exportMeta, records []exportRecord) ([]byte, error) {
	doc := exportDocument{
		Format:           ExportFormatJSON,
		FormatVersion:    exportFormatVersion,
		SessionID:        meta.SessionID,
		TranscriptPath:   meta.TranscriptPath,
		TranscriptSHA256: meta.TranscriptSHA256,
		ExportedAt:       meta.ExportedAt,
		Redactions:       meta.Redactions,
		Records:          records,
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "编码 JSON 导出失败: "+err.Error(), false)
	}
	return append(out, '\n'), nil
}

func renderMarkdownExport(meta exportMeta, records []exportRecord) []byte {
	var b strings.Builder
	b.WriteString("# Anvil 会话导出\n\n")
	fmt.Fprintf(&b, "- 会话：%s\n", mdEscape(meta.SessionID))
	fmt.Fprintf(&b, "- 来源：Pi transcript `%s`\n", strings.ReplaceAll(mdEscape(meta.TranscriptPath), "`", "'"))
	fmt.Fprintf(&b, "- transcript sha256：`%s`\n", meta.TranscriptSHA256)
	fmt.Fprintf(&b, "- 导出时间：%s\n", meta.ExportedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- 记录：%d 条（其中消息 %d 条）\n", meta.Records, meta.Messages)
	fmt.Fprintf(&b, "- 脱敏命中：%d 处\n\n", meta.Redactions)
	b.WriteString("> 本文件由 Pi transcript 原样生成：正文是纯文本，工具与事件以 JSON 保留，没有摘要。\n\n")

	for _, rec := range records {
		b.WriteString("---\n\n")
		if rec.Kind == ExportKindMessage {
			fmt.Fprintf(&b, "## %s（第 %d 行）\n\n", mdEscape(rec.Role), rec.Line)
			if rec.Text != "" {
				b.WriteString(mdEscape(rec.Text))
				b.WriteString("\n\n")
			}
		} else {
			fmt.Fprintf(&b, "## 事件 %s", mdEscape(rec.Type))
			if rec.ToolName != "" {
				fmt.Fprintf(&b, " · 工具 %s", mdEscape(rec.ToolName))
			}
			if rec.EventID != "" {
				fmt.Fprintf(&b, " · id %s", mdEscape(rec.EventID))
			}
			if rec.Timestamp != "" {
				fmt.Fprintf(&b, " · %s", mdEscape(rec.Timestamp))
			}
			fmt.Fprintf(&b, "（第 %d 行）\n\n", rec.Line)
		}
		if len(rec.Payload) > 0 {
			b.WriteString(mdFenced(string(rec.Payload)))
		}
	}
	return []byte(b.String())
}

// mdEscape 只转义会让 Markdown 变成 HTML 的两个字符。
//
// 导出是文本格式，但一旦被"Markdown 当 HTML 渲染"就会执行脚本。GitHub 风格渲染器
// 对 `&lt;` 显示为 `<`，所以转义不损失可读性，却让 `<script>` 永远进不了 HTML 位置。
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	return strings.ReplaceAll(s, "<", "&lt;")
}

// mdFenced 用"比内容里最长反引号串更长"的围栏包住载荷，避免提前闭合。
func mdFenced(body string) string {
	fence := strings.Repeat("`", maxBacktickRun(body)+1)
	if len(fence) < 3 {
		fence = "```"
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return fence + "json\n" + body + fence + "\n\n"
}

func maxBacktickRun(s string) int {
	best, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > best {
				best = run
			}
			continue
		}
		run = 0
	}
	return best
}

// htmlExportStyle 是导出文档的样式表。
//
// 它只包含静态声明：没有 `>` 选择器、没有 `="`，所以导出的标签/属性集合可以
// 被测试逐项白名单校验（见 export_test.go 的 assertExportHTMLHasNoActiveContent）。
const htmlExportStyle = `body { font-family: system-ui, -apple-system, "Segoe UI", sans-serif; margin: 2rem auto; max-width: 52rem; line-height: 1.6; color: #111; }
h1 { font-size: 1.35rem; margin-bottom: .5rem; }
h2 { font-size: .9rem; color: #555; margin: 0 0 .4rem 0; }
dl.meta { font-size: .85rem; color: #444; margin: 0; }
dl.meta dt { font-weight: 600; margin-top: .25rem; }
dl.meta dd { margin: 0 0 0 .0rem; }
article.record { border-top: 1px solid #ddd; padding: .75rem 0; }
pre { white-space: pre-wrap; word-break: break-word; background: #f6f6f6; padding: .6rem; margin: 0; overflow-x: auto; }`

func renderHTMLExport(meta exportMeta, records []exportRecord) []byte {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString("<html lang=\"zh-CN\">\n")
	b.WriteString("<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	fmt.Fprintf(&b, "<title>Anvil 会话导出 · %s</title>\n", esc(meta.SessionID))
	b.WriteString("<style>\n")
	b.WriteString(htmlExportStyle)
	b.WriteString("\n</style>\n")
	b.WriteString("</head>\n")
	b.WriteString("<body>\n")
	b.WriteString("<header>\n")
	b.WriteString("<h1>Anvil 会话导出</h1>\n")
	b.WriteString("<dl class=\"meta\">\n")
	fmt.Fprintf(&b, "<dt>会话</dt><dd>%s</dd>\n", esc(meta.SessionID))
	fmt.Fprintf(&b, "<dt>来源</dt><dd>Pi transcript %s</dd>\n", esc(meta.TranscriptPath))
	fmt.Fprintf(&b, "<dt>transcript sha256</dt><dd>%s</dd>\n", esc(meta.TranscriptSHA256))
	fmt.Fprintf(&b, "<dt>导出时间</dt><dd>%s</dd>\n", esc(meta.ExportedAt.Format(time.RFC3339)))
	fmt.Fprintf(&b, "<dt>记录</dt><dd>%d 条（其中消息 %d 条）</dd>\n", meta.Records, meta.Messages)
	fmt.Fprintf(&b, "<dt>脱敏命中</dt><dd>%d 处</dd>\n", meta.Redactions)
	b.WriteString("</dl>\n")
	b.WriteString("</header>\n")
	b.WriteString("<main>\n")

	for _, rec := range records {
		if rec.Kind == ExportKindMessage {
			fmt.Fprintf(&b, "<article class=\"record message\" data-line=\"%d\">\n", rec.Line)
			fmt.Fprintf(&b, "<h2>%s · 第 %d 行</h2>\n", esc(rec.Role), rec.Line)
			fmt.Fprintf(&b, "<pre>%s</pre>\n", esc(rec.Text))
		} else {
			fmt.Fprintf(&b, "<article class=\"record event\" data-line=\"%d\">\n", rec.Line)
			title := rec.Type
			if rec.ToolName != "" {
				title += " · " + rec.ToolName
			}
			if rec.EventID != "" {
				title += " · id " + rec.EventID
			}
			fmt.Fprintf(&b, "<h2>%s · 第 %d 行</h2>\n", esc(title), rec.Line)
			if len(rec.Payload) > 0 {
				fmt.Fprintf(&b, "<pre class=\"payload\">%s</pre>\n", esc(string(rec.Payload)))
			}
		}
		b.WriteString("</article>\n")
	}
	b.WriteString("</main>\n")
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// 原子写
// ---------------------------------------------------------------------------

// writeFileAtomic 把 data 写进 target：同目录临时文件 → fsync → rename。
//
// 目标只有"旧的完整版本"和"新的完整版本"两种状态：rename 在同卷上是原子的，
// 而中途失败只会留下一个被删除的临时文件，不会留下半份导出。
func writeFileAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, exportTempPrefix+"*.tmp")
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建导出临时文件失败: "+err.Error(), false)
	}
	tmpPath := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(data); err != nil {
		discard()
		return contracts.NewAppError(contracts.CodeInternal, "写入导出临时文件失败: "+err.Error(), false)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return contracts.NewAppError(contracts.CodeInternal, "刷新导出临时文件失败: "+err.Error(), false)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return contracts.NewAppError(contracts.CodeInternal, "关闭导出临时文件失败: "+err.Error(), false)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return contracts.NewAppError(contracts.CodeInternal, "发布导出文件失败: "+err.Error(), false)
	}
	return nil
}
