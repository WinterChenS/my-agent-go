package piinstall

// PI_INS01 的第一层：受信清单。清单是运行期唯一的信任源，因此解析必须 fail closed。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

const (
	realWinX64SHA256 = "12d58a70e80b16ba9f764950b7a42bdeb07959495c0db68396cf2988c0b71af3"
	realWinX64Size   = int64(61890147)
)

func appErrCode(err error) string {
	var appErr *contracts.AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return ""
}

func TestEmbeddedManifestCarriesRealWinX64Entry(t *testing.T) {
	m, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatalf("加载嵌入清单失败: %v", err)
	}
	entry, err := m.Entry("1.5.0", "win-x64")
	if err != nil {
		t.Fatalf("取 win-x64 条目失败: %v", err)
	}
	if entry.SHA256 != realWinX64SHA256 {
		t.Errorf("win-x64 sha256 = %s，期望官方 SHA256SUMS 里的 %s", entry.SHA256, realWinX64SHA256)
	}
	if entry.SizeBytes != realWinX64Size {
		t.Errorf("win-x64 sizeBytes = %d，期望本地官方包实测 %d", entry.SizeBytes, realWinX64Size)
	}
	if !strings.HasPrefix(entry.URL, "https://github.com/earendil-works/pi/releases/download/v1.5.0/") {
		t.Errorf("win-x64 url 不是官方发布地址: %s", entry.URL)
	}
	if entry.Signature != "" {
		t.Errorf("上游 v1.5.0 没有独立签名，signature 必须为空，实际 %q", entry.Signature)
	}
	// 本地官方包必须与清单哈希逐字节一致（只读引用，不写入）。
	local := filepath.Join(repoRoot(t), ".tools", "pi", "standalone", "pi-windows-x64-1.5.0.zip")
	if sum, err := HashFileContent(local); err != nil {
		t.Fatalf("读取本地官方包失败: %v", err)
	} else if sum != entry.SHA256 {
		t.Errorf("本地官方包 sha256 = %s，与清单 %s 不一致", sum, entry.SHA256)
	}
}

func TestEmbeddedManifestRefusesTargetsWithoutMeasuredSize(t *testing.T) {
	m, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatalf("加载嵌入清单失败: %v", err)
	}
	// 六个目标都在清单里（URL/hash 来自官方 SHA256SUMS），但只有 win-x64 在
	// 离线条件下能测出精确大小；其余五个 sizeBytes=0，必须被 loader 拒绝。
	blocked := m.UnavailableTargets("1.5.0")
	// 顺序与 TargetIDs（六目标展示顺序）一致，便于证据里逐条比对。
	want := []string{"win-arm64", "mac-x64", "mac-arm64", "linux-x64", "linux-arm64"}
	if strings.Join(blocked, ",") != strings.Join(want, ",") {
		t.Fatalf("不可用目标 = %v，期望 %v", blocked, want)
	}
	for _, target := range want {
		_, err := m.Entry("1.5.0", target)
		if err == nil {
			t.Fatalf("%s 的 sizeBytes=0，Entry 必须拒绝（不能编造大小）", target)
		}
		if code := appErrCode(err); code != contracts.CodeUnsupported {
			t.Fatalf("%s 的拒绝码 = %q，期望 %q（err=%v）", target, code, contracts.CodeUnsupported, err)
		}
		if !strings.Contains(err.Error(), "sizeBytes") {
			t.Errorf("%s 的拒绝理由必须说明 sizeBytes 缺失: %v", target, err)
		}
	}
}

func TestParseManifestRejectsUnknownFieldsAndExtraContent(t *testing.T) {
	good := `{"schema":1,"generatedAt":"2026-11-05T00:00:00Z","source":"https://github.com/earendil-works/pi/releases/download/v1.5.0/SHA256SUMS",` +
		`"entries":[{"version":"1.5.0","targetId":"win-x64","assetName":"pi-windows-x64.zip",` +
		`"url":"https://github.com/earendil-works/pi/releases/download/v1.5.0/pi-windows-x64.zip",` +
		`"sizeBytes":10,"sha256":"` + realWinX64SHA256 + `"}]}`
	if _, err := ParseManifest([]byte(good)); err != nil {
		t.Fatalf("合法清单被拒绝: %v", err)
	}

	cases := map[string]string{
		"未知顶层字段":         strings.Replace(good, `"schema":1`, `"schema":1,"trusted":true`, 1),
		"未知条目字段":         strings.Replace(good, `"sizeBytes":10`, `"sizeBytes":10,"mirror":"http://x"`, 1),
		"文档后夹带":          good + ` {"schema":1}`,
		"schema 非法":      strings.Replace(good, `"schema":1`, `"schema":2`, 1),
		"source 非 https": strings.Replace(good, `"source":"https://github.com`, `"source":"http://github.com`, 1),
	}
	for name, raw := range cases {
		if _, err := ParseManifest([]byte(raw)); err == nil {
			t.Fatalf("%s 的清单必须被拒绝", name)
		}
	}
}

