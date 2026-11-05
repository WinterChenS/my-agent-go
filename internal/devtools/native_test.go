package devtools

// native runner 的守门测试。
//
// 这些用例守的不是"命令跑通了"，而是 runner 自己的判定：未知 target、错宿主、
// 非法 display、缺 runner 都必须非零；Linux 的 X11/Wayland 不能被另一个顶替。

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativeRunnerRejectsUnknownTarget(t *testing.T) {
	if code := RunNative("mars", "desktop"); code != 2 {
		t.Fatalf("未知 target 应返回 2，实际 %d", code)
	}
}

func TestNativeRunnerRejectsMissingTarget(t *testing.T) {
	if code := RunNative("", "desktop"); code != 2 {
		t.Fatalf("缺 target 应返回 2，实际 %d", code)
	}
}

func TestNativeRunnerRejectsWrongHost(t *testing.T) {
	// 挑一个与当前宿主不同的目标：原生用例必须在目标本机跑，交叉执行无效。
	target := "mac-x64"
	if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" {
		target = "linux-x64"
	}
	if code := RunNative(target, ""); code != 2 {
		t.Fatalf("宿主与目标不一致时应返回 2，实际 %d", code)
	}
}

func TestNativeRunnerRejectsInvalidDisplay(t *testing.T) {
	host := runtime.GOOS + "/" + runtime.GOARCH
	target := ""
	switch host {
	case "windows/amd64":
		target = "win-x64"
	case "windows/arm64":
		target = "win-arm64"
	case "darwin/amd64":
		target = "mac-x64"
	case "darwin/arm64":
		target = "mac-arm64"
	case "linux/amd64":
		target = "linux-x64"
	case "linux/arm64":
		target = "linux-arm64"
	default:
		t.Skipf("未知宿主 %s", host)
	}
	bad := "wayland"
	if target == "linux-x64" || target == "linux-arm64" {
		bad = "desktop" // Linux 不接受 desktop；必须是 x11/wayland
	}
	if code := RunNative(target, bad); code != 2 {
		t.Fatalf("非法 display %q 在 %s 上应返回 2，实际 %d", bad, target, code)
	}
}

func TestLookupNativeCaseDisplayCannotBeSubstituted(t *testing.T) {
	// Linux 的 X11 证据不能顶替 Wayland：Display 必须精确匹配。
	table := []nativeCase{{
		ID:      "BR03",
		Target:  "linux-x64",
		Display: DisplayX11,
		Steps:   []nativeStep{{Package: "tests/native", Run: "TestX"}},
	}}
	if _, ok := lookupNativeCase(table, Requirement{CaseID: "BR03", Target: "linux-x64", Display: DisplayX11}, "linux-x64", DisplayX11); !ok {
		t.Fatal("X11 用例应能匹配 X11 运行")
	}
	if _, ok := lookupNativeCase(table, Requirement{CaseID: "BR03", Target: "linux-x64", Display: DisplayWayland}, "linux-x64", DisplayWayland); ok {
		t.Fatal("X11 用例不得匹配 Wayland 运行")
	}
	// HOST case（Target 为空）跟随本次运行的 display，但条目本身没写 display 时适用。
	table = append(table, nativeCase{ID: "BR00", Steps: []nativeStep{{Package: "tests/fixtures/browser", Run: "TestY"}}})
	if _, ok := lookupNativeCase(table, Requirement{CaseID: "BR00"}, "linux-arm64", DisplayWayland); !ok {
		t.Fatal("HOST case 应在任意目标/显示上匹配")
	}
}

// TestNativeCaseIDsExistInIndex 防止 runner 表里写出索引里没有的 case
// （拼错 ID 会让必需位永远找不到 runner，或让一条不存在的 case 混进证据树）。
func TestNativeCaseIDsExistInIndex(t *testing.T) {
	idx := loadRealIndex(t)
	known := map[string]bool{}
	for _, c := range idx.Cases {
		known[c.ID] = true
	}
	for _, nc := range nativeCases() {
		if !known[nc.ID] {
			t.Errorf("native runner 里的 case %s 不在机器索引中", nc.ID)
		}
		if len(nc.Steps) == 0 && nc.Blocked == "" {
			t.Errorf("case %s 既没有执行步骤也没有说明阻塞原因", nc.ID)
		}
	}
}

// TestEvidenceRootLayout 钉住证据目录布局（v1-gates.md §4）：
// 少了 gate 或 display 任何一层，同名 case 的两份证据就会互相覆盖，
// 而门禁只会看到最后写的那一份。
func TestEvidenceRootLayout(t *testing.T) {
	cases := []struct {
		rev, gate, target, display string
		want                       string
	}{
		{"rev1", "G0", "win-x64", "", filepath.Join("docs", "evidence", "runs", "rev1", "G0", "win-x64")},
		{"rev1", "G1", "linux-x64", "wayland", filepath.Join("docs", "evidence", "runs", "rev1", "G1", "linux-x64", "wayland")},
		{"rev1", "G2", "win-x64", "desktop", filepath.Join("docs", "evidence", "runs", "rev1", "G2", "win-x64", "desktop")},
	}
	for _, c := range cases {
		if got := evidenceRoot(c.rev, c.gate, c.target, c.display); got != c.want {
			t.Errorf("evidenceRoot(%q,%q,%q,%q)=%q，期望 %q", c.rev, c.gate, c.target, c.display, got, c.want)
		}
	}
}

// TestIntegrationSelectsCasesByGate 钉住 --gate 真的改变 case 列表。
//
// 真事故（2026-11-05）：--gate 只改了证据目录，case 列表仍写死 "G0"，
// 于是 `--gate=G2` 跑的是 G0 的 24 条用例，还把证据写进 G2 目录。
// 命令"能跑"不代表"跑的是对的东西"。
func TestIntegrationSelectsCasesByGate(t *testing.T) {
	idx := loadRealIndex(t)
	g0, err := selectedIntegrationCases(idx, "G0")
	if err != nil {
		t.Fatalf("G0: %v", err)
	}
	g2, err := selectedIntegrationCases(idx, "G2")
	if err != nil {
		t.Fatalf("G2: %v", err)
	}
	if len(g0) == 0 || len(g2) == 0 {
		t.Fatal("G0/G2 都不应为空")
	}
	if contains(g0, "CFG01") || contains(g0, "MEM01") {
		t.Fatalf("G0 不应包含 G2 专属 case，实际 %v", g0)
	}
	for _, want := range []string{"CFG01", "DB01", "MEM01", "MCP01", "RES01"} {
		if !contains(g2, want) {
			t.Errorf("G2 必需列表缺 %s（选择了 %d 条）", want, len(g2))
		}
	}
	if contains(g2, "ENV01") || contains(g2, "GOV01") {
		t.Error("G2 不应包含 G0 专属的 ENV/GOV case")
	}
	if _, err := selectedIntegrationCases(idx, "G9"); err == nil {
		t.Error("未知 gate 应返回错误")
	}
}
