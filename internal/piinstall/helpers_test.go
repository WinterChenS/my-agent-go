package piinstall

// 测试用的小装置：TLS fixture server、归档构造、可执行的 fakeworker。
//
// 纪律：
//   - fixture server 一律 httptest.NewTLSServer：下载器只接受 HTTPS，明文 HTTP
//     服务器测出来的结论不作数；
//   - 主机白名单是**测试注入**的（127.0.0.1:port），生产默认白名单不含任何 IP
//     字面量，IP 字面量默认拒绝；
//   - 归档一律现场构造，不落仓库。

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// testServer 是带 TLS 的 fixture 服务器。
type testServer struct {
	*httptest.Server
	Allowlist []string
}

// newTestServer 起一个 TLS 服务器。routes 的 key 是路径，value 是处理器。
func newTestServer(t *testing.T, routes map[string]http.HandlerFunc) *testServer {
	t.Helper()
	mux := http.NewServeMux()
	for p, h := range routes {
		mux.HandleFunc(p, h)
	}
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	return &testServer{Server: srv, Allowlist: []string{host}}
}

// fileHandler 返回一个直接吐出固定字节的处理器（Content-Length 由 net/http 补）。
func fileHandler(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}
}

// redirectHandler 302 到给定位置。
func redirectHandler(location string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, location, http.StatusFound)
	}
}

// sha256HexOf 计算字节串的 sha256（小写十六进制）。
func sha256HexOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// zipBytes 构造一个 zip 归档。files 的 key 是归档内路径。
func zipBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("创建 zip 条目 %s 失败: %v", name, err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("写 zip 条目 %s 失败: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

// zipBytesWithModes 构造带权限位的 zip（用于符号链接/目录条目）。
func zipBytesWithModes(t *testing.T, entries []zipEntrySpec) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.Name, Method: zip.Deflate}
		if e.Mode != 0 {
			hdr.SetMode(e.Mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("创建 zip 条目 %s 失败: %v", e.Name, err)
		}
		if _, err := w.Write(e.Body); err != nil {
			t.Fatalf("写 zip 条目 %s 失败: %v", e.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

type zipEntrySpec struct {
	Name string
	Body []byte
	Mode os.FileMode
}

// tarGzBytes 构造一个 tar.gz 归档（可带符号链接/硬链接/fifo/权限位）。
func tarGzBytes(t *testing.T, entries []tarEntrySpec) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.Name, Mode: e.Mode, Size: int64(len(e.Body)), Typeflag: e.Typeflag}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if e.Linkname != "" {
			hdr.Linkname = e.Linkname
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写 tar 头 %s 失败: %v", e.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write(e.Body); err != nil {
				t.Fatalf("写 tar 内容 %s 失败: %v", e.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
	return buf.Bytes()
}

type tarEntrySpec struct {
	Name     string
	Body     []byte
	Mode     int64
	Typeflag byte
	Linkname string
}

// entryFor 用一段字节和一份 URL 造一条清单条目。
func entryFor(version, target, asset, url string, body []byte) ManifestEntry {
	return ManifestEntry{
		Version:   version,
		TargetID:  target,
		AssetName: asset,
		URL:       url,
		SizeBytes: int64(len(body)),
		SHA256:    sha256HexOf(body),
	}
}

// fakeWorkerBytes 构建 internal/runtime/pi/fakeworker 并返回其字节（进程内缓存）。
//
// 为什么要一个**真的可执行文件**：PI_UPG01 要观察"活跃 worker 仍执行旧路径"，
// 只有真实子进程才能从 OS 进程表里读出它的 image path；写个假文件是自证。
var (
	fakeWorkerOnce        sync.Once
	fakeWorkerBytesCached []byte
	fakeWorkerErr         error
)

func fakeWorkerBytes(t *testing.T) []byte {
	t.Helper()
	fakeWorkerOnce.Do(func() {
		root := repoRoot(t)
		out := filepath.Join(os.TempDir(), "anvil-piinstall-fakeworker-"+randSuffix())
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out, "./internal/runtime/pi/fakeworker")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if combined, err := cmd.CombinedOutput(); err != nil {
			fakeWorkerErr = &buildError{msg: string(combined), err: err}
			return
		}
		body, err := os.ReadFile(out)
		if err != nil {
			fakeWorkerErr = err
			return
		}
		_ = os.Remove(out)
		fakeWorkerBytesCached = body
	})
	if fakeWorkerErr != nil {
		t.Fatalf("构建 fakeworker 失败: %v", fakeWorkerErr)
	}
	return fakeWorkerBytesCached
}

type buildError struct {
	msg string
	err error
}

func (e *buildError) Error() string { return e.err.Error() + ": " + e.msg }

// randSuffix 返回一个短随机后缀（避免并行/重复构建互相踩）。
func randSuffix() string {
	return strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
}

// expectedExecutableFor 返回目标平台的可执行文件名（测试用同名实现）。
func expectedExecutableFor(targetID string) string {
	if strings.HasPrefix(targetID, "win-") {
		return "pi.exe"
	}
	return "pi"
}
