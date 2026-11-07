package resources

// RES05 的目标地址判定与归档安全测试。
//
// 这一组测试只证明一件事：链接导入的每条硬规则都真的会拒绝。
// 用例刻意覆盖"每个 DNS answer、每次重定向、连接后的实际 peer"三条路径，
// 以及 zip bomb 的声明/实际两种形态——这几条是最容易写成"只判一次"的地方。

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------- 测试替身

type resolverFunc func(ctx context.Context, host string) ([]net.IP, error)

func (f resolverFunc) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return f(ctx, host)
}

// peerAddrConn 让测试能把"实际连到的对端地址"伪装成任意地址。
// 生产路径没有这层包装：peer 检查读的就是真实 RemoteAddr。
type peerAddrConn struct {
	net.Conn
	peer net.Addr
}

func (c *peerAddrConn) RemoteAddr() net.Addr { return c.peer }

// publicTestIP 是一个真实的公网地址（example.com），用于让解析结果通过校验。
var publicTestIP = net.ParseIP("93.184.216.34")

type linkEnv struct {
	inspector *LinkInspector
	server    *httptest.Server
	root      string
	requests  *int32
	// answers 允许用例改写解析结果（DNS rebinding 用）。
	answers atomic.Value // []net.IP
	peer    atomic.Value // net.IP
}

func (e *linkEnv) setAnswers(ips ...net.IP) { e.answers.Store(ips) }
func (e *linkEnv) setPeer(ip net.IP)        { e.peer.Store(ip) }

// newLinkEnv 起一个 TLS 测试服务并把 inspector 指向它。
//
// 关键点：URL 用 https://example.com（httptest 证书的 DNS 名就是 example.com），
// 解析结果被替换成公网地址，拨号实际落到本机测试服务并把 RemoteAddr 伪装成
// 同一个公网地址。这样"公网校验 + peer 校验"两条真实代码路径都被执行到。
func newLinkEnv(t *testing.T, handler http.Handler) *linkEnv {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	addr := srv.Listener.Addr().String()
	env := &linkEnv{server: srv, root: t.TempDir(), requests: new(int32)}
	env.answers.Store([]net.IP{publicTestIP})
	env.peer.Store(publicTestIP)
	env.inspector = &LinkInspector{
		Root:    env.root,
		RootCAs: pool,
		Resolve: resolverFunc(func(ctx context.Context, host string) ([]net.IP, error) {
			return env.answers.Load().([]net.IP), nil
		}),
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			ip := env.peer.Load().(net.IP)
			return &peerAddrConn{Conn: conn, peer: &net.TCPAddr{IP: ip, Port: 443}}, nil
		},
	}
	return env
}

func (e *linkEnv) count() int32 { return atomic.LoadInt32(e.requests) }

func (e *linkEnv) url(path string) string { return "https://example.com" + path }

// stagedEntries 返回暂存根下还剩几个目录。
func stagedEntries(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "skill-links"))
	if err != nil {
		return 0
	}
	return len(entries)
}

func assertRejected(t *testing.T, env *linkEnv, rawURL, wantRule string) LinkRejection {
	t.Helper()
	_, err := env.inspector.Inspect(context.Background(), rawURL)
	if err == nil {
		t.Fatalf("期望被拒绝（%s），实际通过了", wantRule)
	}
	rejections, ok := AsLinkRejections(err)
	if !ok {
		t.Fatalf("期望规则拒绝，实际内部错误: %v", err)
	}
	if len(rejections) != 1 || rejections[0].Rule != wantRule {
		t.Fatalf("拒绝规则 = %+v，期望 %s", rejections, wantRule)
	}
	if got := stagedEntries(t, env.root); got != 0 {
		t.Fatalf("拒绝路径不应留下暂存目录，实际 %d 个", got)
	}
	return rejections[0]
}

// ---------------------------------------------------------------- 地址判定

