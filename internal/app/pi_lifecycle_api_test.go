package app

// F08-A 绑定层测试：DiscoverPi / SelectPi。
//
// 这里的重点是"绑定层不是转发器"：
//   - 发现请求真的用到 settings.piPath（用户指定）与真实 PATH 的**副本**；
//   - 发现过程不改 PATH、不改设置文件；
//   - 选一个不存在的 id 必须报错，不许静默回退到第一个可用候选。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"my-agent-go/internal/runtime/pi"
)

func appTestPiName() string {
	if runtime.GOOS == "windows" {
		return "pi.exe"
	}
	return "pi"
}

func writeAppPiStub(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录 %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("stub"), 0o755); err != nil {
		t.Fatalf("写 %s: %v", p, err)
	}
	return p
}

func TestDiscoverPiListsCandidatesInPriorityOrder(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()

	pref := writeAppPiStub(t, filepath.Join(root, "preferred"), name)
	managedRoot := filepath.Join(root, "managed")
	managed := writeAppPiStub(t, filepath.Join(managedRoot, "1.5.0"), name)
	pathDir := filepath.Join(root, "pathdir")
	pathPi := writeAppPiStub(t, pathDir, name)
	commonDir := filepath.Join(root, "common")
	common := writeAppPiStub(t, commonDir, name)

	// 只覆盖"发现环境"（PATH/常见位置/探针），不覆盖绑定层要验证的字段来源。
	d.piRequestOverride = func(req pi.DiscoveryRequest) pi.DiscoveryRequest {
		req.Preferred = pref
		req.ManagedRoot = managedRoot
		req.PathEnv = []string{pathDir}
		req.CommonLocations = []string{commonDir}
		req.VersionProbe = func(context.Context, pi.PiInstallation) (string, []string) {
			return "1.5.0", nil
		}
		return req
	}

	pathBefore := os.Getenv("PATH")
	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if os.Getenv("PATH") != pathBefore {
		t.Fatal("DiscoverPi 不得修改进程 PATH")
	}
	if len(res.Candidates) != 4 {
		t.Fatalf("期望 4 个候选（preferred/managed/path/common），实际 %+v", res.Candidates)
	}
	wantOrigins := []string{pi.CandidateOriginPreferred, pi.CandidateOriginManaged, pi.CandidateOriginPATH, pi.CandidateOriginCommon}
	for i, want := range wantOrigins {
		if res.Candidates[i].Origin != want {
			t.Fatalf("候选 %d origin = %q，期望 %q", i, res.Candidates[i].Origin, want)
		}
	}
	wantPaths := []string{pref, managed, pathPi, common}
	for i, want := range wantPaths {
		if !strings.EqualFold(filepath.Clean(res.Candidates[i].LauncherPath), filepath.Clean(want)) {
			t.Fatalf("候选 %d 路径 = %q，期望 %q", i, res.Candidates[i].LauncherPath, want)
		}
	}
	if res.Selected == nil || res.Selected.Origin != pi.CandidateOriginPreferred {
		t.Fatalf("应选中用户指定候选，实际 %+v", res.Selected)
	}
}

func TestSelectPiRejectsBogusInstallationID(t *testing.T) {
	d, root := newTestDesktop(t)
	name := appTestPiName()
	pref := writeAppPiStub(t, filepath.Join(root, "preferred"), name)

	d.piRequestOverride = func(req pi.DiscoveryRequest) pi.DiscoveryRequest {
		req.Preferred = pref
		req.ManagedRoot = ""
		req.PathEnv = nil
		req.CommonLocations = nil
		req.VersionProbe = func(context.Context, pi.PiInstallation) (string, []string) {
			return "1.5.0", nil
		}
		return req
	}

	if _, err := d.SelectPi("bogus"); err == nil {
		t.Fatal("不在候选里的 id 必须报错，不许静默回退")
	}

	res, err := d.DiscoverPi()
	if err != nil {
		t.Fatalf("DiscoverPi: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("期望 1 个候选，实际 %+v", res.Candidates)
	}
	sel, err := d.SelectPi(res.Candidates[0].Installation.ID)
	if err != nil {
		t.Fatalf("选中已发现候选不应报错: %v", err)
	}
	if sel.Selected == nil || sel.Selected.Installation.ID != res.Candidates[0].Installation.ID {
		t.Fatalf("Selected = %+v", sel.Selected)
	}
}
