package pi

// 本文件是 F02 的首个失败用例：Windows 下 npm shim、中文/空格路径、多版本共存、
// 无 Pi、已保存路径失效。
//
// 纪律：测试只操作隔离临时目录与子进程环境变量（t.Setenv），不改系统 PATH。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeShim 在 dir 下写一个假的 pi.cmd，输出指定版本。
func writeShim(t *testing.T, dir, name, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	body := "@echo off\r\necho " + version + "\r\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return p
}

// isolate 把 PATH / LOCALAPPDATA / APPDATA / MYAGENT_PI_HOME 换成隔离值，
// 避免测试看到宿主真实的 Pi。
func isolate(t *testing.T) (pathDir string) {
	t.Helper()
	root := t.TempDir()
	pathDir = filepath.Join(root, "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatalf("mkdir path dir: %v", err)
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	t.Setenv("MYAGENT_PI_HOME", filepath.Join(root, "managedpi"))
	return pathDir
}

func TestLocateWindowsShimWithSpaces(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专项：npm .cmd shim 与执行策略")
	}
	pathDir := isolate(t)

	// 中文 + 空格目录，模拟 npm 全局安装路径。
	cnDir := filepath.Join(t.TempDir(), "中文 目录 with space", "node_modules", ".bin")
	writeShim(t, cnDir, "pi.cmd", "1.5.0")

	// 第二个版本，验证"多版本共存时分别列出、来源可见"。
	otherDir := filepath.Join(t.TempDir(), "pi-other")
	writeShim(t, otherDir, "pi.cmd", "1.4.0")

	t.Setenv("PATH", strings.Join([]string{cnDir, otherDir, pathDir}, string(os.PathListSeparator)))

	got, err := Locate(context.Background(), "")
	if err != nil {
		t.Fatalf("Locate 不应报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望发现 2 个 Pi，实际 %d 个: %+v", len(got), got)
	}
	versions := map[string]string{}
	for _, inst := range got {
		versions[inst.Version] = inst.Executable
		if !inst.Runnable {
			t.Fatalf("pi.cmd 应可执行: %+v", inst)
		}
		if inst.Source != SourcePATH {
			t.Fatalf("期望来源为 path，实际 %q", inst.Source)
		}
	}
	if versions["1.5.0"] == "" || versions["1.4.0"] == "" {
		t.Fatalf("两个版本都应被识别，实际 %+v", versions)
	}
	// 路径含中文与空格时仍应精确定位到文件本身，而不是拼接成 shell 命令。
	if !strings.Contains(versions["1.5.0"], "中文 目录 with space") {
		t.Fatalf("中文/空格路径未被正确解析: %s", versions["1.5.0"])
	}
}

// 只有 .ps1 时不得假装可用：执行策略会拒绝，必须标记不可直接执行。
func TestLocatePowerShellOnlyShimIsNotRunnable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专项")
	}
	isolate(t)
	dir := filepath.Join(t.TempDir(), "only-ps1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pi.ps1"), []byte("Write-Output 1.5.0"), 0o644); err != nil {
		t.Fatalf("write ps1: %v", err)
	}
	t.Setenv("PATH", dir)

	got, err := Locate(context.Background(), "")
	if err != nil {
		t.Fatalf("Locate 不应报错: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("期望 1 条诊断记录，实际 %d", len(got))
	}
	if got[0].Runnable {
		t.Fatalf("仅 .ps1 时不得标记为可执行: %+v", got[0])
	}
	if got[0].Note == "" {
		t.Fatalf("应给出不可执行的诊断说明: %+v", got[0])
	}
}

// 无 Pi：返回空列表且不报错（NotFound 是探测状态，不是调用失败）。
func TestLocateNoPiReturnsEmpty(t *testing.T) {
	isolate(t)
	got, err := Locate(context.Background(), "")
	if err != nil {
		t.Fatalf("无 Pi 时不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("期望空列表，实际 %+v", got)
	}
}

// 已保存路径失效：必须报 NotFound，但不吞掉其它可用安装（用户可重选）。
func TestLocateStaleSavedPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专项")
	}
	pathDir := isolate(t)
	writeShim(t, pathDir, "pi.cmd", "1.5.0")

	got, err := Locate(context.Background(), filepath.Join(t.TempDir(), "已经不存在的", "pi.exe"))
	if err == nil {
		t.Fatalf("失效的已保存路径必须返回错误")
	}
	if !IsNotFound(err) {
		t.Fatalf("期望 NotFound 类错误，实际 %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("失效路径不应影响其它安装的发现，实际 %+v", got)
	}
}

