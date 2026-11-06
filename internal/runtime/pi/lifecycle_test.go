package pi

// F08-A 的发现/选择测试。
//
// 纪律：
//   - 只用临时目录与 testdata，绝不读写用户真实 Pi 配置（~/.pi、.tools/pi）。
//   - 不依赖本机是否装了 Pi：版本探测通过注入的 VersionProbe 提供，
//     发现过程本身也不执行任何候选（不启动 agent loop）。
//   - 每个 case 的观察值由测试自己写进 $ANVIL_OBSERVATION_DIR，
//     契约与 internal/run/observation_test.go 一致（首次清空 → 之后合并/追加）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ── 观察值输出（契约同 internal/run/observation_test.go） ──────────────────

var piObservationStarted sync.Once

func writePiObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
	if dir == "" {
		return // 本地 focused 运行：不落盘，也不影响断言
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	piObservationStarted.Do(func() {
		// 同一 revision 重跑时清掉上一轮残留，避免"这次什么都没写"被旧文件掩盖。
		_ = os.Remove(filepath.Join(dir, "observation.json"))
		_ = os.Remove(filepath.Join(dir, "events.jsonl"))
	})
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}
	merged := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON: %v", err)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == t.Name() {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, t.Name())
	}
	merged["tests"] = tests
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}

// ── fixtures ────────────────────────────────────────────────────────────────

// nativePiName 返回本平台的 Pi 可执行文件名（不依赖 Node，发现过程无需运行时）。
func nativePiName() string {
	if runtime.GOOS == "windows" {
		return "pi.exe"
	}
	return "pi"
}

// writePiStub 在 dir 里写一个占位 Pi 文件，返回其绝对路径。
//
// 内容不是真实的 Pi：发现只做文件发现 + 版本探测（注入），
// 因此这里绝不能"看起来像能跑"，也就不能用它冒充真实 Pi 的兼容性。
func writePiStub(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录 %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("写 %s: %v", p, err)
	}
	return p
}

// fixedVersionProbe 返回一个总是报告同一版本（或失败）的探测函数。
func fixedVersionProbe(version, diag string) VersionProbe {
	return func(context.Context, PiInstallation) (string, []string) {
		if version == "" {
			return "", []string{diag}
		}
		return version, nil
	}
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// snapshotTree 记录目录树里每个文件的 hash（相对路径 → hash）。
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			out[relPath(t, root, path)+"/"] = ""
			return nil
		}
		out[relPath(t, root, path)] = fileHash(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s: %v", root, err)
	}
	return out
}

func relPath(t *testing.T, root, path string) string {
	t.Helper()
	r, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("相对路径 %s: %v", path, err)
	}
	return r
}

func candidateIDs(res DiscoveryResult) []string {
	out := make([]string, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		out = append(out, c.Installation.ID)
	}
	return out
}

func originsOf(res DiscoveryResult) []string {
	out := make([]string, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		out = append(out, c.Origin)
	}
	return out
}

func findCandidate(t *testing.T, res DiscoveryResult, launcher string) PiCandidate {
	t.Helper()
	for _, c := range res.Candidates {
		if strings.EqualFold(filepath.Clean(c.LauncherPath), filepath.Clean(launcher)) {
			return c
		}
	}
	t.Fatalf("候选里没有 %s，实际 %+v", launcher, res.Candidates)
	return PiCandidate{}
}

// ── PI_DISC01 ───────────────────────────────────────────────────────────────

