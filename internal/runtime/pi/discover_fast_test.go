package pi

// FIX1-B：快速发现的语义边界。
//
// 这里守的是三条**可被观测**的承诺：
//  1. 快速发现一次也不调用契约探测（用注入的 ProbeFunc 计数，而不是"看起来很快"）；
//  2. 它也不会被一个慢探针拖住（探针根本没被执行），版本/指纹/来源与完整发现一致；
//  3. 完整发现仍然会把契约探测跑起来，并且能把坏 Pi 判成不可用——
//     快速路径不是"把探测删掉"，而是"把探测放到该跑的地方"。

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoverFastDoesNotRunContractProbe(t *testing.T) {
	root := t.TempDir()
	pref := writePiStub(t, filepath.Join(root, "preferred"), nativePiName(), "preferred")

	var probeCalls int32
	res, err := DiscoverFast(context.Background(), DiscoveryRequest{
		Preferred:    pref,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		// 调用方显式注入的探针也不许被执行：快速路径的承诺不能被注入点破坏。
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			atomic.AddInt32(&probeCalls, 1)
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("DiscoverFast: %v", err)
	}
	if got := atomic.LoadInt32(&probeCalls); got != 0 {
		t.Fatalf("快速发现调用了 %d 次契约探测，期望 0 次", got)
	}
	if res.Verification != VerificationVersionOnly {
		t.Fatalf("快速发现的 Verification = %q，期望 %q", res.Verification, VerificationVersionOnly)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("期望 1 个候选，实际 %+v", res.Candidates)
	}
	cand := res.Candidates[0]
	if cand.ContractVerified {
		t.Fatal("快速发现不得把候选标成契约已验证")
	}
	// 未跑契约探测 → 最高只能 ready_compatible，且状态必须可选中（能启动）。
	if cand.Compatibility != StateReadyCompatible {
		t.Fatalf("快速发现的状态 = %q，期望 %q（诊断：%v）",
			cand.Compatibility, StateReadyCompatible, cand.Diagnostics)
	}
	if res.Selected == nil || res.Selected.Installation.ID != cand.Installation.ID {
		t.Fatalf("快速发现仍需给出选中项，实际 %+v", res.Selected)
	}
	joined := ""
	for _, d := range res.Diagnostics {
		joined += d + "\n"
	}
	if !strings.Contains(joined, "fast-discovery") {
		t.Fatalf("快速发现必须在结果里标明未跑契约探测，实际 %v", res.Diagnostics)
	}
}

func TestDiscoverFastIsNotSlowedByContractProbe(t *testing.T) {
	root := t.TempDir()
	pref := writePiStub(t, filepath.Join(root, "preferred"), nativePiName(), "preferred")

	const probeDelay = 2 * time.Second
	start := time.Now()
	res, err := DiscoverFast(context.Background(), DiscoveryRequest{
		Preferred:    pref,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			time.Sleep(probeDelay)
			return true, nil
		},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("DiscoverFast: %v", err)
	}
	// 判据用"远小于探针耗时"，不用绝对毫秒阈值：机器快慢不该让这条测试变红。
	if elapsed >= probeDelay {
		t.Fatalf("快速发现耗时 %s ≥ 探针耗时 %s：探针被真的执行了", elapsed, probeDelay)
	}
	if res.Selected == nil {
		t.Fatalf("快速发现没有选中可用候选: state=%s diags=%v", res.State, res.Diagnostics)
	}
}

func TestDiscoverFastKeepsSameCandidateShapeAsFullDiscovery(t *testing.T) {
	root := t.TempDir()
	pref := writePiStub(t, filepath.Join(root, "preferred"), nativePiName(), "preferred")
	other := writePiStub(t, filepath.Join(root, "other"), nativePiName(), "other")

	req := DiscoveryRequest{
		Preferred:       pref,
		PathEnv:         []string{filepath.Dir(other)},
		VersionProbe:    fixedVersionProbe("1.5.0", ""),
		CommonLocations: nil,
	}
	fast, err := DiscoverFast(context.Background(), req)
	if err != nil {
		t.Fatalf("DiscoverFast: %v", err)
	}

	fullReq := req
	fullReq.ProbeFunc = func(context.Context, PiCandidate) (bool, []string) { return true, nil }
	full, err := Discover(context.Background(), fullReq)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(fast.Candidates) != len(full.Candidates) {
		t.Fatalf("候选数量不一致：fast=%d full=%d", len(fast.Candidates), len(full.Candidates))
	}
	for i := range fast.Candidates {
		f, g := fast.Candidates[i], full.Candidates[i]
		if f.Installation.ID != g.Installation.ID ||
			f.Installation.Version != g.Installation.Version ||
			f.LauncherPath != g.LauncherPath ||
			f.ExecutableHash != g.ExecutableHash ||
			f.RuntimeHash != g.RuntimeHash ||
			f.Origin != g.Origin ||
			f.Distribution != g.Distribution {
			t.Fatalf("候选 %d 的形状与完整发现不一致：\n fast %+v\n full %+v", i, f, g)
		}
	}
	if full.Verification != VerificationContractProbed {
		t.Fatalf("完整发现的 Verification = %q，期望 %q", full.Verification, VerificationContractProbed)
	}
	if !full.Candidates[0].ContractVerified {
		t.Fatal("完整发现跑过契约探测后应标记 ContractVerified")
	}
}

func TestDiscoverStillRunsContractProbeAndRejectsBrokenPi(t *testing.T) {
	root := t.TempDir()
	pref := writePiStub(t, filepath.Join(root, "preferred"), nativePiName(), "preferred")

	var probeCalls int32
	res, err := Discover(context.Background(), DiscoveryRequest{
		Preferred:    pref,
		VersionProbe: fixedVersionProbe("1.5.0", ""),
		ProbeFunc: func(context.Context, PiCandidate) (bool, []string) {
			atomic.AddInt32(&probeCalls, 1)
			return false, []string{"kind:\"contract-probe-failed\" 注入的坏契约"}
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := atomic.LoadInt32(&probeCalls); got != 1 {
		t.Fatalf("完整发现应执行 1 次契约探测，实际 %d 次", got)
	}
	cand := res.Candidates[0]
	if cand.Compatibility != StateIncompatible {
		t.Fatalf("契约探测失败的候选必须是 incompatible，实际 %q", cand.Compatibility)
	}
	if cand.ContractVerified {
		t.Fatal("契约探测失败不得标记 ContractVerified")
	}
	if res.Selected != nil {
		t.Fatal("不可用候选不许被选中")
	}
	if res.Verification != VerificationVersionOnly {
		t.Fatalf("没有候选通过契约探测时 Verification 必须是 %q，实际 %q",
			VerificationVersionOnly, res.Verification)
	}
}
