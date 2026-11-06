package piinstall

// PI_INS02：用**官方真实包**（.tools/pi/standalone/pi-windows-x64-1.5.0.zip，只读）
// 走完整安装链路：本地 TLS fixture 服务器供包 → 清单哈希校验 → 解压 → probe →
// 激活 → F08 Discover 能发现它。
//
// 真实 vs fixture 的边界（报告里同样写明）：
//   - 包字节、哈希、大小都是官方的，不是构造的；
//   - 传输是本地 TLS 服务器（不访问网络），但走的仍是生产下载器（HTTPS + 白名单 +
//     每一跳重定向复核 + 流式 sha256）；
//   - probe 是 F08 的真实可执行体探针（pi.Probe：隔离 env 下跑 --version，不依赖 Node）。
//     六项契约探测（pi.ContractProbe）无法用于真实 Pi：它启动 worker 时不带 --mode rpc
//     （F08 冻结，2026-11-05 实测六个检查全部 RPC_TIMEOUT），因此本 case 不假装跑过它。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"my-agent-go/internal/runtime/pi"
)

// realPackagePath 返回官方独立包路径（只读引用，绝不写入）。
func realPackagePath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRoot(t), ".tools", "pi", "standalone", "pi-windows-x64-1.5.0.zip")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("缺少官方独立包（%s）；修复：把官方 pi-windows-x64.zip 放到 .tools/pi/standalone/ 后重跑", p)
	}
	return p
}