// TestDiscoverReturnsAllCandidatesAndSelectsDeterministically 是 PI_DISC01 的核心：
// 五个来源全部列出（路径/origin 都可核对），顺序确定，选中用户指定的那一个。
func TestDiscoverReturnsAllCandidatesAndSelectsDeterministically(t *testing.T) {
	root := t.TempDir()

	pref := writePiStub(t, filepath.Join(root, "preferred"), nativePiName(), "preferred")
	saved := writePiStub(t, filepath.Join(root, "saved"), nativePiName(), "saved")
	managedRoot := filepath.Join(root, "managed")
	managed := writePiStub(t, filepath.Join(managedRoot, "1.5.0"), nativePiName(), "managed")
	pathDir := filepath.Join(root, "pathdir")
	pathPi := writePiStub(t, pathDir, nativePiName(), "path")
	commonDir := filepath.Join(root, "common")
	common := writePiStub(t, commonDir, nativePiName(), "common")

	pathBefore := os.Getenv("PATH")
	hashBefore := map[string]string{
		pref: fileHash(t, pref), saved: fileHash(t, saved), managed: fileHash(t, managed),
		pathPi: fileHash(t, pathPi), common: fileHash(t, common),
	}

	res, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:       pref,
		Saved:           saved,
		ManagedRoot:     managedRoot,
		PathEnv:         []string{pathDir},
		CommonLocations: []string{commonDir},
		VersionProbe:    fixedVersionProbe("1.5.0", ""),
	})
	if err != nil {
		t.Fatalf("Discover 不应报错: %v", err)
	}

	wantOrigins := []string{
		CandidateOriginPreferred, CandidateOriginSaved, CandidateOriginManaged,
		CandidateOriginPATH, CandidateOriginCommon,
	}
	if got := originsOf(res); !reflect.DeepEqual(got, wantOrigins) {
		t.Fatalf("候选顺序/origin 不对：\n got %v\nwant %v", got, wantOrigins)
	}
	wantLaunchers := []string{pref, saved, managed, pathPi, common}
	for i, c := range res.Candidates {
		if !strings.EqualFold(filepath.Clean(c.LauncherPath), filepath.Clean(wantLaunchers[i])) {
			t.Fatalf("候选 %d 的 LauncherPath = %s，期望 %s", i, c.LauncherPath, wantLaunchers[i])
		}
		if c.ExecutableHash == "" {
			t.Fatalf("候选 %s 缺可执行体 hash", c.LauncherPath)
		}
		if c.RuntimeExecutable == "" || c.RuntimeHash == "" {
			t.Fatalf("候选 %s 缺 runtime 记录: %+v", c.LauncherPath, c)
		}
		if c.Installation.Version != "1.5.0" {
			t.Fatalf("候选 %s 版本 = %q，期望 1.5.0", c.LauncherPath, c.Installation.Version)
		}
		if c.Compatibility != StateReadyCompatible {
			t.Fatalf("未认证哈希的 1.5.0 只能是 ready_compatible，实际 %q（%v）", c.Compatibility, c.Diagnostics)
		}
	}
	if res.Selected == nil {
		t.Fatal("应从候选里选出一个")
	}
	if res.Selected.Installation.ID != res.Candidates[0].Installation.ID {
		t.Fatalf("应选中用户指定（最高优先级）的候选，实际 %+v", res.Selected)
	}
	if res.State != StateReadyCompatible {
		t.Fatalf("结果状态 = %q，期望 ready_compatible", res.State)
	}
	// 不静默切换的实现前提：所有候选都在结果里，UI 才能展示"切到哪个"。
	if len(res.Candidates) != 5 {
		t.Fatalf("期望列出 5 个候选，实际 %d", len(res.Candidates))
	}

	if os.Getenv("PATH") != pathBefore {
		t.Fatal("Discover 不得修改进程 PATH")
	}
	for p, h := range hashBefore {
		if fileHash(t, p) != h {
			t.Fatalf("Discover 修改了候选文件 %s", p)
		}
	}

	raw, err := json.MarshalIndent(res.Candidates, "", "  ")
	if err != nil {
		t.Fatalf("序列化候选: %v", err)
	}
	writePiObservation(t,
		map[string]any{
			"candidates":     len(res.Candidates),
			"origins":        originsOf(res),
			"selectedOrigin": res.Selected.Origin,
			"pathModified":   os.Getenv("PATH") != pathBefore,
		},
		map[string][]byte{"candidates.json": raw},
	)
}

