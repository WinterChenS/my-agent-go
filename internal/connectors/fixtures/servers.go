// Package fixtures 提供确定性的本地协议服务器：SMTP、IMAP 与一个 OAuth 令牌端点。
//
// 它们是**外部 oracle**：测试进程只能通过这里暴露的计数器与消息记录来证明
// "副作用发生了 0 次还是 1 次"，不能靠"被测代码说自己没发出去"。
//
// 纪律：
//   - 只绑 127.0.0.1 的随机端口，Close 幂等；
//   - 计数器在每次变化后原子写进 $ANVIL_OBSERVATION_DIR/connector-counter.json
//     （未设置该变量时只在内存里，便于本地 focused 运行）；
//   - 读动作不增加写计数：搜索/读取只动 MailSearched/MailRead。
package fixtures

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/connectors/auth"
)

// ObservationDirEnv 与连接器包保持一致（同一个 runner 契约）。
const ObservationDirEnv = "ANVIL_OBSERVATION_DIR"

// CounterFile 是副作用计数快照的文件名。
const CounterFile = "connector-counter.json"

// Snapshot 是一次计数快照。
//
// 字段刻意分平台可读，而不是一个总数：CONN02 要求"每个平台的写/外发动作各自
// 计数"，把它们加总就分不出是谁发生了副作用。
type Snapshot struct {
	Connections       int `json:"connections"`
	MailAccepted      int `json:"mailAccepted"` // SMTP 完整接收并确认的消息数
	MailRejected      int `json:"mailRejected"`
	MailAuthFailed    int `json:"mailAuthFailed"`
	MailRead          int `json:"mailRead"`     // IMAP 完整取回一封信
	MailSearched      int `json:"mailSearched"` // IMAP 取回信头（搜索）
	OAuthTokensIssued int `json:"oauthTokensIssued"`
	OAuthCodeRejected int `json:"oauthCodeRejected"`
}

// ExternalWrites 是"已经对平台产生写副作用"的次数。
func (s Snapshot) ExternalWrites() int { return s.MailAccepted }

// Counter 是副作用计数器。
type Counter struct {
	mu   sync.Mutex
	dir  string
	snap Snapshot
}

// NewCounter 创建计数器；dir 非空时每次变化都落盘。
func NewCounter(dir string) *Counter {
	return &Counter{dir: strings.TrimSpace(dir)}
}

// Dir 返回观察目录（可能为空）。
func (c *Counter) Dir() string { return c.dir }

// Update 在锁内修改并持久化。
func (c *Counter) Update(fn func(*Snapshot)) {
	c.mu.Lock()
	fn(&c.snap)
	snap := c.snap
	dir := c.dir
	c.mu.Unlock()
	if dir == "" {
		return
	}
	_ = writeSnapshot(dir, snap)
}

// Snapshot 返回当前计数副本。
func (c *Counter) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap
}

// Persist 把当前快照写进观察目录（Close 时调用；没有目录则什么也不做）。
func (c *Counter) Persist() error {
	c.mu.Lock()
	snap := c.snap
	dir := c.dir
	c.mu.Unlock()
	if dir == "" {
		return nil
	}
	return writeSnapshot(dir, snap)
}

// writeSnapshot 原子写（临时文件 + rename）：并发读到半截 JSON 会让 oracle 判死。
func writeSnapshot(dir string, snap Snapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, CounterFile+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, filepath.Join(dir, CounterFile))
}

// ObservationDir 返回环境变量里的观察目录。
func ObservationDir() string { return strings.TrimSpace(os.Getenv(ObservationDirEnv)) }

// Options 是 fixture 服务器的公共选项。
type Options struct {
	// ObservationDir 覆盖 $ANVIL_OBSERVATION_DIR。
	ObservationDir string
	// Username / Password 是唯一被接受的认证凭据（空 Password 表示不校验）。
	Username string
	Password string
	// StallAfterData 让 SMTP 在收完 DATA 后不返回 250：消息已经被接收，
	// 但客户端只会看到超时——这正是 OutcomeUnknown 的现实来源。
	StallAfterData bool
	// CodeTTL 是 OAuth 授权码有效期，默认 60 秒；负值表示签发即过期。
	CodeTTL time.Duration
	// Counter 是共享计数器：SMTP/IMAP/OAuth 三个 fixture 应该写同一份快照，
	// 否则后写入者会覆盖前一个的数字（观察目录只有一个 connector-counter.json）。
	Counter *Counter
}

