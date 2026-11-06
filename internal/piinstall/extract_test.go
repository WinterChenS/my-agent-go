package piinstall

// PI_INS01 的第三层：归档边界。zip 与 tar.gz 都必须做路径/链接/配额检查，
// 且解出来的包必须真的含有目标平台的可执行体（否则"解压成功"没有意义）。

import (
	"archive/tar"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeArchive(t *testing.T, body []byte, name string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatalf("写归档 %s 失败: %v", p, err)
	}
	return p
}

func TestExtractRejectsEscapingArchives(t *testing.T) {
	piName := expectedExecutableFor("win-x64")
	zipCases := map[string][]byte{
		"zip 相对越界": zipBytes(t, map[string][]byte{piName: []byte("x"), "../escape": []byte("x")}),
		"zip 绝对路径": zipBytes(t, map[string][]byte{piName: []byte("x"), "/absolute": []byte("x")}),
		"zip 盘符路径": zipBytes(t, map[string][]byte{piName: []byte("x"), `C:\windows\x`: []byte("x")}),
		"zip 符号链接": zipBytesWithModes(t, []zipEntrySpec{
			{Name: piName, Body: []byte("x"), Mode: 0o755},
			{Name: "link-outside", Body: []byte("/etc/passwd"), Mode: os.ModeSymlink | 0o777},
		}),
		"zip 重复路径": zipBytes(t, map[string][]byte{piName: []byte("x")}),
	}
	// 重复路径要单独构造（map 去重，必须用有序构造器）。
	zipCases["zip 重复路径"] = zipBytesWithModes(t, []zipEntrySpec{
		{Name: piName, Body: []byte("first"), Mode: 0o755},
		{Name: piName, Body: []byte("second"), Mode: 0o755},
	})
	for name, body := range zipCases {
		archive := writeArchive(t, body, "pkg.zip")
		dest := filepath.Join(t.TempDir(), "out")
		if _, err := Extract(archive, dest, piName, DefaultExtractLimits()); err == nil {
			t.Fatalf("%s 必须被拒绝", name)
		}
		assertNothingEscaped(t, dest)
	}

	tarCases := map[string][]byte{
		"tar 相对越界": tarGzBytes(t, []tarEntrySpec{
			{Name: piName, Body: []byte("x"), Mode: 0o755},
			{Name: "../escape", Body: []byte("x")},
		}),
		"tar 绝对路径": tarGzBytes(t, []tarEntrySpec{
			{Name: piName, Body: []byte("x"), Mode: 0o755},
			{Name: "/absolute", Body: []byte("x")},
		}),
		"tar 符号链接": tarGzBytes(t, []tarEntrySpec{
			{Name: piName, Body: []byte("x"), Mode: 0o755},
			{Name: "link-outside", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		}),
		"tar 硬链接": tarGzBytes(t, []tarEntrySpec{
			{Name: piName, Body: []byte("x"), Mode: 0o755},
			{Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "../../etc/passwd"},
		}),
		"tar 设备节点": tarGzBytes(t, []tarEntrySpec{
			{Name: piName, Body: []byte("x"), Mode: 0o755},
			{Name: "fifo", Typeflag: tar.TypeFifo},
		}),
	}
	for name, body := range tarCases {
		archive := writeArchive(t, body, "pkg.tar.gz")
		dest := filepath.Join(t.TempDir(), "out")
		if _, err := Extract(archive, dest, piName, DefaultExtractLimits()); err == nil {
			t.Fatalf("%s 必须被拒绝", name)
		}
		assertNothingEscaped(t, dest)
	}
}

func TestExtractPreservesExecutableBitForTar(t *testing.T) {
	piName := "pi"
	archive := writeArchive(t, tarGzBytes(t, []tarEntrySpec{
		{Name: piName, Body: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Name: "docs/readme.md", Body: []byte("hi"), Mode: 0o644},
	}), "pkg.tar.gz")
	dest := filepath.Join(t.TempDir(), "out")
	got, err := Extract(archive, dest, piName, DefaultExtractLimits())
	if err != nil {
		t.Fatalf("解压合法 tar.gz 失败: %v", err)
	}
	if got.Files != 2 {
		t.Errorf("解出的文件数 = %d，期望 2", got.Files)
	}
	st, err := os.Stat(filepath.Join(dest, piName))
	if err != nil {
		t.Fatalf("stat 解出的可执行体失败: %v", err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Errorf("tar 条目的执行位没有被保留: %s", st.Mode())
	}
	if got.ExecutableSHA256 != sha256HexOf([]byte("#!/bin/sh\n")) {
		t.Errorf("可执行体哈希 = %s，期望 %s", got.ExecutableSHA256, sha256HexOf([]byte("#!/bin/sh\n")))
	}
}

func TestExtractEnforcesLimits(t *testing.T) {
	piName := expectedExecutableFor("win-x64")
	files := map[string][]byte{piName: []byte("x")}
	for i := 0; i < 8; i++ {
		files["docs/f"+string(rune('a'+i))+".md"] = []byte("body")
	}
	archive := writeArchive(t, zipBytes(t, files), "pkg.zip")

	limits := DefaultExtractLimits()
	limits.MaxFiles = 3
	dest := filepath.Join(t.TempDir(), "out")
	if _, err := Extract(archive, dest, piName, limits); err == nil {
		t.Fatal("文件数超过上限必须被拒绝")
	}

	limits = DefaultExtractLimits()
	limits.MaxTotalBytes = 2
	dest = filepath.Join(t.TempDir(), "out2")
	if _, err := Extract(archive, dest, piName, limits); err == nil {
		t.Fatal("总解压字节数超过上限必须被拒绝")
	}
}

func TestExtractRequiresExpectedExecutable(t *testing.T) {
	piName := expectedExecutableFor("win-x64")
	archive := writeArchive(t, zipBytes(t, map[string][]byte{"readme.md": []byte("no pi here")}), "pkg.zip")
	dest := filepath.Join(t.TempDir(), "out")
	if _, err := Extract(archive, dest, piName, DefaultExtractLimits()); err == nil {
		t.Fatal("包里没有目标可执行体时必须拒绝")
	}
}

func TestValidateExecutableForTarget(t *testing.T) {
	exe := fakeWorkerBytes(t)
	p := filepath.Join(t.TempDir(), "pi.exe")
	if err := os.WriteFile(p, exe, 0o755); err != nil {
		t.Fatalf("写可执行体失败: %v", err)
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		if err := ValidateExecutableForTarget(p, "win-x64"); err != nil {
			t.Fatalf("真实 windows/amd64 可执行体被判为架构不符: %v", err)
		}
		if err := ValidateExecutableForTarget(p, "linux-x64"); err == nil {
			t.Fatal("PE 文件不能冒充 linux-x64 目标")
		}
	}
	// 不是可执行体（纯文本）必须被拒绝：不能靠扩展名蒙过目标架构校验。
	text := filepath.Join(t.TempDir(), "pi.exe")
	if err := os.WriteFile(text, []byte("not a binary"), 0o755); err != nil {
		t.Fatalf("写文本文件失败: %v", err)
	}
	if err := ValidateExecutableForTarget(text, "win-x64"); err == nil {
		t.Fatal("非可执行体必须被拒绝")
	}
}

// assertNothingEscaped 断言解压目录之外没有被写入任何东西。
func assertNothingEscaped(t *testing.T, dest string) {
	t.Helper()
	parent := filepath.Dir(dest)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if filepath.Join(parent, e.Name()) == dest {
			continue
		}
		if e.Name() == "escape" || e.Name() == "absolute" || strings.HasPrefix(e.Name(), "link") {
			t.Fatalf("越界写入发生在解压目录之外: %s", filepath.Join(parent, e.Name()))
		}
	}
}
