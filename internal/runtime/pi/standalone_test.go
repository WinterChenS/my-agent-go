package pi

// PI04：官方独立发行包（不依赖宿主 Node）的探测与协议回归。
//
// 已核实的事实（2026-11-03 实测，见 docs/evidence/pi-compatibility.md）：
//   - 官方 GitHub release v1.5.0 提供 pi-windows-x64.zip；解包后是单个 pi.exe（约 123MB），
//     目录内没有任何 node.exe，SHA256 与官方 SHA256SUMS 一致。
//   - npm 形式（@earendil-works/pi-coding-agent）的 bin 指向 dist/bundle/cli.js，
//     engines 要求 node >= 22.19.0，且没有任何平台二进制的 optionalDependencies
//     —— 必须依赖宿主 Node。
//
// 两种发行形式必须分别记录；PI04 要求独立包在**无 Node 环境**下通过探测，
// 并能加载本项目的薄 extension 与 MCP。
//
// 本测试不会卸载或改名系统 Node（那是破坏性操作）。它把子进程的 PATH 收窄到
// 只含系统目录，并在启动前断言该 PATH 上解析不到 node —— 对被测进程而言，
// 这就是一个没有 Node 的环境。这是"模拟的无 Node 环境"，报告中如此标注，
// 不冒充一台真正的干净机器。
//
// 开启方式：MYAGENT_PI_STANDALONE=1

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/fixtures/openaisse"
)

// standaloneExe 返回官方独立包的 pi.exe 路径。
func standaloneExe(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	exe := filepath.Join(root, ".tools", "pi", "standalone", "v1.5.0-win-x64", "pi.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("未找到官方独立包（%s）。先执行：\n"+
			"  curl -L -o pi-windows-x64.zip https://github.com/earendil-works/pi/releases/download/v1.5.0/pi-windows-x64.zip\n"+
			"  解包到 .tools/pi/standalone/v1.5.0-win-x64/", exe)
	}
	return exe
}

// nodeFreePath 返回一个只有系统目录的 PATH，并把当前测试进程的 PATH 也换成它，
// 使 Probe（读 os.Environ）与子进程都处于同一"无 Node"环境。
func nodeFreePath(t *testing.T) string {
	t.Helper()
	rootDir := os.Getenv("SystemRoot")
	if rootDir == "" {
		rootDir = `C:\Windows`
	}
	p := filepath.Join(rootDir, "System32")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("系统目录不存在，无法构造无 Node 环境: %s", p)
	}
	t.Setenv("PATH", p)
	return p
}

// assertNoNodeOnPath 断言在给定 PATH 上解析不到 node。
// 这是"无 Node 环境"的前提条件，必须在运行被测程序之前确认。
func assertNoNodeOnPath(t *testing.T, path string) {
	t.Helper()
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		for _, name := range []string{"node.exe", "node.cmd", "node.bat"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				t.Fatalf("PATH 上仍存在 node：%s", filepath.Join(dir, name))
			}
		}
	}
	if p, err := exec.LookPath("node"); err == nil {
		t.Fatalf("仍能解析到 node（%s），不构成无 Node 环境", p)
	}
}

// assertSelfContained 断言独立包目录里没有 Node 运行时或 JS 入口，
// 排除"其实是 node + 脚本被改名"这种伪独立包。
func assertSelfContained(t *testing.T, dir string) {
	t.Helper()
	var exeSize int64
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := strings.ToLower(info.Name())
		if name == "node.exe" {
			t.Fatalf("独立包内含 node.exe：%s", p)
		}
		if name == "cli.js" {
			t.Fatalf("独立包内含 cli.js 入口（说明仍需 Node）：%s", p)
		}
		if name == "pi.exe" {
			exeSize = info.Size()
		}
		return nil
	})
	if exeSize < 50<<20 {
		t.Fatalf("pi.exe 体积异常（%d 字节），不像内置运行时的单文件发行包", exeSize)
	}
}

