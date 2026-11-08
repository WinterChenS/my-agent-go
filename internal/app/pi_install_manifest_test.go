package app

// B2：设置页需要真实版本清单，而不是让用户手输版本号。
//
// 守三件事：
//  1. 绑定列出的条目与安装服务实际用的**同一份**清单（不是另读一次嵌入文件）；
//  2. usable 的判据与 loader（Entry）同源：sizeBytes 未测量的目标显式不可用并说明原因；
//  3. HostTarget 只有一个映射点（pi.HostTarget），且与 build/platforms.json 对得上。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"my-agent-go/internal/piinstall"
	"my-agent-go/internal/runtime/pi"
)

func TestPiInstallManifestUsesInstallServiceManifest(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 注入一份"清单里只有一个版本、两个目标"的受信清单：绑定读的必须是它。
	injected := piinstall.Manifest{
		Schema:      piinstall.ManifestSchema,
		GeneratedAt: "2026-11-06T00:00:00Z",
		Source:      "https://github.com/earendil-works/pi/releases/download/v1.5.0/SHA256SUMS",
		Entries: []piinstall.ManifestEntry{
			{
				Version:   "9.9.9",
				TargetID:  "win-x64",
				AssetName: "pi-windows-x64.zip",
				URL:       "https://github.com/earendil-works/pi/releases/download/v1.5.0/pi-windows-x64.zip",
				SizeBytes: 42,
				SHA256:    "12d58a70e80b16ba9f764950b7a42bdeb07959495c0db68396cf2988c0b71af3",
			},
			{
				Version:   "9.9.9",
				TargetID:  "mac-x64",
				AssetName: "pi-darwin-x64.tar.gz",
				URL:       "https://github.com/earendil-works/pi/releases/download/v1.5.0/pi-darwin-x64.tar.gz",
				SizeBytes: 0,
				SHA256:    "f41b26cd9a1754abe259e05a421fb33b00936779cb0783c893a6389d1bd2ee0d",
			},
		},
	}
	d.piInstallOverride = func(svc *piinstall.InstallService) { svc.Manifest = injected }

	res := d.PiInstallManifest()
	if res.Error != nil {
		t.Fatalf("PiInstallManifest: %+v", res.Error)
	}
	if res.Schema != piinstall.ManifestSchema {
		t.Fatalf("schema = %d，期望 %d", res.Schema, piinstall.ManifestSchema)
	}
	if res.GeneratedAt != injected.GeneratedAt {
		t.Fatalf("generatedAt = %q，期望 %q（清单是嵌入的静态事实）", res.GeneratedAt, injected.GeneratedAt)
	}
	if res.HostTarget != pi.HostTarget() {
		t.Fatalf("hostTarget = %q，期望 %q", res.HostTarget, pi.HostTarget())
	}
	if len(res.Entries) != 2 {
		t.Fatalf("条目数 = %d，期望 2: %+v", len(res.Entries), res.Entries)
	}
	byTarget := map[string]PiManifestEntry{}
	for _, e := range res.Entries {
		byTarget[e.TargetID] = e
	}
	if got := byTarget["win-x64"]; !got.Usable || got.SizeBytes != 42 || got.AssetName == "" || got.SHA256 == "" {
		t.Fatalf("win-x64 条目未被完整带出: %+v", got)
	}
	if got := byTarget["mac-x64"]; got.Usable {
		t.Fatalf("sizeBytes=0 的目标必须是不可用: %+v", got)
	} else if !strings.Contains(got.UnavailableReason, "sizeBytes") {
		t.Fatalf("不可用原因必须点名 sizeBytes: %q", got.UnavailableReason)
	}
}

func TestPiInstallManifestMarksUnmeasuredTargetsOfEmbeddedManifest(t *testing.T) {
	d, _ := newTestDesktop(t)

	res := d.PiInstallManifest()
	if res.Error != nil {
		t.Fatalf("PiInstallManifest: %+v", res.Error)
	}
	m, err := piinstall.LoadEmbeddedManifest()
	if err != nil {
		t.Fatalf("加载嵌入清单: %v", err)
	}
	if len(res.Entries) != len(m.Entries) {
		t.Fatalf("条目数 = %d，嵌入清单 = %d", len(res.Entries), len(m.Entries))
	}

	usable := []string{}
	blocked := []string{}
	for _, e := range res.Entries {
		// 每一条都必须原样报出版本/目标/大小/哈希：界面据此列出真实版本。
		if e.Version == "" || e.TargetID == "" || e.AssetName == "" || e.SHA256 == "" {
			t.Fatalf("条目缺字段: %+v", e)
		}
		if e.Usable {
			usable = append(usable, e.TargetID)
		} else {
			blocked = append(blocked, e.TargetID)
			if !strings.Contains(e.UnavailableReason, "sizeBytes") {
				t.Fatalf("%s 的不可用原因必须说明 sizeBytes 未测量: %q", e.TargetID, e.UnavailableReason)
			}
		}
	}
	// 顺序与六目标展示顺序一致，便于证据逐条比对。
	if got := strings.Join(usable, ","); got != "win-x64" {
		t.Fatalf("可用目标 = %s，期望 win-x64（本清单只有它测过大小）", got)
	}
	if got := strings.Join(blocked, ","); got != "win-arm64,mac-x64,mac-arm64,linux-x64,linux-arm64" {
		t.Fatalf("不可用目标 = %s，期望五个未测量目标", got)
	}
}

// TestHostTargetComesFromPlatformsManifest 守"映射只此一处"：
// 绑定返回的目标必须与 build/platforms.json 里同 GOOS/GOARCH 的目标一致。
func TestHostTargetComesFromPlatformsManifest(t *testing.T) {
	d, _ := newTestDesktop(t)

	raw, err := os.ReadFile(filepath.Join(repoRootForAppTest(t), "build", "platforms.json"))
	if err != nil {
		t.Fatalf("读 build/platforms.json: %v", err)
	}
	var doc struct {
		Host struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
		} `json:"host"`
		Targets []struct {
			TargetID string `json:"targetId"`
			GOOS     string `json:"goos"`
			GOARCH   string `json:"goarch"`
			Active   bool   `json:"active"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 build/platforms.json: %v", err)
	}

	want := ""
	activeForHost := ""
	hostMatchesManifest := doc.Host.OS == runtime.GOOS && doc.Host.Arch == runtime.GOARCH
	for _, target := range doc.Targets {
		if target.GOOS == runtime.GOOS && target.GOARCH == runtime.GOARCH {
			if want != "" {
				t.Fatalf("platforms.json 里 %s/%s 对应了多个目标", runtime.GOOS, runtime.GOARCH)
			}
			want = target.TargetID
			if target.Active {
				activeForHost = target.TargetID
			}
		}
	}
	if want == "" {
		t.Fatalf("宿主 %s/%s 不在 build/platforms.json 的六目标里", runtime.GOOS, runtime.GOARCH)
	}
	if got := d.HostTarget(); got != want {
		t.Fatalf("HostTarget() = %q，platforms.json 里同平台目标是 %q", got, want)
	}
	// 绑定不能再写一份映射：它必须与内层唯一映射完全一致。
	if got := d.HostTarget(); got != pi.HostTarget() {
		t.Fatalf("HostTarget() = %q 与 pi.HostTarget() = %q 分叉（映射必须只有一处）", got, pi.HostTarget())
	}
	if hostMatchesManifest && activeForHost != want {
		t.Fatalf("platforms.json 的 active 目标 = %q，与宿主目标 %q 不一致", activeForHost, want)
	}
}