// TestCheckPublicDestinationMatchesIANATable 逐段确认判定表生效，
// 并确认公网地址不被误拒（把 public 也判错的表会让功能整体不可用）。
func TestCheckPublicDestinationMatchesIANATable(t *testing.T) {
	rejected := []string{
		"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.1.1",
		"172.16.5.5", "192.0.0.8", "192.0.2.5", "192.88.99.1", "192.168.1.1",
		"198.18.0.1", "198.19.255.255", "198.51.100.7", "203.0.113.9",
		"224.0.0.1", "239.255.255.250", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:10.0.0.1", "::ffff:127.0.0.1",
		"64:ff9b::a00:1", "64:ff9b:1::1", "100::1", "2001::1", "2001:2::1",
		"2001:db8::1", "2002::1", "3fff::1", "5f00::1", "fc00::1", "fd12:3456::1",
		"fe80::1", "ff02::1",
	}
	for _, raw := range rejected {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("用例里的地址本身不合法: %s", raw)
		}
		ok, name := CheckPublicDestination(ip)
		if ok {
			t.Fatalf("%s 应被拒绝，实际通过（range=%q）", raw, name)
		}
		if name == "" {
			t.Fatalf("%s 被拒绝但没有给出命中的段名", raw)
		}
	}
	allowed := []string{"93.184.216.34", "8.8.8.8", "2606:4700:4700::1111"}
	for _, raw := range allowed {
		if ok, name := CheckPublicDestination(net.ParseIP(raw)); !ok {
			t.Fatalf("%s 是公网地址，被误拒（range=%q）", raw, name)
		}
	}
}

// TestSpecialPurposeTableIsVersioned 守"表有出处、可更新"：
// 没有来源/版本的 deny list 说不清为什么拒，也无法在 IANA 更新后核对。
func TestSpecialPurposeTableIsVersioned(t *testing.T) {
	if SpecialPurposeRegistrySource == "" || SpecialPurposeRegistryVersion == "" {
		t.Fatal("special-purpose 表必须带来源与版本")
	}
	ranges := SpecialPurposeRanges()
	if len(ranges) < 20 {
		t.Fatalf("表只有 %d 行，明显不完整", len(ranges))
	}
	for _, r := range ranges {
		if r.CIDR == "" || r.Name == "" {
			t.Fatalf("表行缺少字段: %+v", r)
		}
	}
}

// ---------------------------------------------------------------- 协议与 DNS

// TestLinkInspectRejectsNonHTTPSOrUserinfo：只接受 HTTPS，且不接受 userinfo。
func TestLinkInspectRejectsNonHTTPSOrUserinfo(t *testing.T) {
	var hits int32
	env := newLinkEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	assertRejected(t, env, "http://example.com/SKILL.md", LinkRuleScheme)
	assertRejected(t, env, "ftp://example.com/SKILL.md", LinkRuleScheme)
	assertRejected(t, env, "https://user:pass@example.com/SKILL.md", LinkRuleUserinfo)
	assertRejected(t, env, "https://example.com:99999/SKILL.md", LinkRulePortInvalid)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("被拒绝的地址不应发起请求，实际 %d 次", got)
	}
}

// TestLinkInspectRejectsEverySpecialPurposeDNSAnswer：
// 每个 DNS answer 都要过判定，只要有一条落在 special-purpose 段就整体拒绝。
func TestLinkInspectRejectsEverySpecialPurposeDNSAnswer(t *testing.T) {
	var hits int32
	env := newLinkEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("---\nname: ok\ndescription: ok\n---\n"))
	}))
	private := []string{
		"127.0.0.1", "10.0.0.5", "172.20.0.1", "192.168.0.10", "100.64.1.1",
		"198.18.1.1", "169.254.169.254", "192.0.2.1", "198.51.100.1", "203.0.113.1",
		"224.0.0.1", "255.255.255.255", "::1", "fc00::1", "fe80::1", "ff02::1", "::",
		"::ffff:127.0.0.1", "2001:db8::1", "2001:2::1",
	}
	for _, raw := range private {
		// 一条公网 + 一条私网：客户端挑哪条取决于运气，所以必须整体拒绝。
		env.setAnswers(publicTestIP, net.ParseIP(raw))
		rej := assertRejected(t, env, env.url("/SKILL.md"), LinkRuleSpecialPurpose)
		// IPv4-mapped 地址会被归一到 IPv4 再报，比较归一后的形式。
		want := net.ParseIP(raw).String()
		if v4 := net.ParseIP(raw).To4(); v4 != nil {
			want = v4.String()
		}
		if rej.Params["address"] != want {
			t.Fatalf("%s 的拒绝参数没有指出具体地址: %+v", raw, rej.Params)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("地址判定失败时不应发起请求，实际 %d 次", got)
	}
}