// TestDiscoverMapsVersionToCompatibilityState 把版本 → 产品状态固定下来。
// 契约探测尚未实现（F08-B），所以这里只允许 ready_compatible 或"已认证版本+已登记哈希"的 ready_verified。
func TestDiscoverMapsVersionToCompatibilityState(t *testing.T) {
	root := t.TempDir()

	// 一个都没有：missing。
	if err := os.MkdirAll(filepath.Join(root, "nothing"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	empty, err := Discover(context.Background(), DiscoveryRequest{
		CommonLocations: []string{filepath.Join(root, "nothing")},
		VersionProbe:    fixedVersionProbe("1.5.0", ""),
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if empty.State != StateMissing || empty.Selected != nil || len(empty.Candidates) != 0 {
		t.Fatalf("无候选时应为 missing，实际 state=%q selected=%+v candidates=%d",
			empty.State, empty.Selected, len(empty.Candidates))
	}

	launcher := writePiStub(t, filepath.Join(root, "pi"), nativePiName(), "stub")

	cases := []struct {
		name         string
		version      string
		diag         string
		wantCand     CompatibilityState
		wantResult   CompatibilityState
		wantSelected bool
	}{
		{"probe-failed", "", "模拟探测失败", StateIncompatible, StateIncompatible, false},
		{"below-minimum", "1.4.0", "", StateUpgradeAvailable, StateIncompatible, false},
		{"in-range-unregistered-hash", "1.5.0", "", StateReadyCompatible, StateReadyCompatible, true},
		{"major-2", "2.0.0", "", StateIncompatible, StateIncompatible, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Discover(context.Background(), DiscoveryRequest{
				Preferred:    launcher,
				VersionProbe: fixedVersionProbe(tc.version, tc.diag),
			})
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(res.Candidates) != 1 {
				t.Fatalf("期望 1 个候选，实际 %+v", res.Candidates)
			}
			c := res.Candidates[0]
			if c.Compatibility != tc.wantCand {
				t.Fatalf("版本 %q → %q，期望 %q（diagnostics=%v）", tc.version, c.Compatibility, tc.wantCand, c.Diagnostics)
			}
			if res.State != tc.wantResult {
				t.Fatalf("结果状态 %q，期望 %q", res.State, tc.wantResult)
			}
			if tc.wantSelected != (res.Selected != nil) {
				t.Fatalf("selected=%+v，期望是否选中=%v", res.Selected, tc.wantSelected)
			}
			if tc.version == "" && len(c.Diagnostics) == 0 {
				t.Fatal("探测失败必须带诊断")
			}
			if tc.version == "1.4.0" && c.Compatibility == StateReadyCompatible {
				t.Fatal("低于最低版本绝不能算兼容")
			}
		})
	}

	// ready_verified 必须**同时**满足三件事：版本在预认证清单、可执行体 SHA-256 在嵌入
	// 清单、并且当前可执行体的契约探测通过。缺一条就只能是 ready_compatible。
	res, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	hash := res.Candidates[0].ExecutableHash
	policy := DefaultPolicy
	policy.PreCertifiedHashes = map[string]map[string]bool{"1.5.0": {hash: true}}

	// 1) 已认证版本 + 已登记哈希 + 契约探测通过 → ready_verified。
	verified, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		Policy:       policy,
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := verified.Candidates[0].Compatibility; got != StateReadyVerified {
		t.Fatalf("版本已认证 + 哈希已登记 + 契约探测通过应为 ready_verified，实际 %q", got)
	}

	// 2) 版本与哈希都在清单里，但契约探测尚未接上（F08-B）→ 只能 ready_compatible，
	//    并要说清“契约探测没跑”，不能含混成“已验证”。
	noProbe, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		Policy:       policy,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := noProbe.Candidates[0].Compatibility; got != StateReadyCompatible {
		t.Fatalf("契约探测未执行时不能 ready_verified，实际 %q", got)
	}
	if diag := strings.Join(noProbe.Candidates[0].Diagnostics, "\n"); !strings.Contains(diag, "契约探测") {
		t.Fatalf("诊断要说明契约探测未执行，实际 %v", noProbe.Candidates[0].Diagnostics)
	}

	// 3) 认得版本但认不得这个二进制：即使契约探测通过，也必须停在 ready_compatible。
	//    未登记的哈希永远不能 ready_verified（防回归的关键一条）。
	policyOther := DefaultPolicy
	policyOther.PreCertifiedHashes = map[string]map[string]bool{"1.5.0": {"deadbeef": true}}
	unregistered, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		Policy:       policyOther,
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := unregistered.Candidates[0].Compatibility; got != StateReadyCompatible {
		t.Fatalf("未登记哈希即使契约探测通过也不许标 ready_verified，实际 %q（%v）",
			got, unregistered.Candidates[0].Diagnostics)
	}
	if diag := strings.Join(unregistered.Candidates[0].Diagnostics, "\n"); !strings.Contains(diag, "unregistered-executable-hash") {
		t.Fatalf("诊断要区分“哈希未登记”，实际 %v", unregistered.Candidates[0].Diagnostics)
	}
}

// TestDiscoverSkipsBrokenSavedPath：失效的保存路径要报出来，但不许被选中，也不许静默丢。
func TestDiscoverSkipsBrokenSavedPath(t *testing.T) {
	root := t.TempDir()
	pref := writePiStub(t, filepath.Join(root, "preferred"), nativePiName(), "preferred")
	gone := filepath.Join(root, "gone", nativePiName())

	res, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    pref,
		Saved:        gone,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
	})
	if err != nil {
		t.Fatalf("Discover 不应因失效保存路径报错: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("失效保存路径也要作为候选列出，实际 %+v", res.Candidates)
	}
	broken := findCandidate(t, res, gone)
	if broken.Compatibility != StateMissing {
		t.Fatalf("失效保存路径应为 missing，实际 %q", broken.Compatibility)
	}
	if len(broken.Diagnostics) == 0 {
		t.Fatal("失效保存路径必须带诊断")
	}
	if res.Selected == nil || res.Selected.Installation.ID != res.Candidates[0].Installation.ID {
		t.Fatalf("应选中有效的用户指定候选，实际 %+v", res.Selected)
	}
	if res.Selected.Compatibility == StateMissing {
		t.Fatal("失效候选不许被选中")
	}
}

// ── PI_DISC02 ───────────────────────────────────────────────────────────────

// TestDiscoverRejectsWechatNodeShim：安全目录里的 pi.cmd，解析到的 Node 落在微信开发者工具里。
// 必须按**实际解析路径**拒绝（只看 launcher 路径是不够的）。
func TestDiscoverRejectsWechatNodeShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		// npm .cmd shim + 微信开发者工具 Node 是 Windows 场景；Linux/macOS 没有这种 shim。
		// 目标平台 win-x64 上必跑，不是"跳过即通过"：CI 上仍以 win-x64 为准。
		t.Skip("Windows 专项：微信开发者工具 Node shim")
	}

	root := t.TempDir()
	safeDir := filepath.Join(root, "npm-bin")
	shim := writePiStub(t, safeDir, "pi.cmd", "@echo off\r\nexit /b 0\r\n")
	wechatDir := filepath.Join(root, "微信web开发者工具", "node")
	wechatNode := writePiStub(t, wechatDir, "node.exe", "not-a-real-node")
	otherDir := filepath.Join(root, "common")
	other := writePiStub(t, otherDir, nativePiName(), "other")

	res, err := Discover(context.Background(), DiscoveryRequest{
		PathEnv:         []string{safeDir, wechatDir},
		CommonLocations: []string{otherDir},
		VersionProbe:    fixedVersionProbe("1.5.0", ""),
	})
	if err != nil {
		t.Fatalf("Discover 不应报错: %v", err)
	}

	bad := findCandidate(t, res, shim)
	if bad.Compatibility != StateIncompatible {
		t.Fatalf("微信 Node shim 应判 incompatible，实际 %q", bad.Compatibility)
	}
	if !strings.EqualFold(filepath.Clean(bad.RuntimeExecutable), filepath.Clean(wechatNode)) {
		t.Fatalf("记录的实际运行时路径 = %q，期望 %q", bad.RuntimeExecutable, wechatNode)
	}
	joined := strings.Join(bad.Diagnostics, "\n")
	if !strings.Contains(joined, "unexpected-node-origin") {
		t.Fatalf("诊断要带 kind unexpected-node-origin，实际 %v", bad.Diagnostics)
	}
	if !strings.Contains(joined, wechatNode) {
		t.Fatalf("诊断要写明实际解析到的路径 %s，实际 %v", wechatNode, bad.Diagnostics)
	}
	if res.Selected == nil || res.Selected.Installation.ID == bad.Installation.ID {
		t.Fatalf("被拒候选不许被选中，实际 %+v", res.Selected)
	}
	if res.Selected.Origin != CandidateOriginCommon {
		t.Fatalf("应改选另一个来源的候选，实际 %+v", res.Selected)
	}
	if !strings.EqualFold(filepath.Clean(res.Selected.LauncherPath), filepath.Clean(other)) {
		t.Fatalf("应选中安全目录里的那个候选 %s，实际 %s", other, res.Selected.LauncherPath)
	}

	rejected, err := json.MarshalIndent(map[string]any{
		"launcherPath":      bad.LauncherPath,
		"runtimeExecutable": bad.RuntimeExecutable,
		"diagnostics":       bad.Diagnostics,
		"compatibility":     bad.Compatibility,
		"candidates":        res.Candidates,
	}, "", "  ")
	if err != nil {
		t.Fatalf("序列化被拒候选: %v", err)
	}
	writePiObservation(t, map[string]any{
		"rejectedLauncher": bad.LauncherPath,
		"rejectedReason":   "unexpected-node-origin",
	}, map[string][]byte{"rejected.json": rejected})
}