func TestInstallRealStandalonePackage(t *testing.T) {
	if runtime.GOOS != "windows" {
		// 官方包只有 win-x64 一份：非 Windows 宿主无法用真实包取证。
		// integration runner 对这类宿主把 case 记为 BLOCKED（见 piInstallBlockedReasonFor），
		// 不会走到这里；本地手动跑包测试时这条是显式 BLOCKED，不是"通过"。
		t.Skipf("BLOCKED: PI_INS02 的观察装置是官方 win-x64 独立包，本宿主是 %s", runtime.GOOS)
	}

	manifest, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatalf("加载嵌入清单失败: %v", err)
	}
	entry, err := manifest.Entry("1.5.0", "win-x64")
	if err != nil {
		t.Fatalf("清单缺少 win-x64 条目: %v", err)
	}

	body, err := os.ReadFile(realPackagePath(t))
	if err != nil {
		t.Fatalf("读取官方包失败: %v", err)
	}
	if int64(len(body)) != entry.SizeBytes {
		t.Fatalf("官方包大小 %d 与清单 %d 不一致", len(body), entry.SizeBytes)
	}
	if got := sha256HexOf(body); got != entry.SHA256 {
		t.Fatalf("官方包 sha256 %s 与清单 %s 不一致", got, entry.SHA256)
	}

	h := newHarness(t)
	// 用官方包字节替换 fixture 载荷，URL 指向本地 TLS 服务器，其余（哈希/大小）保持清单原值。
	h.mu.Lock()
	h.bodies["/"+entry.AssetName] = body
	h.mu.Unlock()
	served := entry
	served.URL = h.srv.URL + "/" + entry.AssetName
	h.entries = append(h.entries, served)
	h.rebuild()

	// 真实可执行体探针：pi.Probe（隔离 env，不依赖 Node）。
	h.svc.Probe = executableProbe("1.5.0")
	h.versionP = nil // 让 F08 Discover 用真实 --version 探针

	pathBefore := os.Getenv("PATH")
	externalPath, externalHash := writeExternalPi(t)
	externalBefore, err := snapshotRoot(filepath.Dir(externalPath))
	if err != nil {
		t.Fatalf("快照外部 Pi 失败: %v", err)
	}
	realPiBefore := captureExternalSnapshot(t, externalPath)

	rec := &stageRecorder{}
	res, err := h.svc.Install(context.Background(), InstallRequest{Version: "1.5.0", TargetID: "win-x64"}, rec.progress())
	if err != nil {
		t.Fatalf("官方包安装失败: %v（阶段=%v）", err, rec.list())
	}
	if res.State != pi.StateReadyCompatible {
		t.Fatalf("state = %q，期望 ready_compatible（哈希不在预认证清单里，不能自称 verified）", res.State)
	}
	if res.Version != "1.5.0" || res.TargetID != "win-x64" {
		t.Fatalf("安装结果版本/目标不对: %+v", res)
	}
	// 托管安装落在托管根内，且可执行体是真实 pi.exe。
	if !strings.HasPrefix(res.Executable, h.Root) {
		t.Fatalf("安装不在托管根内: %s（root=%s）", res.Executable, h.Root)
	}
	st, err := os.Stat(res.Executable)
	if err != nil {
		t.Fatalf("stat 安装后的可执行体失败: %v", err)
	}
	if st.Size() < 50<<20 {
		t.Fatalf("安装后的 pi.exe 只有 %d 字节，不像官方单文件发行包", st.Size())
	}
	gotHash, err := hashFileContent(res.Executable)
	if err != nil {
		t.Fatalf("计算安装后可执行体哈希失败: %v", err)
	}
	if gotHash != res.ExecutableSHA256 {
		t.Fatalf("结果里的哈希 %s 与磁盘 %s 不一致", res.ExecutableSHA256, gotHash)
	}

	// F08 Discover：托管根里必须能发现这个版本，且选中它。
	// 用真实 --version 探针，而不是 F08 的默认六项契约探针：
	// ContractProbe 启动 worker 时不带 --mode rpc（F08 冻结），对真实 Pi 只会
	// RPC_TIMEOUT，拿它当门槛等于把"探测装置坏了"记成"Pi 不可用"。
	disc, err := pi.Discover(context.Background(), pi.DiscoveryRequest{
		ManagedRoot: filepath.Join(h.Root, versionsDirName),
		Preferred:   res.Executable,
		Policy:      pi.DefaultPolicy,
		ProbeFunc:   executableProbe(""),
	})
	if err != nil {
		t.Fatalf("Discover 失败: %v", err)
	}
	if disc.Selected == nil {
		t.Fatalf("Discover 没有选中任何候选: %+v", disc)
	}
	if disc.Selected.Installation.Version != "1.5.0" {
		t.Fatalf("Discover 版本 = %q，期望 1.5.0", disc.Selected.Installation.Version)
	}
	if disc.Selected.ExecutableHash != res.ExecutableSHA256 {
		t.Fatalf("Discover 可执行体哈希 = %s，期望 %s", disc.Selected.ExecutableHash, res.ExecutableSHA256)
	}
	if disc.Selected.Origin != pi.CandidateOriginPreferred && disc.Selected.Origin != pi.CandidateOriginManaged {
		t.Fatalf("Discover 来源 = %q", disc.Selected.Origin)
	}

	// 外部 Pi 与 PATH 都不许变。
	if sum, err := hashFileContent(externalPath); err != nil || sum != externalHash {
		t.Fatalf("外部 Pi 被改写: hash=%s err=%v", sum, err)
	}
	externalAfter, err := snapshotRoot(filepath.Dir(externalPath))
	if err != nil {
		t.Fatalf("快照外部 Pi 失败: %v", err)
	}
	if externalAfter.SnapshotHash != externalBefore.SnapshotHash {
		t.Fatal("外部 Pi 目录在安装期间发生变化")
	}
	if after := os.Getenv("PATH"); after != pathBefore {
		t.Fatal("安装改变了 PATH")
	}
	realPiAfter := captureExternalSnapshot(t, externalPath)
	if realPiAfter.RealPiHash != realPiBefore.RealPiHash {
		t.Fatalf("真实 ~/.pi 在安装期间变化: before=%s after=%s", realPiBefore.RealPiHash, realPiAfter.RealPiHash)
	}

	writeInstallObservation(t, h.Root, map[string]any{
		"case":             "PI_INS02",
		"state":            string(res.State),
		"version":          res.Version,
		"targetId":         res.TargetID,
		"executable":       res.Executable,
		"executableSha256": res.ExecutableSHA256,
		"executableBytes":  st.Size(),
		"packageSha256":    entry.SHA256,
		"packageBytes":     entry.SizeBytes,
		"probe":            "pi.Probe（真实 --version，隔离 env，无 Node）",
		"discoveredOrigin": disc.Selected.Origin,
		"discoveredState":  string(disc.Selected.Compatibility),
		"pathUnchanged":    os.Getenv("PATH") == pathBefore,
		"externalHash":     externalHash,
		"realPiHomeHash":   realPiAfter.RealPiHash,
		"stages":           summarizeStages(rec),
	}, rec, captureExternalSnapshot(t, externalPath))
}