// TestLinkInspectRejectsDNSRebinding：第一次解析公网、第二次解析私网。
//
// 只在解析阶段判一次的实现在这里会漏：真正的连接用的是第二次解析结果。
func TestLinkInspectRejectsDNSRebinding(t *testing.T) {
	var calls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/SKILL.md", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("---\nname: ok\ndescription: ok\n---\n"))
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	addr := srv.Listener.Addr().String()
	root := t.TempDir()
	in := &LinkInspector{
		Root:    root,
		RootCAs: pool,
		Resolve: resolverFunc(func(ctx context.Context, host string) ([]net.IP, error) {
			atomic.AddInt32(&calls, 1)
			if atomic.LoadInt32(&calls) == 1 {
				return []net.IP{publicTestIP}, nil
			}
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}),
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &peerAddrConn{Conn: conn, peer: &net.TCPAddr{IP: publicTestIP, Port: 443}}, nil
		},
	}
	_, err := in.Inspect(context.Background(), "https://example.com/start")
	if err == nil {
		t.Fatal("DNS rebinding 必须被拒绝，实际通过了")
	}
	rejections, ok := AsLinkRejections(err)
	if !ok || rejections[0].Rule != LinkRuleSpecialPurpose {
		t.Fatalf("期望 LINK_SPECIAL_PURPOSE_ADDRESS，实际 %+v (%v)", rejections, err)
	}
}

// TestLinkInspectRejectsPrivatePeer：解析看起来正常，但连上后的对端是私网。
//
// 这是"代理/劫持/NAT 设备把连接转走"的形态；peer 判定是最后一道闸。
func TestLinkInspectRejectsPrivatePeer(t *testing.T) {
	var hits int32
	env := newLinkEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("---\nname: ok\ndescription: ok\n---\n"))
	}))
	env.setPeer(net.ParseIP("10.9.8.7"))
	rej := assertRejected(t, env, env.url("/SKILL.md"), LinkRulePrivatePeer)
	if rej.Params["address"] != "10.9.8.7" {
		t.Fatalf("peer 拒绝要指出实际对端地址: %+v", rej.Params)
	}
}

// TestLinkInspectRejectsRedirectEscapes：重定向的协议、目的地与次数都要判。
func TestLinkInspectRejectsRedirectEscapes(t *testing.T) {
	// 重定向到 http。
	env := newLinkEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/SKILL.md", http.StatusFound)
	}))
	assertRejected(t, env, env.url("/start"), LinkRuleRedirectScheme)

	// 重定向到私网主机。
	env = newLinkEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://internal.example/SKILL.md", http.StatusFound)
	}))
	orig := env.inspector.Resolve
	env.inspector.Resolve = resolverFunc(func(ctx context.Context, host string) ([]net.IP, error) {
		if host == "internal.example" {
			return []net.IP{net.ParseIP("10.1.2.3")}, nil
		}
		return orig.LookupIP(ctx, host)
	})
	rej := assertRejected(t, env, env.url("/start"), LinkRuleSpecialPurpose)
	if rej.Params["host"] != "internal.example" {
		t.Fatalf("重定向拒绝要指出目标主机: %+v", rej.Params)
	}

	// 重定向次数超限。
	env = newLinkEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	assertRejected(t, env, env.url("/start"), LinkRuleTooManyRedirects)
}

// ---------------------------------------------------------------- 体积与归档

// TestLinkInspectRejectsOversizeDownload：响应体超限即拒绝（不落盘）。
func TestLinkInspectRejectsOversizeDownload(t *testing.T) {
	env := newLinkEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	}))
	env.inspector.Limits = LinkLimits{MaxDownloadBytes: 1024, MaxUnpackedBytes: 1 << 20, MaxFiles: 10, MaxEntryBytes: 1 << 20, MaxRedirects: 1, Timeout: 5 * time.Second}
	assertRejected(t, env, env.url("/SKILL.md"), LinkRuleDownloadTooLarge)
}

func skillMarkdown(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n说明。\n"
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatalf("建 zip 条目: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("写 zip 条目: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip: %v", err)
	}
	return buf.Bytes()
}

func tarGzBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写 tar 头: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("写 tar 内容: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip: %v", err)
	}
	return buf.Bytes()
}

func serveBytes(body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
}

// TestLinkInspectRejectsZipBombDeclaredSize：
// 声明解压后过大时必须在读取之前拒绝（否则已经解出几百 MiB 才发现）。
func TestLinkInspectRejectsZipBombDeclaredSize(t *testing.T) {
	// 64 MiB 的零字节压缩后只有几十 KiB：典型 zip bomb。
	huge := make([]byte, 64<<20)
	body := zipBytes(t, map[string]string{"SKILL.md": skillMarkdown("bomb", "声明超大"), "payload.bin": string(huge)})
	env := newLinkEnv(t, serveBytes(body))
	assertRejected(t, env, env.url("/skill.zip"), LinkRuleArchiveEntryLarge)
}

