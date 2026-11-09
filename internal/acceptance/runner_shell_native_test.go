package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLayoutProbes(t *testing.T) {
	log := strings.Join([]string{
		"2026/11/09 08:00:00 starting: dist=...",
		"2026/11/09 08:00:01 window-options: requested=390x844 min=360x560",
		"2026/11/09 08:00:02 layout-probe: viewport=374x805 scrollWidth=374 clientWidth=374 overflow=false",
		"2026/11/09 08:00:03 layout-probe: viewport=1424x861 scrollWidth=1424 clientWidth=1424 overflow=false",
		"2026/11/09 08:00:04 layout-probe: viewport=374x805 scrollWidth=400 clientWidth=374 overflow=true",
		"trailing line",
	}, "\n")
	probes := parseLayoutProbes(log)
	if len(probes) != 3 {
		t.Fatalf("probes = %d, want 3: %#v", len(probes), probes)
	}
	if probes[0].Viewport != "374x805" || probes[0].Overflow {
		t.Fatalf("probe[0] = %#v", probes[0])
	}
	if !probes[2].Overflow || probes[2].ScrollWidth != 400 || probes[2].ClientWidth != 374 {
		t.Fatalf("probe[2] = %#v", probes[2])
	}
	if clear := countClearProbes(probes); clear != 2 {
		t.Fatalf("clear probes = %d, want 2", clear)
	}
}

func TestParseFindRefAndCount(t *testing.T) {
	first := []byte(`{"ok":true,"data":{"match":{"ref_id":"@s1abc:e3","name":"Chat"}}}`)
	if ref, ok := parseFindRef(first); !ok || ref != "@s1abc:e3" {
		t.Fatalf("parseFindRef(first) = %q %v", ref, ok)
	}
	many := []byte(`{"ok":true,"data":{"matches":[{"ref_id":"@s1abc:e7"}],"total_matches":1}}`)
	if ref, ok := parseFindRef(many); !ok || ref != "@s1abc:e7" {
		t.Fatalf("parseFindRef(matches) = %q %v", ref, ok)
	}
	if count := parseFindCount(many); count != 1 {
		t.Fatalf("parseFindCount = %d, want 1", count)
	}
	if ref, ok := parseFindRef([]byte(`{"ok":true,"data":{"matches":[],"total_matches":0}}`)); ok || ref != "" {
		t.Fatalf("parseFindRef(empty) = %q %v", ref, ok)
	}
}

func TestParseFindNamesAndShellRoutes(t *testing.T) {
	body := []byte(`{"ok":true,"data":{"matches":[{"name":"工具与自动化"},{"name":"资源库"}],"total_matches":2}}`)
	names := parseFindNames(body)
	if len(names) != 2 || names[0] != "工具与自动化" || names[1] != "资源库" {
		t.Fatalf("parseFindNames = %#v", names)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	routes, err := readShellRoutes(repo)
	if err != nil {
		t.Skipf("routes.json not present: %v", err)
	}
	if len(routes) != 9 || routes[0].Path != "/" || routes[8].Path != "/settings" {
		t.Fatalf("unexpected routes: %#v", routes)
	}
	if routes[1].Group != "primary" || routes[3].Group != "tools" {
		t.Fatalf("unexpected route grouping: %#v", routes)
	}
}

func TestParseWindowOptionsLine(t *testing.T) {
	size, ok := parseWindowOptions("2026/11/09 08:00:01 window-options: requested=390x844 min=360x560")
	if !ok || size.Width != 390 || size.Height != 844 {
		t.Fatalf("parseWindowOptions = %+v %v", size, ok)
	}
	if _, ok := parseWindowOptions("no window options here"); ok {
		t.Fatal("parseWindowOptions accepted a line without options")
	}
}

func TestReadManifestViewports(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs", "ui", "Agent-客户端产品原型", "DESIGN-MANIFEST.json")); err != nil {
		t.Skip("prototype manifest is not present")
	}
	viewports, err := readManifestViewports(repo)
	if err != nil {
		t.Fatalf("readManifestViewports: %v", err)
	}
	if len(viewports) != 9 {
		t.Fatalf("viewports = %d, want 9", len(viewports))
	}
	if viewports[0].Width != 360 || viewports[0].Height != 800 || viewports[8].Width != 1920 {
		t.Fatalf("unexpected viewports: %#v", viewports)
	}
}

func TestResizeLandedAfterTimeout(t *testing.T) {
	topLevel := []byte(`{"ok":false,"command":"resize-window","error":{"code":"TIMEOUT","details":{"timeout_ms":5000}},"disposition":{"delivery":"delivered_unverified","retry":"unsafe"}}

[verify] step "resize-820x1180" exited 1, want 0
`)
	if !resizeLandedAfterTimeout(topLevel) {
		t.Fatal("top-level delivered_unverified TIMEOUT was not recognised")
	}
	inError := []byte(`{"ok":false,"error":{"code":"TIMEOUT","disposition":{"delivery":"delivered_unverified"}}}`)
	if !resizeLandedAfterTimeout(inError) {
		t.Fatal("error-level delivered_unverified TIMEOUT was not recognised")
	}
	notLanded := []byte(`{"ok":false,"error":{"code":"TIMEOUT"},"disposition":{"delivery":"not_delivered"}}`)
	if resizeLandedAfterTimeout(notLanded) {
		t.Fatal("not_delivered TIMEOUT must not be accepted")
	}
	other := []byte(`{"ok":false,"error":{"code":"ACTION_FAILED"},"disposition":{"delivery":"delivered_unverified"}}`)
	if resizeLandedAfterTimeout(other) {
		t.Fatal("non-TIMEOUT error must not be accepted")
	}
}