// counter 返回共享计数器，没给就新建一个。
func (o Options) counter() *Counter {
	if o.Counter != nil {
		return o.Counter
	}
	return NewCounter(o.dir())
}

func (o Options) dir() string {
	if strings.TrimSpace(o.ObservationDir) != "" {
		return strings.TrimSpace(o.ObservationDir)
	}
	return ObservationDir()
}

func (o Options) codeTTL() time.Duration {
	if o.CodeTTL == 0 {
		return time.Minute
	}
	return o.CodeTTL
}

// ---------------------------------------------------------------------------
// SMTP
// ---------------------------------------------------------------------------

// MailMessage 是 fixture 收到的消息（断言内容用）。
type MailMessage struct {
	From    string
	To      []string
	Subject string
	Body    string
}

// SMTP 是本地 SMTP fixture。
type SMTP struct {
	ln      net.Listener
	opts    Options
	counter *Counter
	done    chan struct{}
	wg      sync.WaitGroup

	mu       sync.Mutex
	accepted []MailMessage
	closed   bool
}

// NewSMTP 在回环随机端口上启动 SMTP fixture。
func NewSMTP(opts Options) (*SMTP, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fixtures: SMTP 监听失败: %w", err)
	}
	s := &SMTP{
		ln:      ln,
		opts:    opts,
		counter: opts.counter(),
		done:    make(chan struct{}),
	}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Addr 返回 host:port。
func (s *SMTP) Addr() string { return s.ln.Addr().String() }

// Counter 返回副作用计数器。
func (s *SMTP) Counter() *Counter { return s.counter }

// Accepted 返回已被接收的消息。
func (s *SMTP) Accepted() []MailMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MailMessage, len(s.accepted))
	copy(out, s.accepted)
	return out
}

// Close 关闭监听并等待连接退出（幂等）。
func (s *SMTP) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	err := s.ln.Close()
	s.wg.Wait()
	_ = s.counter.Persist()
	return err
}

func (s *SMTP) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			return
		}
		s.counter.Update(func(sn *Snapshot) { sn.Connections++ })
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = conn.Close() }()
			s.handle(conn)
		}()
	}
}

func (s *SMTP) handle(conn net.Conn) {
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	send := func(line string) error {
		if _, err := w.WriteString(line + "\r\n"); err != nil {
			return err
		}
		return w.Flush()
	}
	_ = send("220 fixture-smtp ready")

	var (
		authed     = strings.TrimSpace(s.opts.Password) == ""
		from       string
		recipients []string
	)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.Fields(line + " ")[0])
		switch verb {
		case "EHLO":
			_ = send("250-fixture-smtp")
			_ = send("250-AUTH PLAIN LOGIN")
			_ = send("250 OK")
		case "HELO":
			_ = send("250 fixture-smtp")
		case "AUTH":
			fields := strings.Fields(line)
			if len(fields) < 2 {
				_ = send("501 缺少认证机制")
				continue
			}
			mech := strings.ToUpper(fields[1])
			var user, pass string
			switch {
			case mech == "PLAIN" && len(fields) >= 3:
				user, pass = decodePlain(fields[2])
			case mech == "PLAIN":
				_ = send("334 ")
				payload, rerr := r.ReadString('\n')
				if rerr != nil {
					return
				}
				user, pass = decodePlain(strings.TrimSpace(payload))
			case mech == "LOGIN":
				_ = send("334 VXNlcm5hbWU6")
				ub, rerr := r.ReadString('\n')
				if rerr != nil {
					return
				}
				_ = send("334 UGFzc3dvcmQ6")
				pb, rerr := r.ReadString('\n')
				if rerr != nil {
					return
				}
				user = decodeB64(ub)
				pass = decodeB64(pb)
			default:
				_ = send("504 不支持的认证机制")
				continue
			}
			if s.checkAuth(user, pass) {
				authed = true
				_ = send("235 认证通过")
			} else {
				s.counter.Update(func(sn *Snapshot) { sn.MailAuthFailed++ })
				_ = send("535 认证失败")
			}
		case "MAIL":
			if !authed {
				_ = send("530 需要先认证")
				continue
			}
			from = addressOf(line)
			_ = send("250 OK")
		case "RCPT":
			if !authed {
				_ = send("530 需要先认证")
				continue
			}
			recipients = append(recipients, addressOf(line))
			_ = send("250 OK")
		case "DATA":
			if !authed || from == "" || len(recipients) == 0 {
				_ = send("503 顺序不对")
				continue
			}
			_ = send("354 开始输入，以单个 . 结束")
			var body strings.Builder
			for {
				dl, derr := r.ReadString('\n')
				if derr != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				body.WriteString(dl)
			}
			msg := MailMessage{From: from, To: append([]string{}, recipients...)}
			msg.Subject, msg.Body = parseMessage(body.String())
			// 先记副作用，再决定是否回复：StallAfterData 模拟"平台已经收下，
			// 但客户端拿不到确认"，此时 oracle 必须是 1。
			s.mu.Lock()
			s.accepted = append(s.accepted, msg)
			s.mu.Unlock()
			s.counter.Update(func(sn *Snapshot) { sn.MailAccepted++ })
			if s.opts.StallAfterData {
				// 卡住直到连接被对端/服务关闭：不回复 250。
				<-s.done
				return
			}
			from = ""
			recipients = nil
			_ = send("250 已接收")
		case "RSET":
			from = ""
			recipients = nil
			_ = send("250 OK")
		case "NOOP":
			_ = send("250 OK")
		case "QUIT":
			_ = send("221 bye")
			return
		default:
			_ = send("502 未知命令")
		}
	}
}

