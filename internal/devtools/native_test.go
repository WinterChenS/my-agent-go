package devtools

// native runner 的守门测试。
//
// 这些用例守的不是"命令跑通了"，而是 runner 自己的判定：未知 target、错宿主、
// 非法 display、缺 runner 都必须非零；Linux 的 X11/Wayland 不能被另一个顶替。

import (
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