// TestDiscoverNeverModifiesExternalFilesOrPath：发现是只读操作。
func TestDiscoverNeverModifiesExternalFilesOrPath(t *testing.T) {
	root := t.TempDir()
	pref := writePiStub(t, filepath.Join(root, "preferred"), nativePiName(), "preferred")
	pathDir := filepath.Join(root, "pathdir")
	pathPi := writePiStub(t, pathDir, nativePiName(), "path")
	// 目录里的其它文件也不许被动：发现不创建目录、不写缓存。
	other := writePiStub(t, pathDir, "unrelated.txt", "keep me")

	beforeTree := snapshotTree(t, root)
	pathBefore := os.Getenv("PATH")
	envBefore := os.Environ()
	hashPref, hashPath, hashOther := fileHash(t, pref), fileHash(t, pathPi), fileHash(t, other)

	if _, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:       pref,
		PathEnv:         []string{pathDir},
		CommonLocations: []string{filepath.Join(root, "empty-common")},
		VersionProbe:    fixedVersionProbe("1.5.0", ""),
	}); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	afterTree := snapshotTree(t, root)
	if !reflect.DeepEqual(beforeTree, afterTree) {
		t.Fatalf("Discover 改动了文件树：\nbefore %v\nafter  %v", sortedKeys(beforeTree), sortedKeys(afterTree))
	}
	if os.Getenv("PATH") != pathBefore {
		t.Fatal("Discover 不得修改 PATH")
	}
	if !reflect.DeepEqual(envBefore, os.Environ()) {
		t.Fatal("Discover 不得修改进程环境")
	}
	if fileHash(t, pref) != hashPref || fileHash(t, pathPi) != hashPath || fileHash(t, other) != hashOther {
		t.Fatal("fixture 文件内容被改动")
	}
}