func (s *SMTP) checkAuth(user, pass string) bool {
	wantUser := s.opts.Username
	wantPass := s.opts.Password
	if wantPass == "" {
		return true
	}
	return user == wantUser && pass == wantPass
}

func addressOf(line string) string {
	start := strings.Index(line, "<")
	end := strings.Index(line, ">")
	if start >= 0 && end > start {
		return line[start+1 : end]
	}
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		return strings.Trim(fields[1], "<>")
	}
	return ""
}

func decodePlain(payload string) (string, string) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return "", ""
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) < 3 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

func decodeB64(payload string) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return ""
	}
	return string(raw)
}

// parseMessage 提取 subject 与正文（fixture 只要求能断言，不追求完整 MIME）。
func parseMessage(raw string) (string, string) {
	subject := ""
	body := ""
	header, rest, found := strings.Cut(raw, "\r\n\r\n")
	if !found {
		header, rest, found = strings.Cut(raw, "\n\n")
	}
	for _, line := range strings.Split(header, "\n") {
		if v, ok := cutPrefixFold(line, "Subject:"); ok {
			subject = decodeWords(strings.TrimSpace(v))
		}
	}
	if found {
		body = rest
	}
	return subject, body
}

// decodeWords 解 RFC 2047 编码头（非 ASCII 主题会被客户端编码）。
func decodeWords(raw string) string {
	dec := new(mime.WordDecoder)
	if out, err := dec.DecodeHeader(raw); err == nil {
		return out
	}
	return raw
}

func cutPrefixFold(line, prefix string) (string, bool) {
	trimmed := strings.TrimSuffix(line, "\r")
	if len(trimmed) < len(prefix) {
		return "", false
	}
	if strings.EqualFold(trimmed[:len(prefix)], prefix) {
		return trimmed[len(prefix):], true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// IMAP
// ---------------------------------------------------------------------------

// Message 是 fixture 邮箱里的一封信。
type Message struct {
	Seq        int
	UID        int
	From       string
	Subject    string
	Date       string
	Raw        []byte
	Header     []byte
	HasAttach  bool
	AttachName string
	AttachBody []byte
}

// IMAP 是本地 IMAP fixture（固定两封邮件，内容确定）。
type IMAP struct {
	ln      net.Listener
	opts    Options
	counter *Counter
	done    chan struct{}
	wg      sync.WaitGroup

	messages []Message

	mu     sync.Mutex
	closed bool
}

// NewIMAP 在回环随机端口上启动 IMAP fixture。
func NewIMAP(opts Options) (*IMAP, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fixtures: IMAP 监听失败: %w", err)
	}
	i := &IMAP{
		ln:       ln,
		opts:     opts,
		counter:  opts.counter(),
		done:     make(chan struct{}),
		messages: fixtureMessages(),
	}
	i.wg.Add(1)
	go i.serve()
	return i, nil
}

// Addr 返回 host:port。
func (i *IMAP) Addr() string { return i.ln.Addr().String() }

// Counter 返回副作用计数器。
func (i *IMAP) Counter() *Counter { return i.counter }

// Messages 返回 fixture 邮箱内容（断言用）。
func (i *IMAP) Messages() []Message {
	out := make([]Message, len(i.messages))
	copy(out, i.messages)
	return out
}

// Close 关闭监听（幂等）。
func (i *IMAP) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	i.mu.Unlock()
	close(i.done)
	err := i.ln.Close()
	i.wg.Wait()
	_ = i.counter.Persist()
	return err
}

