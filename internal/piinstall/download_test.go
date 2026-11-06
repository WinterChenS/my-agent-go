package piinstall

// PI_INS01 的第二层：下载通道。HTTPS + 主机白名单 + 每一跳重定向复核 + size/hash/签名。
// 任何一个校验失败都必须在 probe 之前失败，且不在托管根里留下半成品。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

func TestFetchStagesVerifiedFileInsideManagedRoot(t *testing.T) {
	body := []byte(strings.Repeat("pi-package-", 128))
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/pi-windows-x64.zip": fileHandler(body),
	})
	entry := entryFor("1.5.0", "win-x64", "pi-windows-x64.zip", srv.URL+"/pi-windows-x64.zip", body)
	dir := filepath.Join(t.TempDir(), "staging")

	d := &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist}
	staged, err := d.Fetch(context.Background(), entry, dir)
	if err != nil {
		t.Fatalf("下载合法包失败: %v", err)
	}
	if staged.Size != entry.SizeBytes {
		t.Errorf("staged size = %d，期望 %d", staged.Size, entry.SizeBytes)
	}
	if staged.SHA256 != entry.SHA256 {
		t.Errorf("staged sha256 = %s，期望 %s", staged.SHA256, entry.SHA256)
	}
	if !strings.HasPrefix(staged.Path, dir) {
		t.Errorf("staged 文件必须落在托管根内: %s（dir=%s）", staged.Path, dir)
	}
	onDisk, err := os.ReadFile(staged.Path)
	if err != nil {
		t.Fatalf("读回 staged 文件失败: %v", err)
	}
	if string(onDisk) != string(body) {
		t.Error("staged 文件内容与服务器内容不一致")
	}
	// 不允许留下 .part 之类的半成品。
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("下载成功后仍残留半成品: %s", e.Name())
		}
	}
}

func TestFetchRejectsSizeAndHashMismatch(t *testing.T) {
	body := []byte(strings.Repeat("pi-package-", 64))
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/pi-windows-x64.zip": fileHandler(body),
	})
	base := entryFor("1.5.0", "win-x64", "pi-windows-x64.zip", srv.URL+"/pi-windows-x64.zip", body)

	cases := map[string]func(*ManifestEntry){
		"哈希不符": func(e *ManifestEntry) {
			e.SHA256 = strings.Repeat("0", 64)
		},
		"大小不符": func(e *ManifestEntry) { e.SizeBytes = int64(len(body)) - 1 },
	}
	for name, mutate := range cases {
		entry := base
		mutate(&entry)
		dir := filepath.Join(t.TempDir(), "staging")
		d := &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist}
		if _, err := d.Fetch(context.Background(), entry, dir); err == nil {
			t.Fatalf("%s 的包必须被拒绝", name)
		} else if code := appErrCode(err); code != contracts.CodeVerificationFailed {
			t.Fatalf("%s 的拒绝码 = %q，期望 %q（err=%v）", name, code, contracts.CodeVerificationFailed, err)
		}
		assertNoLeftovers(t, dir)
	}

	// sizeBytes<=0 不是"校验失败"而是"清单没给出可用上界"：显式 UNSUPPORTED，
	// 且同样不留半成品（不编造大小、不做无上界的下载）。
	zero := base
	zero.SizeBytes = 0
	zeroDir := filepath.Join(t.TempDir(), "staging")
	d := &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist}
	if _, err := d.Fetch(context.Background(), zero, zeroDir); err == nil {
		t.Fatal("sizeBytes=0 的条目必须被拒绝")
	} else if code := appErrCode(err); code != contracts.CodeUnsupported {
		t.Fatalf("sizeBytes=0 的拒绝码 = %q，期望 %q（err=%v）", code, contracts.CodeUnsupported, err)
	}
	assertNoLeftovers(t, zeroDir)
}