// 同一路径在 PATH 里出现两次只应有一条记录（去重）。
func TestLocateDedupesSameShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专项")
	}
	pathDir := isolate(t)
	writeShim(t, pathDir, "pi.cmd", "1.5.0")
	t.Setenv("PATH", strings.Join([]string{pathDir, pathDir, pathDir}, string(os.PathListSeparator)))

	got, err := Locate(context.Background(), "")
	if err != nil {
		t.Fatalf("Locate 不应报错: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("重复路径应去重为 1 条，实际 %d: %+v", len(got), got)
	}
}

// 用户指定路径必须排在结果首位（优先级最高），且其余来源仍可见以便展示"来源"。
func TestLocatePreferredTakesPriority(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专项")
	}
	pathDir := isolate(t)
	writeShim(t, pathDir, "pi.cmd", "1.4.0")

	prefDir := filepath.Join(t.TempDir(), "指定 的 Pi")
	pref := writeShim(t, prefDir, "pi.cmd", "1.5.0")

	got, err := Locate(context.Background(), pref)
	if err != nil {
		t.Fatalf("Locate 不应报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望 2 条（指定 + PATH），实际 %d: %+v", len(got), got)
	}
	if got[0].Version != "1.5.0" || got[0].Source != SourceUserSpecified {
		t.Fatalf("指定路径应排首位且来源为 user-specified，实际 %+v", got[0])
	}
	if got[1].Source != SourcePATH {
		t.Fatalf("第二条应来自 PATH，实际 %+v", got[1])
	}
}

// 版本策略：只有认证版本能 Ready，其它一律 unverified。
func TestClassifyVersion(t *testing.T) {
	state, _ := classifyVersion(DefaultPolicy, "1.5.0")
	if state != StateReady {
		t.Fatalf("1.5.0 应为 Ready，实际 %s", state)
	}
	state, diag := classifyVersion(DefaultPolicy, "1.4.0")
	if state != StateUnsupportedVersion {
		t.Fatalf("未认证版本应为 UnsupportedVersion，实际 %s", state)
	}
	if len(diag) == 0 {
		t.Fatalf("未认证版本必须附带 unverified 诊断")
	}
	state, _ = classifyVersion(DefaultPolicy, "")
	if state != StateUnsupportedVersion {
		t.Fatalf("空版本应为 unverified（UnsupportedVersion），实际 %s", state)
	}
}

// ---- PI01 的外部观察装置 ----
//
// 上面几条用例断言的是"定位结果长什么样"。这一条补的是**谁真的被执行了**：
// 候选 shim 把自身视角（真实路径、argv0、工作目录、启动命令行）追加到记录文件，
// 父进程只读那个文件；磁盘上的 fixture 树也一并落盘（路径/大小/模式/哈希）。
//
// 记录文件路径通过环境变量交给 shim，而不是塞进 argv：否则观察装置会污染
// 被观察的 argv（被测产品真正发出的参数就看不到了）。

// writeRecordingShim 写一个会自我记录的版本 shim。
// Windows 用 .cmd（走 cmd.exe /c 启动），其它平台用同名脚本（直接执行）。
func writeRecordingShim(t *testing.T, dir, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, "pi.cmd")
		body := "@echo off\r\n" +
			"if \"%FAKEWORKER_RECORD_SELF%\"==\"\" goto version\r\n" +
			">>\"%FAKEWORKER_RECORD_SELF%\" echo self=%~f0\r\n" +
			">>\"%FAKEWORKER_RECORD_SELF%\" echo argv0=%0\r\n" +
			">>\"%FAKEWORKER_RECORD_SELF%\" echo cwd=%CD%\r\n" +
			">>\"%FAKEWORKER_RECORD_SELF%\" echo cmdline=%CMDCMDLINE%\r\n" +
			">>\"%FAKEWORKER_RECORD_SELF%\" echo arg1=%1\r\n" +
			":version\r\n" +
			"if \"%1\"==\"--version\" echo " + version + "\r\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write shim: %v", err)
		}
		return p
	}
	p := filepath.Join(dir, "pi")
	body := "#!/bin/sh\n" +
		"if [ -n \"$FAKEWORKER_RECORD_SELF\" ]; then\n" +
		"  {\n" +
		"    printf 'self=%s\n' \"$0\"\n" +
		"    printf 'argv0=%s\n' \"$0\"\n" +
		"    printf 'cwd=%s\n' \"$PWD\"\n" +
		"    printf 'cmdline=%s\n' \"$0 $*\"\n" +
		"    printf 'arg1=%s\n' \"$1\"\n" +
		"  } >> \"$FAKEWORKER_RECORD_SELF\"\n" +
		"fi\n" +
		"if [ \"$1\" = \"--version\" ]; then echo " + version + "; fi\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return p
}

