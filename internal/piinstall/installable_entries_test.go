package piinstall

// B2：给设置页的受信清单视图。
//
// 纪律（与 PI_INS01 同口径）：
//   - 只列 loader 接受的条目：条目本身不合法（哈希/URL/资产名/sizeBytes 负值）
//     就不出现，界面无从"选一个装不上"的版本；
//   - 条目合法但 sizeBytes 未测量（本清单里五个目标）必须**显式**标成不可用，
//     并说明原因，绝不编造大小、也不静默隐藏（隐藏会让人以为清单里没有这个目标）；
//   - usable 的判据只能来自 Entry()（安装时真正用的那道闸），不能在这里另写一份。

import (
	"strings"
	"testing"
)

func TestInstallableEntriesMatchLoaderVerdict(t *testing.T) {
	m, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatalf("加载嵌入清单失败: %v", err)
	}
	entries := m.InstallableEntries()
	if len(entries) != len(m.Entries) {
		t.Fatalf("可列条目 = %d，清单条目 = %d（合法的条目不能少给）", len(entries), len(m.Entries))
	}

	usable := []string{}
	blocked := []string{}
	for _, e := range entries {
		_, entryErr := m.Entry(e.Version, e.TargetID)
		if e.Usable != (entryErr == nil) {
			t.Fatalf("%s/%s usable=%v，但 Entry() 结论是 %v（判据必须同源）",
				e.Version, e.TargetID, e.Usable, entryErr)
		}
		if e.Usable {
			usable = append(usable, e.TargetID)
			if e.UnavailableReason != "" {
				t.Fatalf("可用条目 %s 不应带不可用原因: %q", e.TargetID, e.UnavailableReason)
			}
			if e.SizeBytes <= 0 || e.SHA256 == "" {
				t.Fatalf("可用条目 %s 缺精确大小/哈希: %+v", e.TargetID, e)
			}
			continue
		}
		blocked = append(blocked, e.TargetID)
		if !strings.Contains(e.UnavailableReason, "sizeBytes") {
			t.Fatalf("%s 不可用的原因必须点名 sizeBytes 未测量（实际 %q）", e.TargetID, e.UnavailableReason)
		}
		if e.SizeBytes != 0 {
			t.Fatalf("%s 的 sizeBytes 应原样报 0（未测量），实际 %d", e.TargetID, e.SizeBytes)
		}
	}

	// 本清单的真实事实：只有 win-x64 在离线条件下测过大小，其余五个显式 BLOCKED。
	if got := strings.Join(usable, ","); got != "win-x64" {
		t.Fatalf("可用目标 = %s，期望 win-x64", got)
	}
	if got := strings.Join(blocked, ","); got != "win-arm64,mac-x64,mac-arm64,linux-x64,linux-arm64" {
		t.Fatalf("不可用目标 = %s，期望按六目标展示顺序列出五个", got)
	}
}

func TestInstallableEntriesDropWhatLoaderRejects(t *testing.T) {
	good := ManifestEntry{
		Version:   "1.5.0",
		TargetID:  "win-x64",
		AssetName: "pi-windows-x64.zip",
		URL:       "https://github.com/earendil-works/pi/releases/download/v1.5.0/pi-windows-x64.zip",
		SizeBytes: realWinX64Size,
		SHA256:    realWinX64SHA256,
	}
	bad := good
	bad.TargetID = "linux-x64"
	bad.AssetName = "pi-linux-x64.tar.gz"
	bad.URL = "https://github.com/earendil-works/pi/releases/download/v1.5.0/pi-linux-x64.tar.gz"
	bad.SHA256 = strings.ToUpper(realWinX64SHA256) // 大写哈希：loader 拒绝
	bad.SizeBytes = -1                             // 负大小：loader 拒绝

	m := Manifest{
		Schema:      ManifestSchema,
		GeneratedAt: "2026-11-05T00:00:00Z",
		Source:      "https://github.com/earendil-works/pi/releases/download/v1.5.0/SHA256SUMS",
		Entries:     []ManifestEntry{good, bad},
	}
	entries := m.InstallableEntries()
	if len(entries) != 1 || entries[0].TargetID != "win-x64" || !entries[0].Usable {
		t.Fatalf("列出的条目 = %+v，期望只有合法且可安装的 win-x64", entries)
	}
}