// TestLinkInspectRejectsArchiveFileCount：文件数超限。
func TestLinkInspectRejectsArchiveFileCount(t *testing.T) {
	files := map[string]string{"SKILL.md": skillMarkdown("many", "文件很多")}
	for i := 0; i < 30; i++ {
		files["docs/file-"+string(rune('a'+i%26))+string(rune('a'+i/26))+".md"] = "x"
	}
	env := newLinkEnv(t, serveBytes(zipBytes(t, files)))
	env.inspector.Limits = LinkLimits{MaxDownloadBytes: 16 << 20, MaxUnpackedBytes: 8 << 20, MaxFiles: 5, MaxEntryBytes: 1 << 20, MaxRedirects: 1, Timeout: 5 * time.Second}
	assertRejected(t, env, env.url("/skill.zip"), LinkRuleArchiveFileCount)
}

// TestLinkInspectRejectsArchiveTotalSize：单个文件都不超限，但总量超限。
func TestLinkInspectRejectsArchiveTotalSize(t *testing.T) {
	files := map[string]string{"SKILL.md": skillMarkdown("big", "总量超限")}
	for i := 0; i < 4; i++ {
		files["docs/"+strings.Repeat("f", i+1)+".txt"] = strings.Repeat("y", 4096)
	}
	env := newLinkEnv(t, serveBytes(zipBytes(t, files)))
	env.inspector.Limits = LinkLimits{MaxDownloadBytes: 16 << 20, MaxUnpackedBytes: 8000, MaxFiles: 50, MaxEntryBytes: 1 << 20, MaxRedirects: 1, Timeout: 5 * time.Second}
	assertRejected(t, env, env.url("/skill.zip"), LinkRuleArchiveTooLarge)
}