// TestLocateRecordsChildSelfReport 是 PI01 的 external-observation 取证点。
//
// 观察链路：Locate → probeVersion → 真实子进程 → 子进程自己写记录文件 → 测试读回。
// 事实（executedPath / executedFromShim / priorityOrder / treeSha256）全部来自磁盘，
// 不是从 Locate 的返回值里推断的；Locate 的返回值只用于**交叉核对**（不一致即判失败）。
func TestLocateRecordsChildSelfReport(t *testing.T) {
	root := t.TempDir()
	isolate(t) // 清掉宿主真实 Pi 的其它发现来源
	bin := filepath.Join(root, "bin")
	shim := writeRecordingShim(t, bin, "1.5.0")
	t.Setenv("PATH", bin)

	obs := piObservationDir()
	var recordPath string
	if obs != "" {
		// Clean 同时把分隔符规范成平台形式：cmd.exe 的 >> 重定向不认 D:/a/b 这种写法，
		// 写成正斜杠时文件会被静默丢弃（实测）。
		recordPath = filepath.Clean(filepath.Join(obs, "child-self.log"))
		// 目录必须先存在：cmd.exe 的 >> 重定向在父目录不存在时会**静默**丢内容，
		// 那样"子进程没写"和"目录没建"就分不开了。
		if err := os.MkdirAll(filepath.Dir(recordPath), 0o755); err != nil {
			t.Fatalf("创建观察目录失败: %v", err)
		}
		_ = os.Remove(recordPath)
		t.Setenv("FAKEWORKER_RECORD_SELF", recordPath)
	}

	got, err := Locate(context.Background(), "")
	if err != nil {
		t.Fatalf("Locate 不应报错: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("隔离 PATH 下应只发现 1 个 Pi，实际 %d: %+v", len(got), got)
	}
	if got[0].Version != "1.5.0" {
		t.Fatalf("版本应为 1.5.0，实际 %q（Note=%q）", got[0].Version, got[0].Note)
	}
	if !got[0].Runnable {
		t.Fatalf("可执行的版本 shim 应标 Runnable: %+v", got[0])
	}

	if obs == "" {
		return // 本地 focused 运行：不落盘
	}

	entries := walkTree(t, root)
	writeArtifact(t, "located-tree.json", entries)
	digest := treeDigest(t, entries)

	recs := parseKVRecords(t, recordPath)
	if len(recs) == 0 {
		t.Fatal("定位过程没有真正执行候选：子进程没有写下任何自述记录")
	}
	writeArtifact(t, "child-record.json", recs)

	var order []string
	viaLauncher := "direct"
	for _, rec := range recs {
		if p := strings.TrimSpace(rec["self"]); p != "" {
			order = append(order, p)
		}
		if strings.Contains(strings.ToLower(rec["cmdline"]), "cmd.exe") {
			viaLauncher = "cmd.exe /c"
		}
	}
	if len(order) == 0 {
		t.Fatal("子进程自述里没有自身路径，无法判定真正执行的是哪个文件")
	}

	// 交叉核对：产品报告的 Executable 必须就是子进程自己说的那个文件。
	// 不一致说明定位结果与真实执行脱节（例如被 shell 重新拆分/解析到别处）。
	// 8.3 短名（ADMINI~1）与长名指向同一文件，所以用 os.SameFile 而不是纯字符串比较。
	if !sameFile(order[0], got[0].Executable) {
		t.Fatalf("定位结果与真实执行的路径不一致：Locate=%s 子进程自述=%s",
			got[0].Executable, order[0])
	}
	if !sameFile(shim, order[0]) {
		t.Fatalf("被执行的不是 fixture shim：%s（shim=%s）", order[0], shim)
	}

	writePiObservation(t, map[string]any{
		"executedPath":               order[0],
		"executedFromShim":           viaLauncher == "cmd.exe /c",
		"viaLauncher":                viaLauncher,
		"locatorExecutable":          got[0].Executable,
		"executedPathMatchesLocator": true,
		"priorityOrder":              order,
		"probeCount":                 len(recs),
		"treeSha256":                 digest,
		"treeEntries":                len(entries),
		"fixtureShim":                shim,
		"recordedCwd":                recs[0]["cwd"],
		"recordedArg1":               recs[0]["arg1"],
	}, nil)
}

// sameFile 判断两个路径指向同一个文件（容忍 8.3 短名与大小写差异）。
// 不能用纯字符串比较：cmd.exe 会把 %~f0 展开成短名（ADMINI~1），
// 而产品报的是长名——两者是同一个文件，比较错了会把真观察判成假失败。
func sameFile(a, b string) bool {
	if strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	if aerr != nil || berr != nil {
		return false
	}
	return os.SameFile(ai, bi)
}