// TestSelectRejectsUnknownOrUnusableCandidate：Select 绝不静默回退。
func TestSelectRejectsUnknownOrUnusableCandidate(t *testing.T) {
	root := t.TempDir()
	launcher := writePiStub(t, filepath.Join(root, "pi"), nativePiName(), "stub")

	res, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, err := Select(context.Background(), res, "pi-does-not-exist"); err == nil {
		t.Fatal("未知 installationID 必须报错")
	}

	// 低于最低版本的候选是真实候选，但不可选。
	upgrade, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    launcher,
		VersionProbe: fixedVersionProbe("1.4.0", ""),
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, err := Select(context.Background(), upgrade, upgrade.Candidates[0].Installation.ID); err == nil {
		t.Fatal("upgrade_available 候选不可选，必须报错")
	}

	// 可用的候选显式选中：结果状态跟着走，且 Selected 就是它。
	sel, err := Select(context.Background(), res, res.Candidates[0].Installation.ID)
	if err != nil {
		t.Fatalf("选中可用候选不应报错: %v", err)
	}
	if sel.Selected == nil || sel.Selected.Installation.ID != res.Candidates[0].Installation.ID {
		t.Fatalf("Selected = %+v", sel.Selected)
	}
	if sel.State != StateReadyCompatible {
		t.Fatalf("选中后状态 = %q", sel.State)
	}
	if !reflect.DeepEqual(candidateIDs(sel), candidateIDs(res)) {
		t.Fatal("选择不得改变候选集合（不许静默丢弃其它版本）")
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