func (i *IMAP) serve() {
	defer i.wg.Done()
	for {
		conn, err := i.ln.Accept()
		if err != nil {
			return
		}
		i.counter.Update(func(sn *Snapshot) { sn.Connections++ })
		i.wg.Add(1)
		go func() {
			defer i.wg.Done()
			defer func() { _ = conn.Close() }()
			i.handle(conn)
		}()
	}
}

func (i *IMAP) handle(conn net.Conn) {
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	send := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}
	send("* OK fixture-imap ready")

	loggedIn := strings.TrimSpace(i.opts.Password) == ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		tag := fields[0]
		verb := strings.ToUpper(fields[1])
		rest := strings.TrimSpace(strings.TrimPrefix(line, tag))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, fields[1]))

		switch verb {
		case "CAPABILITY":
			send("* CAPABILITY IMAP4rev1 AUTH=PLAIN")
			send(tag + " OK CAPABILITY completed")
		case "NOOP":
			send(tag + " OK NOOP completed")
		case "LOGIN":
			user, pass := imapCredentials(rest)
			if i.checkAuth(user, pass) {
				loggedIn = true
				send(tag + " OK LOGIN completed")
			} else {
				i.counter.Update(func(sn *Snapshot) { sn.MailAuthFailed++ })
				send(tag + " NO [AUTHENTICATIONFAILED] 认证失败")
			}
		case "SELECT", "EXAMINE":
			if !loggedIn {
				send(tag + " NO 需要先登录")
				continue
			}
			send(fmt.Sprintf("* %d EXISTS", len(i.messages)))
			send("* 0 RECENT")
			send("* OK [UIDVALIDITY 1] UIDs valid")
			send(tag + " OK [READ-WRITE] SELECT completed")
		case "LIST":
			if !loggedIn {
				send(tag + " NO 需要先登录")
				continue
			}
			send(`* LIST () "/" "INBOX"`)
			send(tag + " OK LIST completed")
		case "FETCH", "UID":
			if !loggedIn {
				send(tag + " NO 需要先登录")
				continue
			}
			sub := verb
			args := rest
			if verb == "UID" {
				parts := strings.SplitN(rest, " ", 2)
				if len(parts) == 2 {
					sub = strings.ToUpper(parts[0])
					args = strings.TrimSpace(parts[1])
				} else {
					sub = strings.ToUpper(parts[0])
					args = ""
				}
			}
			if sub != "FETCH" {
				send(tag + " BAD 不支持的命令")
				continue
			}
			i.handleFetch(r, w, tag, args)
		case "LOGOUT":
			send("* BYE fixture-imap")
			send(tag + " OK LOGOUT completed")
			return
		default:
			send(tag + " BAD 未知命令")
		}
	}
}

func (i *IMAP) checkAuth(user, pass string) bool {
	if strings.TrimSpace(i.opts.Password) == "" {
		return true
	}
	return user == i.opts.Username && pass == i.opts.Password
}

func imapCredentials(rest string) (string, string) {
	fields := splitIMAPArgs(rest)
	if len(fields) >= 2 {
		return fields[0], fields[1]
	}
	return "", ""
}

