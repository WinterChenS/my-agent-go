package mail

// SMTP/IMAP 的最小真实客户端。
//
// 不引第三方库的原因：这里只需要"连上、认证、发一封、读一封"，而**是否已派发**
// 的判断必须精确到"终止点已经写出去"这一步——只有自己写协议才能拿到这个事实。
// 库把 write 与 read 的失败揉成一个 error，正是 OutcomeUnknown 判定最需要的地方。

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/contracts"
)

// defaultTimeout 是单次协议交互的上限（调用方通常用更短的 ctx 截止时间）。
const defaultTimeout = 30 * time.Second

// outgoing 是一封待发送的邮件。
type outgoing struct {
	To      []string
	Subject string
	Body    string
	// IdempotencyKey 只写进 X-Anvil-Idempotency-Key：邮件协议本身没有幂等键，
	// 所以它只是相关性线索，不构成"可安全重放"。
	IdempotencyKey string
}

// mailSummary 是列表/搜索返回的单封摘要（不含正文）。
type mailSummary struct {
	UID     int    `json:"uid"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Date    string `json:"date"`
}

// mailAttachment 是附件元数据 + 内容（base64）。
type mailAttachment struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Size      int    `json:"size"`
	DataB64   string `json:"dataBase64"`
}

// mailDetail 是一封邮件的完整读取结果。
type mailDetail struct {
	mailSummary
	Body        string           `json:"body"`
	Attachments []mailAttachment `json:"attachments"`
}

// ---------------------------------------------------------------------------
// SMTP
// ---------------------------------------------------------------------------

// sendViaSMTP 发送一封邮件。
//
// 返回值 dispatched 表示"终止点（单独一行的 .）已经写给服务器"。为 true 时
// 后续任何错误都只能是 OutcomeUnknown——服务器可能已经接收了这封信。
func sendViaSMTP(ctx context.Context, endpoints connectors.Endpoints, password string, msg outgoing) (bool, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var (
		conn net.Conn
		err  error
	)
	if endpoints.Secure {
		conn, err = tls.DialWithDialer(dialer, "tcp", endpoints.SMTPAddr,
			&tls.Config{ServerName: hostOnly(endpoints.SMTPAddr), MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", endpoints.SMTPAddr)
	}
	if err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 连接失败: "+err.Error(), true)
	}
	defer func() { _ = conn.Close() }()
	applyDeadline(ctx, conn)

	tp := textproto.NewConn(conn)
	if _, _, err := tp.ReadResponse(220); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 问候失败: "+err.Error(), true)
	}
	host := hostOnly(endpoints.Username)
	if host == "" {
		host = "anvil.local"
	}
	if err := tp.PrintfLine("EHLO %s", host); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP EHLO 失败: "+err.Error(), true)
	}
	if _, _, err := tp.ReadResponse(250); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderError, "SMTP EHLO 被拒绝: "+err.Error(), true)
	}

	payload := base64.StdEncoding.EncodeToString([]byte("\x00" + endpoints.Username + "\x00" + password))
	if err := tp.PrintfLine("AUTH PLAIN %s", payload); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 认证写入失败: "+err.Error(), true)
	}
	code, msgText, err := tp.ReadResponse(235)
	if err != nil || code != 235 {
		detail := strings.TrimSpace(msgText)
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return false, contracts.NewAppError(contracts.CodeUnauthorized, "SMTP 认证失败: "+detail, false)
	}

	if err := tp.PrintfLine("MAIL FROM:<%s>", endpoints.From); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP MAIL FROM 写入失败: "+err.Error(), true)
	}
	if _, _, err := tp.ReadResponse(250); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderError, "SMTP 拒绝发件人: "+err.Error(), false)
	}
	for _, to := range msg.To {
		if err := tp.PrintfLine("RCPT TO:<%s>", to); err != nil {
			return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP RCPT TO 写入失败: "+err.Error(), true)
		}
		if _, _, err := tp.ReadResponse(250); err != nil {
			// 251 也表示用户非本地但会转发；ReadResponse(250) 对 251 会报错，
			// 这里退化为再次读取不可靠，因此把"拒绝收件人"当作确定失败（未派发正文）。
			return false, contracts.NewAppError(contracts.CodeProviderError, "SMTP 拒绝收件人 "+to+": "+err.Error(), false)
		}
	}
	if err := tp.PrintfLine("DATA"); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP DATA 写入失败: "+err.Error(), true)
	}
	if _, _, err := tp.ReadResponse(354); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderError, "SMTP 拒绝 DATA: "+err.Error(), false)
	}

	raw := buildMessage(endpoints.From, msg)
	if _, err := tp.W.Write(raw); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 正文写入失败: "+err.Error(), true)
	}
	// 终止点：写出去之后，服务器随时可能把信收下。
	if _, err := tp.W.WriteString(".\r\n"); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 终止点写入失败: "+err.Error(), true)
	}
	if err := tp.W.Flush(); err != nil {
		return false, contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 刷新失败: "+err.Error(), true)
	}
	if _, _, err := tp.ReadResponse(250); err != nil {
		return true, contracts.NewAppError(contracts.CodeRpcTimeout, "SMTP 未收到接收确认: "+err.Error(), true)
	}
	return true, nil
}

// probeSMTP 只做连接 + EHLO + AUTH，用于健康检查。
func probeSMTP(ctx context.Context, endpoints connectors.Endpoints, password string) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var (
		conn net.Conn
		err  error
	)
	if endpoints.Secure {
		conn, err = tls.DialWithDialer(dialer, "tcp", endpoints.SMTPAddr,
			&tls.Config{ServerName: hostOnly(endpoints.SMTPAddr), MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", endpoints.SMTPAddr)
	}
	if err != nil {
		return contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 连接失败: "+err.Error(), true)
	}
	defer func() { _ = conn.Close() }()
	applyDeadline(ctx, conn)
	tp := textproto.NewConn(conn)
	if _, _, err := tp.ReadResponse(220); err != nil {
		return contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 问候失败: "+err.Error(), true)
	}
	host := hostOnly(endpoints.Username)
	if host == "" {
		host = "anvil.local"
	}
	if err := tp.PrintfLine("EHLO %s", host); err != nil {
		return contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP EHLO 失败: "+err.Error(), true)
	}
	if _, _, err := tp.ReadResponse(250); err != nil {
		return contracts.NewAppError(contracts.CodeProviderError, "SMTP EHLO 被拒绝: "+err.Error(), true)
	}
	payload := base64.StdEncoding.EncodeToString([]byte("\x00" + endpoints.Username + "\x00" + password))
	if err := tp.PrintfLine("AUTH PLAIN %s", payload); err != nil {
		return contracts.NewAppError(contracts.CodeProviderUnreachable, "SMTP 认证写入失败: "+err.Error(), true)
	}
	if code, text, err := tp.ReadResponse(235); err != nil || code != 235 {
		detail := strings.TrimSpace(text)
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return contracts.NewAppError(contracts.CodeUnauthorized, "SMTP 认证失败: "+detail, false)
	}
	return nil
}

// buildMessage 生成 RFC 5322 文本（CRLF，点填充）。
func buildMessage(from string, msg outgoing) []byte {
	var b strings.Builder
	write := func(line string) { b.WriteString(line + "\r\n") }
	write("From: <" + from + ">")
	write("To: " + strings.Join(msg.To, ", "))
	write("Subject: " + encodeHeader(msg.Subject))
	write("Date: " + time.Now().Format(time.RFC1123Z))
	write("Message-ID: <" + messageID(from) + ">")
	if msg.IdempotencyKey != "" {
		write("X-Anvil-Idempotency-Key: " + msg.IdempotencyKey)
	}
	write("MIME-Version: 1.0")
	write("Content-Type: text/plain; charset=utf-8")
	write("Content-Transfer-Encoding: 8bit")
	write("")
	for _, line := range strings.Split(strings.ReplaceAll(msg.Body, "\r\n", "\n"), "\n") {
		// 点填充：正文里以 . 开头的行必须写成 ..，否则会被当成终止点。
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		write(line)
	}
	return []byte(b.String())
}

func messageID(from string) string {
	host := hostOnly(from)
	if host == "" {
		host = "anvil.local"
	}
	return fmt.Sprintf("%d.%s@%s", time.Now().UnixNano(), randomSuffix(), host)
}

func randomSuffix() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "0"
	}
	return fmt.Sprintf("%x", buf)
}

// encodeHeader 对非 ASCII 头做 RFC 2047 编码（真实邮件必须如此）。
func encodeHeader(v string) string {
	for _, r := range v {
		if r > 127 {
			return mime.BEncoding.Encode("utf-8", v)
		}
	}
	return v
}

// ---------------------------------------------------------------------------
// IMAP
// ---------------------------------------------------------------------------

// imapItem 是一行未标记响应 + 可能的内联字面量。
type imapItem struct {
	Line    string
	Literal []byte
}

type imapClient struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	tag  int
}

func dialIMAP(ctx context.Context, endpoints connectors.Endpoints, password string) (*imapClient, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var (
		conn net.Conn
		err  error
	)
	if endpoints.Secure {
		conn, err = tls.DialWithDialer(dialer, "tcp", endpoints.IMAPAddr,
			&tls.Config{ServerName: hostOnly(endpoints.IMAPAddr), MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", endpoints.IMAPAddr)
	}
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeProviderUnreachable, "IMAP 连接失败: "+err.Error(), true)
	}
	applyDeadline(ctx, conn)
	c := &imapClient{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}
	greeting, err := c.r.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, contracts.NewAppError(contracts.CodeProviderUnreachable, "IMAP 问候失败: "+err.Error(), true)
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(greeting)), "* OK") {
		_ = conn.Close()
		return nil, contracts.NewAppError(contracts.CodeProviderError, "IMAP 问候异常: "+strings.TrimSpace(greeting), true)
	}
	if err := c.login(endpoints.Username, password); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *imapClient) close() {
	if c == nil || c.conn == nil {
		return
	}
	_, _ = c.roundTrip("LOGOUT")
	_ = c.conn.Close()
}

func (c *imapClient) login(user, password string) error {
	if _, err := c.roundTrip(fmt.Sprintf("LOGIN %s %s", quoteIMAP(user), quoteIMAP(password))); err != nil {
		return contracts.NewAppError(contracts.CodeUnauthorized, "IMAP 登录失败: "+err.Error(), false)
	}
	return nil
}

func (c *imapClient) selectInbox() error {
	if _, err := c.roundTrip(`SELECT "INBOX"`); err != nil {
		return contracts.NewAppError(contracts.CodeProviderError, "IMAP 选择 INBOX 失败: "+err.Error(), false)
	}
	return nil
}

// roundTrip 发一条命令并读到自己的 tag 为止。
//
// 内联字面量（{n} + CRLF + n 字节）必须按长度读，不能按行读：按行读会把
// 二进制附件当成多行文本，得到"看起来成功、内容已经损坏"的结果。
func (c *imapClient) roundTrip(command string) ([]imapItem, error) {
	c.tag++
	tag := fmt.Sprintf("A%d", c.tag)
	if _, err := c.w.WriteString(tag + " " + command + "\r\n"); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	items := []imapItem{}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return items, err
		}
		line = strings.TrimRight(line, "\r\n")
		if n, ok := literalLength(line); ok {
			buf := make([]byte, n)
			if _, err := io.ReadFull(c.r, buf); err != nil {
				return items, err
			}
			items = append(items, imapItem{Line: line, Literal: buf})
			continue
		}
		if strings.HasPrefix(line, tag+" ") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, tag+" "))
			if strings.HasPrefix(strings.ToUpper(rest), "OK") {
				return items, nil
			}
			return items, fmt.Errorf("IMAP 命令 %q 返回: %s", command, rest)
		}
		items = append(items, imapItem{Line: line})
	}
}

// literalLength 解析行尾的 {n}。
func literalLength(line string) (int, bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, false
	}
	open := strings.LastIndex(line, "{")
	if open < 0 {
		return 0, false
	}
	n, err := parsePositiveInt(line[open+1 : len(line)-1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// listMessages 用一次 FETCH 取回全部信头并做客户端过滤。
//
// 说明：这里不做服务端 SEARCH。V1 的 mail 适配器只需要"小邮箱 + 确定性结果"，
// 客户端过滤让行为在六个平台上完全一致（各平台的 SEARCH 语义差异很大）。
// 代价是邮箱很大时会把信头都拉下来——这是 C08b 需要再权衡的已知限制。
func (c *imapClient) listMessages(query string, limit int) ([]mailSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	items, err := c.roundTrip("FETCH 1:* (UID BODY.PEEK[HEADER])")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeProviderError, "IMAP 读取信头失败: "+err.Error(), true)
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	out := []mailSummary{}
	for _, it := range items {
		if len(it.Literal) == 0 {
			continue
		}
		uid := uidFromLine(it.Line)
		summary, err := parseHeaders(uid, it.Literal)
		if err != nil {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(summary.Subject), needle) &&
			!strings.Contains(strings.ToLower(summary.From), needle) {
			continue
		}
		out = append(out, summary)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// readMessage 取回整封邮件并解析正文与附件。
func (c *imapClient) readMessage(uid int) (mailDetail, error) {
	items, err := c.roundTrip(fmt.Sprintf("UID FETCH %d (UID BODY.PEEK[])", uid))
	if err != nil {
		return mailDetail{}, contracts.NewAppError(contracts.CodeProviderError, "IMAP 读取邮件失败: "+err.Error(), true)
	}
	var raw []byte
	for _, it := range items {
		if len(it.Literal) > 0 {
			raw = it.Literal
			break
		}
	}
	if len(raw) == 0 {
		return mailDetail{}, contracts.NewAppError(contracts.CodeNotFound, fmt.Sprintf("找不到 UID %d 的邮件", uid), false)
	}
	return parseMessage(uid, raw)
}

func parseHeaders(uid int, raw []byte) (mailSummary, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return mailSummary{}, err
	}
	return mailSummary{
		UID:     uid,
		Subject: decodeHeader(msg.Header.Get("Subject")),
		From:    decodeHeader(msg.Header.Get("From")),
		Date:    strings.TrimSpace(msg.Header.Get("Date")),
	}, nil
}

func parseMessage(uid int, raw []byte) (mailDetail, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return mailDetail{}, contracts.NewAppError(contracts.CodeProviderError, "解析邮件失败: "+err.Error(), false)
	}
	detail := mailDetail{mailSummary: mailSummary{
		UID:     uid,
		Subject: decodeHeader(msg.Header.Get("Subject")),
		From:    decodeHeader(msg.Header.Get("From")),
		Date:    strings.TrimSpace(msg.Header.Get("Date")),
	}}
	mediaType, params := parseMediaType(msg.Header.Get("Content-Type"))
	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		reader := multipart.NewReader(msg.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return mailDetail{}, contracts.NewAppError(contracts.CodeProviderError, "解析 MIME 分段失败: "+err.Error(), false)
			}
			body, rerr := io.ReadAll(io.LimitReader(part, 8<<20))
			if rerr != nil {
				return mailDetail{}, contracts.NewAppError(contracts.CodeProviderError, "读取 MIME 分段失败: "+rerr.Error(), false)
			}
			// multipart.Part 不做传输解码：附件/正文可能是 base64 或 quoted-printable，
			// 不解码就会把编码后的文本当成内容交出去（"看起来成功、内容已损坏"）。
			body, derr := decodeTransfer(body, part.Header.Get("Content-Transfer-Encoding"))
			if derr != nil {
				return mailDetail{}, contracts.NewAppError(contracts.CodeProviderError, "解码 MIME 分段失败: "+derr.Error(), false)
			}
			partType, _ := parseMediaType(part.Header.Get("Content-Type"))
			filename := attachmentName(part)
			if filename != "" {
				detail.Attachments = append(detail.Attachments, mailAttachment{
					Name:      filename,
					MediaType: partType,
					Size:      len(body),
					DataB64:   base64.StdEncoding.EncodeToString(body),
				})
				continue
			}
			if strings.HasPrefix(partType, "text/plain") {
				detail.Body = string(body)
			}
		}
	default:
		body, err := io.ReadAll(io.LimitReader(msg.Body, 8<<20))
		if err != nil {
			return mailDetail{}, contracts.NewAppError(contracts.CodeProviderError, "读取正文失败: "+err.Error(), false)
		}
		body, derr := decodeTransfer(body, msg.Header.Get("Content-Transfer-Encoding"))
		if derr != nil {
			return mailDetail{}, contracts.NewAppError(contracts.CodeProviderError, "解码正文失败: "+derr.Error(), false)
		}
		detail.Body = string(body)
	}
	return detail, nil
}

// decodeTransfer 处理 MIME 的传输编码（base64 / quoted-printable / 7bit）。
// decodeTransfer 处理 MIME 的传输编码（base64 / quoted-printable / 7bit）。
func decodeTransfer(body []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		cleaned := strings.Map(func(r rune) rune {
			if r == 0x0a || r == 0x0d || r == 0x20 || r == 0x09 {
				return -1
			}
			return r
		}, string(body))
		out, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return nil, err
		}
		return out, nil
	case "quoted-printable":
		reader := quotedprintable.NewReader(strings.NewReader(string(body)))
		return io.ReadAll(reader)
	default:
		return body, nil
	}
}

func attachmentName(part *multipart.Part) string {
	if part.FileName() != "" {
		return decodeHeader(part.FileName())
	}
	// 有些客户端只写 filename 参数而没有 Content-Disposition: attachment。
	if _, params := parseMediaType(part.Header.Get("Content-Disposition")); params["filename"] != "" {
		return decodeHeader(params["filename"])
	}
	return ""
}

func parseMediaType(raw string) (string, map[string]string) {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0])), map[string]string{}
	}
	return strings.ToLower(mediaType), params
}

func decodeHeader(raw string) string {
	if raw == "" {
		return ""
	}
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(raw)
	if err != nil {
		return raw
	}
	return decoded
}

// uidFromLine 从 `* 1 FETCH (UID 101 BODY[HEADER] {n}` 里取 UID。
func uidFromLine(line string) int {
	fields := strings.Fields(line)
	for idx, f := range fields {
		if strings.EqualFold(strings.TrimLeft(f, "("), "UID") && idx+1 < len(fields) {
			if v, err := parsePositiveInt(fields[idx+1]); err == nil {
				return v
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func applyDeadline(ctx context.Context, conn net.Conn) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		return
	}
	_ = conn.SetDeadline(time.Now().Add(defaultTimeout))
}

func hostOnly(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func quoteIMAP(v string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
}

func parsePositiveInt(v string) (int, error) {
	n := 0
	if v == "" {
		return 0, fmt.Errorf("空数字")
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("不是数字: %s", v)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// marshalJSON 把读结果编码进 Result.Data（失败时不编造内容）。
func marshalJSON(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化读结果失败: "+err.Error(), false)
	}
	return raw, nil
}