func TestFetchRejectsOversizeBody(t *testing.T) {
	body := []byte(strings.Repeat("x", 4096))
	srv := newTestServer(t, map[string]http.HandlerFunc{
		// 声称 10 字节、实际吐 4KB：不得靠 Content-Length 过关。
		"/pi-windows-x64.zip": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		},
	})
	entry := entryFor("1.5.0", "win-x64", "pi-windows-x64.zip", srv.URL+"/pi-windows-x64.zip", body)
	entry.SizeBytes = 10
	entry.SHA256 = sha256HexOf([]byte(strings.Repeat("y", 10)))
	dir := filepath.Join(t.TempDir(), "staging")
	d := &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist}
	if _, err := d.Fetch(context.Background(), entry, dir); err == nil {
		t.Fatal("超过声明大小的响应必须被拒绝")
	}
	assertNoLeftovers(t, dir)
}

func TestFetchRejectsUntrustedRedirect(t *testing.T) {
	body := []byte("payload")
	// 每个入口 URL 的最后一段都仍是官方资产名（否则清单自身就该拒绝），
	// 差异只在**重定向目标**上：这才是在测"每跳复核"，而不是在测 URL 拼写。
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/files/pi-windows-x64.zip":   fileHandler(body),
		"/evil/pi-windows-x64.zip":    redirectHandler("https://evil.example/pi-windows-x64.zip"),
		"/plain/pi-windows-x64.zip":   redirectHandler("http://127.0.0.1:1/pi-windows-x64.zip"),
		"/creds/pi-windows-x64.zip":   redirectHandler("https://user:pass@github.com/pi.zip"),
		"/to-self/pi-windows-x64.zip": redirectHandler("/files/pi-windows-x64.zip"),
		"/hop1/pi-windows-x64.zip":    redirectHandler("/hop2/pi-windows-x64.zip"),
		"/hop2/pi-windows-x64.zip":    redirectHandler("/hop3/pi-windows-x64.zip"),
		"/hop3/pi-windows-x64.zip":    redirectHandler("/hop4/pi-windows-x64.zip"),
		"/hop4/pi-windows-x64.zip":    redirectHandler("/hop5/pi-windows-x64.zip"),
		"/hop5/pi-windows-x64.zip":    redirectHandler("/hop6/pi-windows-x64.zip"),
		"/hop6/pi-windows-x64.zip":    redirectHandler("/hop7/pi-windows-x64.zip"),
		"/hop7/pi-windows-x64.zip":    redirectHandler("/hop8/pi-windows-x64.zip"),
		"/hop8/pi-windows-x64.zip":    fileHandler(body),
	})
	cases := map[string]string{
		"重定向到非白名单主机":  "/evil/pi-windows-x64.zip",
		"重定向到明文 HTTP": "/plain/pi-windows-x64.zip",
		"重定向带凭证":      "/creds/pi-windows-x64.zip",
		"重定向超过上限":     "/hop1/pi-windows-x64.zip",
	}
	for name, path := range cases {
		entry := entryFor("1.5.0", "win-x64", "pi-windows-x64.zip", srv.URL+path, body)
		dir := filepath.Join(t.TempDir(), "staging")
		d := &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist}
		_, err := d.Fetch(context.Background(), entry, dir)
		if err == nil {
			t.Fatalf("%s 的重定向必须被拒绝", name)
		}
		if code := appErrCode(err); code != contracts.CodeVerificationFailed {
			t.Fatalf("%s 的拒绝码 = %q，期望 %q（err=%v）", name, code, contracts.CodeVerificationFailed, err)
		}
		assertNoLeftovers(t, dir)
	}

	// 同一主机内的合法重定向必须放行（否则"拒绝一切重定向"也能让上面几条绿）。
	entry := entryFor("1.5.0", "win-x64", "pi-windows-x64.zip", srv.URL+"/to-self/pi-windows-x64.zip", body)
	dir := filepath.Join(t.TempDir(), "staging")
	d := &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist}
	if _, err := d.Fetch(context.Background(), entry, dir); err != nil {
		t.Fatalf("同一主机内的合法重定向被拒绝: %v", err)
	}
}