// splitIMAPArgs 支持带引号的参数与裸 atom。
func splitIMAPArgs(rest string) []string {
	out := []string{}
	var cur strings.Builder
	inQuote := false
	for _, r := range rest {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// handleFetch 是 fixture 的 FETCH 实现。
//
// 支持两种客户端真正会发的形式：
//
//	FETCH 1:* (UID BODY.PEEK[HEADER])  搜索（只取信头）
//	UID FETCH <uid> (UID BODY.PEEK[])  读取（整封）
func (i *IMAP) handleFetch(_ *bufio.Reader, w *bufio.Writer, tag, args string) {
	head := strings.ToUpper(args)
	wantHeader := strings.Contains(head, "HEADER")
	wantBody := strings.Contains(head, "BODY") && !wantHeader

	targets := i.messages
	if uid := uidOf(args); uid > 0 {
		all := i.messages
		targets = nil
		for _, m := range all {
			if m.UID == uid {
				targets = append(targets, m)
			}
		}
		if len(targets) == 0 {
			_, _ = w.WriteString(tag + " OK FETCH completed\r\n")
			_ = w.Flush()
			return
		}
	}

	for _, m := range targets {
		switch {
		case wantHeader:
			i.counter.Update(func(sn *Snapshot) { sn.MailSearched++ })
			_, _ = w.WriteString(fmt.Sprintf("* %d FETCH (UID %d BODY[HEADER] {%d}\r\n", m.Seq, m.UID, len(m.Header)))
			_, _ = w.Write(m.Header)
			_, _ = w.WriteString(")\r\n")
		case wantBody:
			i.counter.Update(func(sn *Snapshot) { sn.MailRead++ })
			_, _ = w.WriteString(fmt.Sprintf("* %d FETCH (UID %d BODY[] {%d}\r\n", m.Seq, m.UID, len(m.Raw)))
			_, _ = w.Write(m.Raw)
			_, _ = w.WriteString(")\r\n")
		default:
			_, _ = w.WriteString(fmt.Sprintf("* %d FETCH (UID %d FLAGS (\\Seen))\r\n", m.Seq, m.UID))
		}
	}
	_, _ = w.WriteString(tag + " OK FETCH completed\r\n")
	_ = w.Flush()
}

// uidOf 从 FETCH 参数里取出单个 UID（1:* 或 1:2 返回 0 = "全部"）。
func uidOf(args string) int {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return 0
	}
	token := fields[0]
	if strings.Contains(token, ":") || token == "*" {
		return 0
	}
	v, err := strconv.Atoi(token)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// fixtureMessages 构造确定性的两封邮件（其中一封带附件）。
func fixtureMessages() []Message {
	first := Message{
		Seq:        1,
		UID:        101,
		From:       "Fixture Sender <sender@fixture.test>",
		Subject:    "周报 2026-W45",
		Date:       "Mon, 02 Nov 2026 09:00:00 +0800",
		HasAttach:  true,
		AttachName: "report.txt",
		AttachBody: []byte("report-body\n"),
	}
	first.Header = []byte("From: " + first.From + "\r\n" +
		"To: user@fixture.test\r\n" +
		"Subject: " + first.Subject + "\r\n" +
		"Date: " + first.Date + "\r\n" +
		"Message-ID: <msg-101@fixture.test>\r\n" +
		"MIME-Version: 1.0\r\n\r\n")
	first.Raw = []byte("From: " + first.From + "\r\n" +
		"To: user@fixture.test\r\n" +
		"Subject: " + first.Subject + "\r\n" +
		"Date: " + first.Date + "\r\n" +
		"Message-ID: <msg-101@fixture.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"fixture-boundary\"\r\n" +
		"\r\n" +
		"--fixture-boundary\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"本周进度：连接器框架落地。\r\n" +
		"--fixture-boundary\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"report.txt\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		base64.StdEncoding.EncodeToString(first.AttachBody) + "\r\n" +
		"--fixture-boundary--\r\n")

	second := Message{
		Seq:     2,
		UID:     102,
		From:    "Fixture Notify <notify@fixture.test>",
		Subject: "系统通知",
		Date:    "Tue, 03 Nov 2026 10:30:00 +0800",
	}
	second.Header = []byte("From: " + second.From + "\r\n" +
		"To: user@fixture.test\r\n" +
		"Subject: " + second.Subject + "\r\n" +
		"Date: " + second.Date + "\r\n" +
		"Message-ID: <msg-102@fixture.test>\r\n" +
		"MIME-Version: 1.0\r\n\r\n")
	second.Raw = []byte("From: " + second.From + "\r\n" +
		"To: user@fixture.test\r\n" +
		"Subject: " + second.Subject + "\r\n" +
		"Date: " + second.Date + "\r\n" +
		"Message-ID: <msg-102@fixture.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"夜间构建已完成。\r\n")
	return []Message{first, second}
}

// ---------------------------------------------------------------------------
// OAuth 令牌端点
// ---------------------------------------------------------------------------

type oauthCode struct {
	challenge   string
	redirectURI string
	clientID    string
	expiresAt   time.Time
	used        bool
}

// OAuth 是本地 OAuth 授权服务器（authorize + token + refresh）。
//
// 它实现的是 RFC 6749 授权码 + PKCE(S256) 的**真实**交互：授权码一次性、
// 有有效期、必须带匹配的 verifier。测试因此能区分"换码成功"和"重放/过期"。
type OAuth struct {
	ln      net.Listener
	srv     *http.Server
	opts    Options
	counter *Counter

	mu      sync.Mutex
	codes   map[string]*oauthCode
	refresh map[string]bool
	closed  bool
}

// NewOAuth 在回环随机端口上启动授权服务器。
func NewOAuth(opts Options) (*OAuth, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fixtures: OAuth 监听失败: %w", err)
	}
	o := &OAuth{
		ln:      ln,
		opts:    opts,
		counter: opts.counter(),
		codes:   map[string]*oauthCode{},
		refresh: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", o.handleAuthorize)
	mux.HandleFunc("/token", o.handleToken)
	o.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	go func() {
		if err := o.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			_ = err
		}
	}()
	return o, nil
}

// Addr 返回 host:port。
func (o *OAuth) Addr() string { return o.ln.Addr().String() }

// AuthorizeURL 返回授权端点。
func (o *OAuth) AuthorizeURL() string { return "http://" + o.Addr() + "/authorize" }

// TokenURL 返回令牌端点。
func (o *OAuth) TokenURL() string { return "http://" + o.Addr() + "/token" }

// Counter 返回副作用计数器。
func (o *OAuth) Counter() *Counter { return o.counter }

// Close 关闭服务器（幂等）。
func (o *OAuth) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := o.srv.Shutdown(ctx)
	_ = o.counter.Persist()
	return err
}

func (o *OAuth) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	if err := auth.ValidateRedirectURI(redirect); err != nil {
		http.Error(w, "redirect_uri 不是回环地址", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(q.Get("client_id")) == "" {
		http.Error(w, "缺少 client_id", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge_method") != "S256" || strings.TrimSpace(q.Get("code_challenge")) == "" {
		http.Error(w, "只支持 PKCE S256", http.StatusBadRequest)
		return
	}
	code, err := randomToken("code-")
	if err != nil {
		http.Error(w, "生成授权码失败", http.StatusInternalServerError)
		return
	}
	o.mu.Lock()
	o.codes[code] = &oauthCode{
		challenge:   q.Get("code_challenge"),
		redirectURI: redirect,
		clientID:    q.Get("client_id"),
		expiresAt:   time.Now().Add(o.opts.codeTTL()),
	}
	o.mu.Unlock()

	target, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "redirect_uri 不合法", http.StatusBadRequest)
		return
	}
	tq := target.Query()
	tq.Set("code", code)
	if state := q.Get("state"); state != "" {
		tq.Set("state", state)
	}
	target.RawQuery = tq.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (o *OAuth) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "只支持 POST", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, "invalid_request", "表单不合法")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		o.exchangeCode(w, r)
	case "refresh_token":
		o.exchangeRefresh(w, r)
	default:
		writeOAuthError(w, "unsupported_grant_type", "不支持的 grant_type")
	}
}

