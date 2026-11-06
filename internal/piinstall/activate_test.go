package piinstall

// active 指针与回滚目标：切换必须原子，回滚目标必须"仍在且哈希仍对"。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeVersionDir(t *testing.T, managedRoot, version, target string, exe []byte) (string, string) {
	t.Helper()
	dir := filepath.Join(managedRoot, "versions", version+"-"+target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建版本目录失败: %v", err)
	}
	p := filepath.Join(dir, expectedExecutableFor(target))
	if err := os.WriteFile(p, exe, 0o755); err != nil {
		t.Fatalf("写版本可执行体失败: %v", err)
	}
	return dir, sha256HexOf(exe)
}

func TestActivePointerRoundTripAndAtomicReplace(t *testing.T) {
	root := t.TempDir()
	if p, err := ReadActive(root); err != nil || p != nil {
		t.Fatalf("空托管根应返回 nil 指针: p=%v err=%v", p, err)
	}
	dir, sum := writeVersionDir(t, root, "1.5.0", "win-x64", []byte("v1"))
	pointer := ActivePointer{
		SchemaVersion:    ActiveSchemaVersion,
		Version:          "1.5.0",
		TargetID:         "win-x64",
		VersionDir:       relToRoot(t, root, dir),
		Executable:       expectedExecutableFor("win-x64"),
		ExecutableSHA256: sum,
		InstalledAt:      time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
		InstallationID:   "pi-test",
	}
	if err := WriteActive(root, pointer); err != nil {
		t.Fatalf("写 active 指针失败: %v", err)
	}
	got, err := ReadActive(root)
	if err != nil || got == nil {
		t.Fatalf("读回 active 指针失败: %v", err)
	}
	if got.Version != pointer.Version || got.ExecutableSHA256 != sum {
		t.Fatalf("读回的指针与写入不一致: %+v", got)
	}
	exe, err := ActiveExecutable(root, got)
	if err != nil {
		t.Fatalf("解析 active 可执行体失败: %v", err)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("active 可执行体不存在: %v", err)
	}
	// 原子替换：临时文件不得残留。
	if _, err := os.Stat(filepath.Join(root, ActiveFileName+".tmp")); err == nil {
		t.Fatal("active.json.tmp 残留：写入不是原子的")
	}
}

func TestRollbackTargetRequiresMatchingHash(t *testing.T) {
	root := t.TempDir()
	prevDir, prevSum := writeVersionDir(t, root, "1.4.0", "win-x64", []byte("v0"))
	curDir, curSum := writeVersionDir(t, root, "1.5.0", "win-x64", []byte("v1"))
	pointer := ActivePointer{
		SchemaVersion: ActiveSchemaVersion, Version: "1.5.0", TargetID: "win-x64",
		VersionDir: relToRoot(t, root, curDir), Executable: expectedExecutableFor("win-x64"),
		ExecutableSHA256: curSum, InstalledAt: time.Now().UTC(), InstallationID: "pi-cur",
		Previous: &PreviousVersion{
			Version: "1.4.0", TargetID: "win-x64", VersionDir: relToRoot(t, root, prevDir),
			Executable: expectedExecutableFor("win-x64"), ExecutableSHA256: prevSum,
			InstalledAt: time.Now().UTC(),
		},
	}
	prev, exe, err := RollbackTarget(root, &pointer)
	if err != nil {
		t.Fatalf("合法回滚目标被拒绝: %v", err)
	}
	if prev == nil || exe == "" {
		t.Fatal("合法回滚目标必须返回 previous 与可执行体路径")
	}

	// 篡改旧版本可执行体：哈希不符 → 回滚目标失效（不是"文件在就行"）。
	tampered := *pointer.Previous
	tampered.ExecutableSHA256 = strings64()
	pointer.Previous = &tampered
	if _, _, err := RollbackTarget(root, &pointer); err == nil {
		t.Fatal("旧版本哈希不符时回滚目标必须失效")
	}

	// 旧版本目录被删：同样失效。
	pointer.Previous = &PreviousVersion{
		Version: "1.4.0", TargetID: "win-x64", VersionDir: "versions/1.4.0-win-x64",
		Executable: expectedExecutableFor("win-x64"), ExecutableSHA256: prevSum,
	}
	if err := os.RemoveAll(prevDir); err != nil {
		t.Fatalf("删除旧版本目录失败: %v", err)
	}
	if _, _, err := RollbackTarget(root, &pointer); err == nil {
		t.Fatal("旧版本目录不存在时回滚目标必须失效")
	}
}