func TestParseManifestRejectsInvalidEntries(t *testing.T) {
	base := ManifestEntry{
		Version:   "1.5.0",
		TargetID:  "win-x64",
		AssetName: "pi-windows-x64.zip",
		URL:       "https://github.com/earendil-works/pi/releases/download/v1.5.0/pi-windows-x64.zip",
		SizeBytes: 10,
		SHA256:    realWinX64SHA256,
	}
	cases := map[string]func(*ManifestEntry){
		"大写哈希":        func(e *ManifestEntry) { e.SHA256 = strings.ToUpper(e.SHA256) },
		"短哈希":         func(e *ManifestEntry) { e.SHA256 = e.SHA256[:63] },
		"http 地址":     func(e *ManifestEntry) { e.URL = strings.Replace(e.URL, "https://", "http://", 1) },
		"非白名单主机":      func(e *ManifestEntry) { e.URL = "https://evil.example/pi-windows-x64.zip" },
		"URL 带凭证":     func(e *ManifestEntry) { e.URL = strings.Replace(e.URL, "https://", "https://user:pass@", 1) },
		"IP 字面量":      func(e *ManifestEntry) { e.URL = "https://93.184.216.34/pi-windows-x64.zip" },
		"资产名与 URL 不符": func(e *ManifestEntry) { e.AssetName = "other.zip" },
		"资产名带路径分隔符":   func(e *ManifestEntry) { e.AssetName = "../pi-windows-x64.zip" },
		"未知目标":        func(e *ManifestEntry) { e.TargetID = "solaris-x64" },
		"版本不是三段式":     func(e *ManifestEntry) { e.Version = "1.5" },
		"负大小":         func(e *ManifestEntry) { e.SizeBytes = -1 },
		"签名不是 base64": func(e *ManifestEntry) { e.Signature = "not-base64!!" },
	}
	for name, mutate := range cases {
		entry := base
		mutate(&entry)
		m := Manifest{Schema: ManifestSchema, GeneratedAt: "2026-11-05T00:00:00Z",
			Source:  "https://github.com/earendil-works/pi/releases/download/v1.5.0/SHA256SUMS",
			Entries: []ManifestEntry{entry}}
		if err := m.Validate(); err == nil {
			t.Fatalf("%s 的条目必须被拒绝", name)
		}
	}
}

func TestParseManifestRejectsDuplicateVersionTarget(t *testing.T) {
	entry := ManifestEntry{
		Version: "1.5.0", TargetID: "win-x64", AssetName: "pi-windows-x64.zip",
		URL:       "https://github.com/earendil-works/pi/releases/download/v1.5.0/pi-windows-x64.zip",
		SizeBytes: 10, SHA256: realWinX64SHA256,
	}
	m := Manifest{Schema: ManifestSchema, GeneratedAt: "2026-11-05T00:00:00Z",
		Source:  "https://github.com/earendil-works/pi/releases/download/v1.5.0/SHA256SUMS",
		Entries: []ManifestEntry{entry, entry}}
	if err := m.Validate(); err == nil {
		t.Fatal("重复的 version+target 必须被拒绝（同一目标两份哈希含义不唯一）")
	}
}

func TestManifestEntryRejectsUnknownVersion(t *testing.T) {
	m, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatalf("加载嵌入清单失败: %v", err)
	}
	if _, err := m.Entry("9.9.9", "win-x64"); err == nil {
		t.Fatal("清单里没有的版本必须被拒绝")
	}
	if _, err := m.Entry("1.5.0", "win-x64"); err != nil {
		t.Fatalf("已有版本被拒绝: %v", err)
	}
}

// repoRoot 从测试工作目录向上找 go.mod。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到仓库根（go.mod）")
		}
		dir = parent
	}
}