func (o *OAuth) exchangeCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	verifier := r.PostForm.Get("code_verifier")
	redirect := r.PostForm.Get("redirect_uri")

	o.mu.Lock()
	entry, ok := o.codes[code]
	now := time.Now()
	reject := func(desc string) {
		o.mu.Unlock()
		o.counter.Update(func(sn *Snapshot) { sn.OAuthCodeRejected++ })
		writeOAuthError(w, "invalid_grant", desc)
	}
	if !ok {
		reject("unknown code")
		return
	}
	if entry.used {
		reject("code already used")
		return
	}
	if now.After(entry.expiresAt) {
		reject("code expired")
		return
	}
	if entry.redirectURI != redirect {
		reject("redirect_uri mismatch")
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != entry.challenge {
		reject("PKCE verification failed")
		return
	}
	entry.used = true
	o.mu.Unlock()

	o.issueTokens(w, "authorization_code")
}

func (o *OAuth) exchangeRefresh(w http.ResponseWriter, r *http.Request) {
	token := r.PostForm.Get("refresh_token")
	o.mu.Lock()
	valid := o.refresh[token]
	o.mu.Unlock()
	if !valid {
		o.counter.Update(func(sn *Snapshot) { sn.OAuthCodeRejected++ })
		writeOAuthError(w, "invalid_grant", "unknown refresh token")
		return
	}
	o.issueTokens(w, "refresh_token")
}

func (o *OAuth) issueTokens(w http.ResponseWriter, _ string) {
	access, err := randomToken("at-")
	if err != nil {
		writeOAuthError(w, "server_error", "生成令牌失败")
		return
	}
	refresh, err := randomToken("rt-")
	if err != nil {
		writeOAuthError(w, "server_error", "生成刷新令牌失败")
		return
	}
	o.mu.Lock()
	o.refresh[refresh] = true
	o.mu.Unlock()
	o.counter.Update(func(sn *Snapshot) { sn.OAuthTokensIssued++ })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
}

func writeOAuthError(w http.ResponseWriter, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": desc,
	})
}

func randomToken(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