func TestEnsureInsideManagedRootRejectsExternalPaths(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "versions", "1.5.0-win-x64")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := EnsureInsideManagedRoot(root, inside); err != nil {
		t.Fatalf("托管根内的路径被拒绝: %v", err)
	}
	outside := t.TempDir()
	if err := EnsureInsideManagedRoot(root, outside); err == nil {
		t.Fatal("托管根之外的路径必须被拒绝（外部 Pi 不可变）")
	}
	if err := EnsureInsideManagedRoot(root, filepath.Join(root, "..", "escape")); err == nil {
		t.Fatal("通过 .. 逃出托管根的路径必须被拒绝")
	}
}

func TestActivePointerJSONIsStable(t *testing.T) {
	root := t.TempDir()
	_, sum := writeVersionDir(t, root, "1.5.0", "win-x64", []byte("v1"))
	pointer := ActivePointer{
		SchemaVersion: ActiveSchemaVersion, Version: "1.5.0", TargetID: "win-x64",
		VersionDir: "versions/1.5.0-win-x64", Executable: expectedExecutableFor("win-x64"),
		ExecutableSHA256: sum, InstalledAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
		InstallationID: "pi-test",
	}
	if err := WriteActive(root, pointer); err != nil {
		t.Fatalf("写 active 指针失败: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ActiveFileName))
	if err != nil {
		t.Fatalf("读 active.json 失败: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("active.json 不是合法 JSON: %v", err)
	}
	for _, key := range []string{"schemaVersion", "version", "targetId", "versionDir", "executable", "executableSha256", "installedAt"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("active.json 缺少字段 %q", key)
		}
	}
}

func relToRoot(t *testing.T, root, p string) string {
	t.Helper()
	rel, err := filepath.Rel(root, p)
	if err != nil {
		t.Fatalf("计算相对路径失败: %v", err)
	}
	return filepath.ToSlash(rel)
}

func strings64() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return sha256HexOf(b)
}

// TestHashFileContentMatchesActivePointer 守"导出的哈希实现就是激活时用的那份"。
//
// 绑定层（internal/app）解析 active 指针时用 HashFileContent 复核可执行体，
// 并把期望/实际哈希写进错误信息。它必须与 ReadActive 记录的 executableSha256
// 同一口径，否则"产品算的哈希"与"证据里的哈希"会分叉。
func TestHashFileContentMatchesActivePointer(t *testing.T) {
	root := t.TempDir()
	payload := []byte("managed-pi-bytes")
	dir, sum := writeVersionDir(t, root, "1.5.0", "win-x64", payload)
	pointer := ActivePointer{
		SchemaVersion: ActiveSchemaVersion, Version: "1.5.0", TargetID: "win-x64",
		VersionDir: relToRoot(t, root, dir), Executable: expectedExecutableFor("win-x64"),
		ExecutableSHA256: sum, InstalledAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
		InstallationID: "pi-test",
	}
	if err := WriteActive(root, pointer); err != nil {
		t.Fatalf("写 active 指针失败: %v", err)
	}
	got, err := ReadActive(root)
	if err != nil || got == nil {
		t.Fatalf("读回 active 指针失败: %v", err)
	}
	exe := filepath.Join(root, filepath.FromSlash(got.VersionDir), filepath.FromSlash(got.Executable))
	actual, err := HashFileContent(exe)
	if err != nil {
		t.Fatalf("HashFileContent: %v", err)
	}
	if actual != got.ExecutableSHA256 {
		t.Fatalf("导出的哈希 %s 与 ReadActive 记录的 %s 不一致", actual, got.ExecutableSHA256)
	}

	// 改一个字节：必须变。证明它真的在读文件内容，而不是回显指针。
	tampered := append([]byte{}, payload...)
	tampered[0] ^= 0xFF
	if err := os.WriteFile(exe, tampered, 0o755); err != nil {
		t.Fatalf("篡改可执行体失败: %v", err)
	}
	after, err := HashFileContent(exe)
	if err != nil {
		t.Fatalf("HashFileContent（篡改后）: %v", err)
	}
	if after == actual {
		t.Fatal("可执行体内容变了但哈希没变：HashFileContent 没有真的读文件")
	}
}