// TestLinkInspectRejectsArchiveTraversal：路径穿越必须拒绝（不写任何文件）。
func TestLinkInspectRejectsArchiveTraversal(t *testing.T) {
	env := newLinkEnv(t, serveBytes(zipBytes(t, map[string]string{
		"SKILL.md":     skillMarkdown("trav", "越界路径"),
		"../../escape": "pwned",
	})))
	assertRejected(t, env, env.url("/skill.zip"), LinkRuleArchiveTraversal)
	if _, err := os.Stat(filepath.Join(env.root, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("越界文件被写出来了: %v", err)
	}
}

// TestLinkInspectRejectsArchiveSymlink：符号链接条目必须拒绝。
func TestLinkInspectRejectsArchiveSymlink(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := skillMarkdown("link", "符号链接")
	if err := tw.WriteHeader(&tar.Header{Name: "SKILL.md", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("写头: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("写内容: %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "evil", Linkname: "C:/Windows", Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatalf("写链接头: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip: %v", err)
	}
	env := newLinkEnv(t, serveBytes(buf.Bytes()))
	assertRejected(t, env, env.url("/skill.tar.gz"), LinkRuleArchiveSymlink)
}

// ---------------------------------------------------------------- 正向路径

// TestLinkInspectAcceptsSingleSkillMarkdown：单个 SKILL.md 也能导入。
func TestLinkInspectAcceptsSingleSkillMarkdown(t *testing.T) {
	env := newLinkEnv(t, serveBytes([]byte(skillMarkdown("single-md", "单个清单"))))
	got, err := env.inspector.Inspect(context.Background(), env.url("/SKILL.md"))
	if err != nil {
		t.Fatalf("合法链接被拒: %v", err)
	}
	if got.Name != "single-md" || got.Form != "skill-md" {
		t.Fatalf("解析结果不对: %+v", got)
	}
	if _, err := os.Stat(got.StagedSkillPath); err != nil {
		t.Fatalf("暂存清单不存在: %v", err)
	}
	if _, err := readSkillFile(got.StagedSkillPath); err != nil {
		t.Fatalf("暂存清单读不出来: %v", err)
	}
}

// TestLinkInspectZipNeverExecutesScripts：归档里的脚本只落盘、不执行，
// 且不带可执行位。管理目录在整个检查过程中不被触碰。
func TestLinkInspectZipNeverExecutesScripts(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed.marker")
	script := "#!/bin/sh\ntouch '" + marker + "'\n"
	env := newLinkEnv(t, serveBytes(zipBytes(t, map[string]string{
		"demo/SKILL.md":   skillMarkdown("zip-skill", "来自 zip"),
		"demo/install.sh": script,
	})))
	got, err := env.inspector.Inspect(context.Background(), env.url("/demo.zip"))
	if err != nil {
		t.Fatalf("合法归档被拒: %v", err)
	}
	if got.Name != "zip-skill" || got.Form != "zip" {
		t.Fatalf("解析结果不对: %+v", got)
	}
	if filepath.Dir(got.StagedSkillPath) != filepath.Join(got.StagedDir, "demo") {
		t.Fatalf("暂存清单路径不对: %s", got.StagedSkillPath)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("归档里的脚本被执行了（marker 存在）: %v", err)
	}
	if len(got.Warnings) == 0 || !strings.Contains(strings.Join(got.Warnings, ","), "script:install.sh") {
		t.Fatalf("带脚本的归档必须给出提示，实际 %v", got.Warnings)
	}
	scriptPath := filepath.Join(filepath.Dir(got.StagedSkillPath), "install.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("脚本没有落盘: %v", err)
	}
	if info.Mode()&0o111 != 0 {
		t.Fatalf("暂存文件不应带可执行位: %v", info.Mode())
	}
	// 托管目录：检查过程不碰它，所以此刻它不该存在。
	if _, err := os.Stat(filepath.Join(env.root, "skills")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("检查链接不应触碰托管目录: %v", err)
	}
}

// TestLinkInspectAcceptsTarGz：tar.gz 同样可用。
func TestLinkInspectAcceptsTarGz(t *testing.T) {
	body := tarGzBytes(t, map[string]string{
		"SKILL.md":  skillMarkdown("tar-skill", "来自 tar.gz"),
		"notes.txt": "hello",
	})
	env := newLinkEnv(t, serveBytes(body))
	got, err := env.inspector.Inspect(context.Background(), env.url("/skill.tar.gz"))
	if err != nil {
		t.Fatalf("合法 tar.gz 被拒: %v", err)
	}
	if got.Name != "tar-skill" || got.Form != "tar.gz" {
		t.Fatalf("解析结果不对: %+v", got)
	}
}

// TestLinkInspectRejectsBadManifests：清单本身不合法时给出分类规则。
func TestLinkInspectRejectsBadManifests(t *testing.T) {
	cases := []struct {
		name string
		body string
		rule string
	}{
		{"缺少 description", "---\nname: nodesc\n---\n正文\n", LinkRuleManifestDescription},
		{"缺少 frontmatter", "没有 frontmatter\n", LinkRuleManifestFrontmatter},
		{"name 非法", "---\nname: Bad_Name\ndescription: d\n---\n", LinkRuleManifestName},
		{"YAML 非法", "---\nname: [unclosed\ndescription: d\n---\n", LinkRuleManifestYAML},
	}
	for _, tc := range cases {
		env := newLinkEnv(t, serveBytes([]byte(tc.body)))
		env.inspector.Limits = DefaultLinkLimits
		assertRejected(t, env, env.url("/SKILL.md"), tc.rule)
	}
}

// TestLinkInspectRejectsUnsupportedForm：非 md/归档的响应要被拒。
func TestLinkInspectRejectsUnsupportedForm(t *testing.T) {
	env := newLinkEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	}))
	assertRejected(t, env, env.url("/logo"), LinkRuleUnsupportedForm)
}

// TestSweepStagedLinksKeepsFreshAndRemovesOld 守"结束后清理"这条：
// 暂存是用户还没确认的临时副本，没有清扫策略就会一直堆在数据目录里。
func TestSweepStagedLinksKeepsFreshAndRemovesOld(t *testing.T) {
	root := t.TempDir()
	in := NewLinkInspector(root)
	staging := in.StagingRoot()
	if err := os.MkdirAll(filepath.Join(staging, "old"), 0o700); err != nil {
		t.Fatalf("建旧暂存: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(staging, "fresh"), 0o700); err != nil {
		t.Fatalf("建新暂存: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(staging, "old"), old, old); err != nil {
		t.Fatalf("改时间: %v", err)
	}
	in.sweepStagedLinks(30 * time.Minute)
	if _, err := os.Stat(filepath.Join(staging, "old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("过期暂存没有被清理: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "fresh")); err != nil {
		t.Fatalf("新暂存被误删: %v", err)
	}
}

// TestSweepStagedLinksOnlyTouchesStagingRoot：清理不能删到别的地方。
func TestSweepStagedLinksOnlyTouchesStagingRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "skills", "demo")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("建托管目录: %v", err)
	}
	if err := SweepStagedLinks(root, outside); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("清理删到了托管目录: %v", err)
	}
}