func TestFetchRejectsNonHTTPSHostsAndIPLiterals(t *testing.T) {
	body := []byte("payload")
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/pi-windows-x64.zip": fileHandler(body),
	})
	cases := map[string]string{
		"明文 HTTP": strings.Replace(srv.URL, "https://", "http://", 1) + "/pi-windows-x64.zip",
		"file 协议": "file:///tmp/pi-windows-x64.zip",
		"IP 字面量":  "https://93.184.216.34/pi-windows-x64.zip",
		"非白名单主机":  "https://evil.example/pi-windows-x64.zip",
		"URL 带凭证": "https://user:pass@github.com/pi-windows-x64.zip",
	}
	for name, url := range cases {
		entry := entryFor("1.5.0", "win-x64", "pi-windows-x64.zip", url, body)
		dir := filepath.Join(t.TempDir(), "staging")
		// 生产默认白名单：不含任何 IP 字面量，也不含 evil.example。
		d := &Downloader{Client: srv.Client()}
		if _, err := d.Fetch(context.Background(), entry, dir); err == nil {
			t.Fatalf("%s 必须被拒绝", name)
		}
	}
}

func TestFetchCancellationLeavesNoPartialFile(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/pi-windows-x64.zip": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", "1000000")
			_, _ = w.Write([]byte(strings.Repeat("a", 8192)))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-release:
			}
		},
	})
	entry := ManifestEntry{
		Version: "1.5.0", TargetID: "win-x64", AssetName: "pi-windows-x64.zip",
		URL: srv.URL + "/pi-windows-x64.zip", SizeBytes: 1000000, SHA256: strings.Repeat("a", 64),
	}
	dir := filepath.Join(t.TempDir(), "staging")
	ctx, cancel := context.WithCancel(context.Background())
	d := &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist}

	errCh := make(chan error, 1)
	go func() {
		_, err := d.Fetch(ctx, entry, dir)
		errCh <- err
	}()
	// 等半成品真的出现再取消：否则测的是"还没开始就取消"。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if entries, _ := os.ReadDir(dir); len(entries) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("取消后 Fetch 必须返回错误")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "取消") {
			t.Logf("取消错误形态（允许包装）: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("取消后 Fetch 未返回")
	}
	assertNoLeftovers(t, dir)
}

func TestFetchSignatureFailsClosed(t *testing.T) {
	body := []byte("signed-payload")
	srv := newTestServer(t, map[string]http.HandlerFunc{
		"/pi-windows-x64.zip": fileHandler(body),
	})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body))

	base := entryFor("1.5.0", "win-x64", "pi-windows-x64.zip", srv.URL+"/pi-windows-x64.zip", body)

	// 1) 有签名 + 正确公钥 → 通过。
	entry := base
	entry.Signature = sig
	dir := filepath.Join(t.TempDir(), "staging")
	d := &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist, PublicKey: pub}
	staged, err := d.Fetch(context.Background(), entry, dir)
	if err != nil {
		t.Fatalf("签名正确的包被拒绝: %v", err)
	}
	if !staged.SignatureVerified {
		t.Error("签名校验通过时 SignatureVerified 必须为 true")
	}

	// 2) 有签名但没有信任根 → fail closed。
	dir = filepath.Join(t.TempDir(), "staging")
	d = &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist}
	if _, err := d.Fetch(context.Background(), entry, dir); err == nil {
		t.Fatal("有签名但无信任根时必须拒绝（fail closed）")
	}
	assertNoLeftovers(t, dir)

	// 3) 签名不匹配 → 拒绝。
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成第二个测试密钥失败: %v", err)
	}
	dir = filepath.Join(t.TempDir(), "staging")
	d = &Downloader{Client: srv.Client(), AllowedHosts: srv.Allowlist, PublicKey: otherPub}
	if _, err := d.Fetch(context.Background(), entry, dir); err == nil {
		t.Fatal("签名不匹配必须被拒绝")
	}
	assertNoLeftovers(t, dir)
}

// assertNoLeftovers 断言目录里没有残留文件（取消/失败都不许留半成品）。
func assertNoLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("读目录 %s 失败: %v", dir, err)
	}
	for _, e := range entries {
		t.Errorf("失败路径留下了残留文件: %s", filepath.Join(dir, e.Name()))
	}
}