// TestStandaloneNodeFreeProbe 验证官方独立包在无 Node 环境下可定位、可探测且判 Ready。
func TestStandaloneNodeFreeProbe(t *testing.T) {
	if os.Getenv("MYAGENT_PI_STANDALONE") != "1" {
		t.Skip("设置 MYAGENT_PI_STANDALONE=1 才执行官方独立包验证")
	}
	if runtime.GOOS != "windows" {
		t.Skip("当前用例针对 pi-windows-x64 独立包")
	}

	exe := standaloneExe(t)
	assertSelfContained(t, filepath.Dir(exe))

	path := nodeFreePath(t)
	assertNoNodeOnPath(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Locate 内部的版本探测同样运行在无 Node 环境，因此也验证了定位链路。
	installs, err := Locate(ctx, exe)
	if err != nil {
		t.Fatalf("Locate 返回错误: %v", err)
	}
	if len(installs) == 0 {
		t.Fatal("Locate 没有返回任何安装件")
	}
	// 用户指定路径必须排首位（优先级最高）；其余发现项只能作为并列候选，
	// 不得顶替用户的选择（基线 §8：多版本显示来源、禁止静默切换）。
	for i, other := range installs {
		t.Logf("发现项[%d]: version=%s source=%s origin=%s runnable=%v path=%s",
			i, other.Version, other.Source, other.Origin, other.Runnable, other.Executable)
	}
	if !strings.EqualFold(installs[0].Executable, exe) {
		t.Fatalf("用户指定路径应排首位，实际首位是 %s", installs[0].Executable)
	}
	inst := installs[0]
	t.Logf("定位结果: version=%s origin=%s source=%s arch=%s runnable=%v",
		inst.Version, inst.Origin, inst.Source, inst.Architecture, inst.Runnable)
	if inst.Version != "1.5.0" {
		t.Errorf("独立包版本应为 1.5.0，实际 %q", inst.Version)
	}
	if !inst.Runnable {
		t.Errorf("独立包应可直接执行，Note=%q", inst.Note)
	}
	if inst.Source != SourceUserSpecified {
		t.Errorf("来源应为 user-specified，实际 %q", inst.Source)
	}

	iso := t.TempDir()
	oldIso := ProbeIsolationDir
	ProbeIsolationDir = iso
	defer func() { ProbeIsolationDir = oldIso }()

	res, err := Probe(ctx, inst)
	if err != nil {
		t.Fatalf("Probe 返回错误: %v", err)
	}
	t.Logf("探测结果: state=%s version=%s diag=%v", res.State, res.Version, res.Diagnostics)
	if res.State != StateReady {
		t.Fatalf("无 Node 环境下独立包应为 Ready，实际 %s（诊断：%v）", res.State, res.Diagnostics)
	}
	if res.Version != "1.5.0" {
		t.Errorf("探测版本应为 1.5.0，实际 %q", res.Version)
	}
	// 隔离目录里不应出现任何被改写的用户配置痕迹（PI02 的同一条纪律）。
	entries, _ := os.ReadDir(iso)
	t.Logf("隔离 agentDir 条目数=%d（Probe 只应写入运行时必要文件，不接触用户真实配置）", len(entries))
}

// TestStandaloneLoadsMCPAndExtension 验证独立包能加载本项目的薄 extension 与 MCP server。
// 这是计划里"在无 Node 目标环境验证独立发行包加载本项目薄 extension 与 MCP"的落地。
func TestStandaloneLoadsMCPAndExtension(t *testing.T) {
	if os.Getenv("MYAGENT_PI_STANDALONE") != "1" {
		t.Skip("设置 MYAGENT_PI_STANDALONE=1 才执行官方独立包验证")
	}
	if runtime.GOOS != "windows" {
		t.Skip("当前用例针对 pi-windows-x64 独立包")
	}

	root := repoRoot(t)
	exe := standaloneExe(t)

	work := t.TempDir()
	agentDir := filepath.Join(work, "agent")
	recDir := filepath.Join(work, "requests")
	srv := httptest.NewServer(openaisse.NewHandler(recDir))
	defer srv.Close()
	writeFixtureModels(t, agentDir, srv.URL)

	// 注意顺序：构建 fixture MCP 需要 go 在 PATH 上，因此必须在收窄 PATH 之前完成。
	writeFixtureMCP(t, root, agentDir)

	// 收窄 PATH 到只剩系统目录，并断言解析不到 node。
	path := nodeFreePath(t)
	assertNoNodeOnPath(t, path)

	// 子进程也要继承收窄后的 PATH：runPiRPC 从 os.Environ 起，t.Setenv 已改掉 PATH。
	launch := piLaunch{exe: exe}

	batches := [][]string{{
		`{"id":"req-cmds","type":"get_commands"}`,
		promptCmd("req-prompt", "text"),
	}}
	recs, stderr, code := runPiRPC(t, launch, work, agentDir, batches, 1, true,
		[]string{"-e", filepath.Join(root, "tests", "fixtures", "extension", "fixture.ts")},
		0, 90*time.Second)

	t.Logf("独立包 RPC：%d 条记录，退出码 %d", len(recs), code)
	if stderr != "" {
		t.Logf("stderr（前 600 字节）：\n%s", truncate(stderr, 600))
	}

	var sawSettled bool
	var buf strings.Builder
	for _, rec := range recs {
		if rec.Type == "agent_settled" {
			sawSettled = true
		}
		buf.WriteString(rec.Line)
		buf.WriteString("\n")
	}
	all := buf.String()

	// 把独立包的记录落盘，作为"两种发行形式协议一致"的可追溯证据。
	// 只做路径脱敏，不改变协议内容。
	var norm strings.Builder
	for _, rec := range recs {
		norm.WriteString(normalizeLine(rec.Line, root))
		norm.WriteString("\n")
	}
	outDir := filepath.Join(root, "internal", "runtime", "pi", "testdata", "v1.5.0")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("创建 testdata 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "standalone-rpc.jsonl"), []byte(norm.String()), 0o644); err != nil {
		t.Fatalf("写独立包记录失败: %v", err)
	}

	if !sawSettled {
		t.Errorf("独立包没有进入 agent_settled，链路未打通")
	}
	if !strings.Contains(all, "fixture-ext") {
		t.Errorf("薄 extension 未被独立包加载：get_commands 里没有 fixture-ext")
	}
	if !requestHasTool(recDir, "mcp__fixture__fixture_echo") {
		t.Errorf("MCP 工具未暴露给模型；实际工具：%v", toolNames(recDir))
	}

	// 记录独立包实际发出的工具与命令，作为可追溯证据。
	summary := map[string]any{
		"executable":    exe,
		"nodeOnPath":    false,
		"records":       len(recs),
		"exitCode":      code,
		"settled":       sawSettled,
		"extension":     strings.Contains(all, "fixture-ext"),
		"mcpTools":      toolNames(recDir),
		"recordedTools": requestHasTool(recDir, "mcp__fixture__fixture_echo"),
	}
	if b, err := json.MarshalIndent(summary, "", "  "); err == nil {
		t.Logf("独立包证据摘要：\n%s", b)
		evidence := filepath.Join(root, "docs", "evidence", "raw", "f02-standalone-summary.json")
		if err := os.MkdirAll(filepath.Dir(evidence), 0o755); err == nil {
			_ = os.WriteFile(evidence, b, 0o644)
		}
	}
}
