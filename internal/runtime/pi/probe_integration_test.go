package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// hashFile 计算文件 sha256（oracle：用户配置 marker 前后必须一致）。
func hashFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 marker 失败: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// makeVersionShim 创建一个只响应 --version 的 fixture（不触碰模型/MCP）。
//
// 形态必须跟着平台走：BuildLaunch 按扩展名推导启动包装（.cmd → cmd /c，.ps1 → powershell -File，
// 其余按可执行文件直接启动）。若在任何平台都生成 .cmd，Unix 上测到的是"缺 cmd.exe"这条依赖分支，
// 而不是 probe 的隔离/版本策略逻辑——CI 上正是这样红的（DependencyMissing: cmd.exe not found）。
//
// 设置 FAKEWORKER_RECORD_ENV 时，shim 会把**它自己收到的环境变量**落盘（PI02 的外部观察源）：
// 父进程只能读那个文件，不能替子进程说它看到了什么。
func makeVersionShim(t *testing.T, dir, version string) PiInstallation {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var p, content string
	if runtime.GOOS == "windows" {
		p = filepath.Join(dir, "pi.cmd")
		content = "@echo off\r\n" +
			"if \"%FAKEWORKER_RECORD_ENV%\"==\"\" goto version\r\n" +
			"set > \"%FAKEWORKER_RECORD_ENV%\"\r\n" +
			":version\r\n" +
			"if \"%1\"==\"--version\" echo " + version + "\r\n"
	} else {
		p = filepath.Join(dir, "pi")
		content = "#!/bin/sh\n" +
			"if [ -n \"$FAKEWORKER_RECORD_ENV\" ]; then env > \"$FAKEWORKER_RECORD_ENV\"; fi\n" +
			"if [ \"$1\" = \"--version\" ]; then echo " + version + "; fi\n"
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return PiInstallation{
		ID:         "fixture:" + p,
		Executable: p,
		Origin:     OriginExternal,
	}
}

// TestProbeHasNoUserConfigSideEffects 验证 PI02：隔离 agentDir 下 probe，
// 不访问真实 provider、不启动用户 MCP、用户配置 marker 哈希不变，且 probe 在限定时间内返回
// （未进入 agent loop / 未发起模型请求）。
func TestProbeHasNoUserConfigSideEffects(t *testing.T) {
	// 模拟真实用户 HOME，并在其中放一个 marker 文件（代表既有用户配置）。
	userHome := t.TempDir()
	markerDir := filepath.Join(userHome, ".pi", "agent")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatalf("mkdir marker: %v", err)
	}
	markerPath := filepath.Join(markerDir, "config.marker")
	if err := os.WriteFile(markerPath, []byte("user-config-sentinel-"+time.Now().Format(time.RFC3339Nano)), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	before := hashFile(t, markerPath)

	// 独立 agentDir：probe 的子进程配置必须落在这里，而不是真实用户 HOME。
	agentDir := t.TempDir()
	oldIso := ProbeIsolationDir
	ProbeIsolationDir = agentDir
	defer func() { ProbeIsolationDir = oldIso }()

	// 把进程级 HOME/USERPROFILE/APPDATA 指向 userHome，模拟“这是用户真实 home”。
	oldHome := os.Getenv("HOME")
	oldUP := os.Getenv("USERPROFILE")
	oldAD := os.Getenv("APPDATA")
	os.Setenv("HOME", userHome)
	os.Setenv("USERPROFILE", userHome)
	os.Setenv("APPDATA", userHome)
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("USERPROFILE", oldUP)
		os.Setenv("APPDATA", oldAD)
	}()

	inst := makeVersionShim(t, filepath.Join(t.TempDir(), "bin"), "1.5.0")

	// PI02 的外部观察装置（filesystem）：
	//   - fake-home-before/after.json：只读磁盘遍历出来的用户 HOME 快照（前后对比）；
	//   - child-env.txt / child-env.json：**probe 子进程自己**把 os.Environ 落盘的副本，
	//     父进程只读。childHome / childAgentDir 取自那个文件，不取自 Probe 的入参。
	obsDir := piObservationDir()
	var (
		childEnvRaw  string
		beforeTree   []treeEntry
		childEnvPath string
	)
	if obsDir != "" {
		if err := os.MkdirAll(obsDir, 0o755); err != nil {
			t.Fatalf("创建观察目录失败: %v", err)
		}
		beforeTree = walkTree(t, userHome)
		writeArtifact(t, "fake-home-before.json", beforeTree)
		childEnvRaw = filepath.Clean(filepath.Join(obsDir, "child-env.txt"))
		_ = os.Remove(childEnvRaw)
		childEnvPath = childEnvRaw
		t.Setenv("FAKEWORKER_RECORD_ENV", childEnvPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := Probe(ctx, inst)
	if err != nil {
		t.Fatalf("Probe 报错: %v（state=%s）", err, res.State)
	}
	// probe 不得进入 agent loop：必须在超时前完成（fixture 仅响应 --version）。
	if res.State != StateReady {
		t.Fatalf("认证版本 1.5.0 fixture 应 Ready，实际 state=%s diag=%v", res.State, res.Diagnostics)
	}
	if res.Version != "1.5.0" {
		t.Fatalf("版本应解析为 1.5.0，实际 %q", res.Version)
	}

	// oracle 1：marker 哈希不变。
	after := hashFile(t, markerPath)
	if after != before {
		t.Fatalf("用户配置 marker 哈希发生变化（probe 产生副作用）：before=%s after=%s", before, after)
	}
	// oracle 2：用户 HOME 下除 marker 外不得出现新文件。
	entries, _ := os.ReadDir(userHome)
	for _, e := range entries {
		if e.IsDir() && e.Name() == ".pi" {
			sub, _ := os.ReadDir(markerDir)
			for _, f := range sub {
				if f.Name() != "config.marker" {
					t.Fatalf("用户 HOME 下出现意外文件: %s（probe 疑似写入用户配置）", f.Name())
				}
			}
			continue
		}
		t.Fatalf("用户 HOME 根目录出现意外条目: %s（probe 疑似写入用户配置）", e.Name())
	}
	// oracle 3：agentDir 应为 probe 实际隔离目录（证明配置被重定向到隔离区）。
	if _, statErr := os.Stat(agentDir); statErr != nil {
		t.Fatalf("隔离 agentDir 未被创建/使用: %v", statErr)
	}

	if obsDir == "" {
		return // 本地 focused 运行：不落盘
	}

	// 读回外部观察值：用户 HOME 的前后快照 + 子进程自述环境。
	afterTree := walkTree(t, userHome)
	writeArtifact(t, "fake-home-after.json", afterTree)
	added, removed, changed := diffTrees(beforeTree, afterTree)

	childEnv := parseKVRecords(t, childEnvRaw)
	if len(childEnv) == 0 {
		t.Fatal("probe 子进程没有写下环境变量：外部观察值不能退回测试进程里的推断")
	}
	if len(childEnv) != 1 {
		t.Fatalf("期望子进程只被启动一次（一份环境快照），实际 %d 份", len(childEnv))
	}
	env := childEnv[0]
	childHome, childHomeOK := lookupEnvMap(env, "HOME")
	childProfile, childProfileOK := lookupEnvMap(env, "USERPROFILE")
	childAppData, childAppDataOK := lookupEnvMap(env, "APPDATA")
	childAgentDir, childAgentDirOK := lookupEnvMap(env, "PI_CODING_AGENT_DIR")
	isoClean := filepath.Clean(agentDir)

	// 子进程视角的隔离必须成立：它的 HOME/USERPROFILE/APPDATA/agentDir 都指向隔离目录，
	// 而不是用户在测试里扮演的真实 home。
	for _, want := range []struct {
		name  string
		value string
		found bool
	}{
		{"HOME", childHome, childHomeOK},
		{"USERPROFILE", childProfile, childProfileOK},
		{"APPDATA", childAppData, childAppDataOK},
		{"PI_CODING_AGENT_DIR", childAgentDir, childAgentDirOK},
	} {
		if !want.found || want.value == "" {
			t.Fatalf("子进程环境里没有 %s：无法证明隔离生效", want.name)
		}
		if !strings.EqualFold(filepath.Clean(want.value), isoClean) {
			t.Fatalf("子进程的 %s 未被重定向到隔离目录：%q（期望 %q）", want.name, want.value, isoClean)
		}
		if strings.EqualFold(filepath.Clean(want.value), filepath.Clean(userHome)) {
			t.Fatalf("子进程的 %s 仍指向用户 HOME（%q）：probe 产生了用户配置副作用风险", want.name, want.value)
		}
	}

	writeArtifact(t, "child-env.json", map[string]any{
		"HOME":                childHome,
		"USERPROFILE":         childProfile,
		"APPDATA":             childAppData,
		"PI_CODING_AGENT_DIR": childAgentDir,
		"envCount":            len(env),
	})

	writePiObservation(t, map[string]any{
		"filesAdded":        added,
		"filesRemoved":      removed,
		"filesChanged":      changed,
		"userHomeEntries":   len(afterTree),
		"userHomeDigest":    treeDigest(t, afterTree),
		"childHome":         childHome,
		"childUserProfile":  childProfile,
		"childAppData":      childAppData,
		"childAgentDir":     childAgentDir,
		"childEnvCount":     len(env),
		"probeState":        string(res.State),
		"probeVersion":      res.Version,
		"probeChildStarted": true,
	}, nil)
}

// TestProbePs1NotDirectlyExecutable 验证：.ps1 在执行策略禁止（不带 -ExecutionPolicy Bypass）
// 时，Probe 返回“不可直接执行”的明确诊断，而不是假装可用。
//
// 前置条件由用例自建，不消费宿主机策略：Process 作用域经 PSExecutionPolicyPreference 环境变量
// 传给子进程 powershell，优先级高于 CurrentUser/LocalMachine（只被组策略覆盖）。
// 不这样做，本用例的成败就取决于跑测试的机器——本机策略 Restricted 时假绿，CI runner 策略放开时
// 变红，而产品行为没有变化（这正是 2026-11-03 CI 首轮全红的三个根因之一）。
func TestProbePs1NotDirectlyExecutable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 专项：PowerShell 执行策略只存在于 Windows")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "pi.ps1")
	// 一个正常内容；重点在于执行策略禁止时它无法运行。
	//
	// 脚本自己把**它的视角**（$PSCommandPath、$MyInvocation、$PSHOME、$args）写进
	// FAKEWORKER_RECORD_SELF：正向对照靠这个文件证明".ps1 是由 powershell -File 启动的"，
	// 而不是被当成可执行文件直接 CreateProcess。
	content := "param()\r\n" +
		"if ($env:FAKEWORKER_RECORD_SELF) {\r\n" +
		"  $lines = @(\r\n" +
		"    \"scriptPath=$PSCommandPath\",\r\n" +
		"    \"invokedAs=$($MyInvocation.InvocationName)\",\r\n" +
		"    \"hostName=$($Host.Name)\",\r\n" +
		"    \"args=$($args -join ' ')\",\r\n" +
		"    \"pshome=$PSHOME\",\r\n" +
		"    \"agentDir=$env:PI_CODING_AGENT_DIR\"\r\n" +
		"  )\r\n" +
		"  Add-Content -Path $env:FAKEWORKER_RECORD_SELF -Value $lines\r\n" +
		"}\r\n" +
		"if ($args -contains '--version') { Write-Output '1.5.0' }\r\n"
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("write ps1: %v", err)
	}
	inst := PiInstallation{ID: "fixture:" + p, Executable: p, Origin: OriginExternal}

	// PI02 的外部观察（正向对照）：执行策略允许时，BuildLaunch 给出的启动器
	// （powershell.exe -NoProfile -ExecutionPolicy Bypass -File <ps1>）能真的跑到脚本，
	// 且脚本自己确认了它被谁、以什么参数启动。
	obsDir := piObservationDir()
	var ps1SelfRaw string
	if obsDir != "" {
		if err := os.MkdirAll(obsDir, 0o755); err != nil {
			t.Fatalf("创建观察目录失败: %v", err)
		}
		ps1SelfRaw = filepath.Clean(filepath.Join(obsDir, "ps1-self.log"))
		_ = os.Remove(ps1SelfRaw)
		t.Setenv("FAKEWORKER_RECORD_SELF", ps1SelfRaw)

		exe, args, berr := BuildLaunch(inst, "--version")
		if berr != nil {
			t.Fatalf("BuildLaunch 失败: %v", berr)
		}
		bypass := exec.Command(exe, args...)
		bypass.Env = os.Environ()
		out, runErr := bypass.CombinedOutput()
		if runErr != nil {
			t.Fatalf("执行策略允许时 .ps1 应能经启动器运行，实际失败: %v（输出 %q）", runErr, string(out))
		}
		if !strings.Contains(string(out), "1.5.0") {
			t.Fatalf("启动器执行 .ps1 的输岀应含版本号，实际 %q", string(out))
		}
		self := parseKVRecords(t, ps1SelfRaw)
		if len(self) == 0 {
			t.Fatal(".ps1 没有写下自述：无法证明它被启动器执行")
		}
		if got := self[0]["scriptPath"]; !sameFile(got, p) {
			t.Fatalf("被执行的不是 fixture .ps1：子进程自述 %q（fixture=%q）", got, p)
		}
		writeArtifact(t, "ps1-self.json", self)
		writePiObservation(t, map[string]any{
			"ps1LauncherExe":  exe,
			"ps1LauncherArgs": args,
			"ps1SelfReport":   self,
			"ps1Ran":          true,
		}, nil)
	}

	// 自建受限前提（Process 作用域）。
	t.Setenv("PSExecutionPolicyPreference", "Restricted")

	// 前置自检：确认受限确实生效。组策略（MachinePolicy/UserPolicy）能盖过 Process 作用域，
	// 那种环境下测不到目标分支，必须显式报错，而不是"恰好通过"。
	if out, ok := ps1RunsWithoutBypass(t, p); ok {
		t.Fatalf("无法构造执行策略受限场景：.ps1 在 PSExecutionPolicyPreference=Restricted 下仍执行成功（stdout=%q）；"+
			"该断言在当前环境不可信，请确认无组策略覆盖", out)
	}

	oldBypass := PSBypassPolicy
	PSBypassPolicy = false // 模拟执行策略禁止（不带 Bypass）
	defer func() { PSBypassPolicy = oldBypass }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := Probe(ctx, inst)
	// 这里期望“无法执行”，但不应 panic；状态应为 ProbeFailed 且诊断说明不可直接执行。
	if err == nil && res.State == StateReady {
		t.Fatalf("执行策略禁止时 .ps1 不应标 Ready；state=%s", res.State)
	}
	if res.State != StateProbeFailed {
		t.Fatalf("执行策略禁止应判 ProbeFailed，实际 state=%s err=%v", res.State, err)
	}
	joined := strings.Join(res.Diagnostics, " ") + " " + errString(err)
	if !strings.Contains(joined, "不可直接执行") && !strings.Contains(joined, "执行策略") {
		t.Fatalf("诊断未说明不可直接执行/执行策略禁止；diag=%v err=%v", res.Diagnostics, err)
	}

	if obsDir == "" {
		return // 本地 focused 运行：不落盘
	}

	// 反向观察：把同一条启动器命令行交给 OS shell 执行，把**子进程的 stderr** 重定向到磁盘。
	// 执行策略禁止时 .ps1 的脚本体根本不会运行（所以它写不出自述），
	// 能拿到的事实是启动器自己的诊断与退出码 —— 这正是"不可直接执行"的证据。
	exe, args, berr := BuildLaunch(inst, "--version")
	if berr != nil {
		t.Fatalf("BuildLaunch 失败: %v", berr)
	}
	stderrPath := filepath.Clean(filepath.Join(obsDir, "ps1-restricted-stderr.txt"))
	_ = os.Remove(stderrPath)
	beforeSum := ""
	if raw, rerr := os.ReadFile(ps1SelfRaw); rerr == nil {
		beforeSum = sha256Of(raw)
	}
	line := shellQuote(exe)
	for _, a := range args {
		line += " " + shellQuote(a)
	}
	line += ` 2> "` + stderrPath + `"`
	code := runViaCmdScript(t, t.TempDir(), "ps1-restricted", line)

	rawStderr, rerr := os.ReadFile(stderrPath)
	if rerr != nil {
		t.Fatalf("读回 .ps1 受限执行的 stderr 失败（OS 重定向未生效）: %v", rerr)
	}
	if code == 0 {
		t.Fatalf(".ps1 在执行策略禁止下不应返回 0，实际 %d（stderr: %s）", code, clip(string(rawStderr), 300))
	}
	if len(strings.TrimSpace(string(rawStderr))) == 0 {
		t.Fatal("受限执行的 stderr 为空：无法证明启动器真的尝试了 .ps1")
	}
	// 错误信息里必然出现脚本路径（ASCII），不依赖系统语言的措辞。
	// powershell 报长名、t.TempDir 给 8.3 短名，两者尾部相同（<测试目录>\pi.ps1），
	// 所以比对尾部而不是整串。
	tail := filepath.Base(filepath.Dir(p)) + `\` + filepath.Base(p)
	namesScript := strings.Contains(string(rawStderr), tail)
	if !namesScript {
		t.Fatalf("受限执行的 stderr 没有提到 .ps1 路径 %q：stderr=%s", p, clip(string(rawStderr), 400))
	}
	afterSum := ""
	if raw, rerr := os.ReadFile(ps1SelfRaw); rerr == nil {
		afterSum = sha256Of(raw)
	}
	if afterSum != beforeSum {
		t.Fatal("执行策略禁止时 .ps1 脚本体竟然运行了（自述文件被改写）")
	}

	writePiObservation(t, map[string]any{
		"restrictedExitCode":          code,
		"restrictedStderrNamesScript": namesScript,
		"restrictedStderrBytes":       len(rawStderr),
		"restrictedScriptBodyRan":     false,
		"ps1DirectlyExecutable":       false,
	}, nil)
}

// shellQuote 在需要时给参数加双引号（路径可能含空格）。
func shellQuote(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " 	") {
		return `"` + s + `"`
	}
	return s
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ps1RunsWithoutBypass 直接以 powershell -File 跑一次脚本（不带 -ExecutionPolicy Bypass），
// 用于自证“当前环境确实禁止 .ps1”这一前置条件，而不是假定它成立。
func ps1RunsWithoutBypass(t *testing.T, script string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-File", script, "--version").CombinedOutput()
	return string(out), err == nil
}
