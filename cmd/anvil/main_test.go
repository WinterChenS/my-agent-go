package main

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"my-agent-go/internal/preflight"
)

func writeIndex(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Anvil</title>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
}

func TestFrontendDistUsesEnvOverride(t *testing.T) {
	dir := t.TempDir()
	writeIndex(t, dir)
	t.Setenv("ANVIL_FRONTEND_DIST", dir)
	got, err := frontendDist()
	if err != nil {
		t.Fatalf("frontendDist: %v", err)
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(dir)) {
		t.Fatalf("frontendDist = %q, want %q", got, dir)
	}
}

func TestAppDataDirRequiresExplicitPath(t *testing.T) {
	t.Setenv("ANVIL_DATA_DIR", "")
	if _, err := appDataDir(); err == nil {
		t.Fatal("appDataDir accepted an empty ANVIL_DATA_DIR; it must refuse to fall back")
	}
}

func TestAppDataDirRejectsOtherDrive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the D: rule is Windows-specific")
	}
	target := `C:\Users\Administrator\AppData\Local\anvil-r0-00-must-not-exist`
	t.Setenv("ANVIL_DATA_DIR", target)
	_, err := appDataDir()
	if !errors.Is(err, preflight.ErrOutsideD) {
		t.Fatalf("appDataDir on C: returned %v, want ErrOutsideD", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("appDataDir created %q on C:", target)
	}
}

func TestAppDataDirCreatesAndProbesOnD(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the D: rule is Windows-specific")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if vol := strings.ToUpper(filepath.VolumeName(dir)); vol != "D:" {
		t.Fatalf("test temp dir %q is on %q, want D:", dir, vol)
	}
	t.Setenv("ANVIL_DATA_DIR", dir)
	got, err := appDataDir()
	if err != nil {
		t.Fatalf("appDataDir: %v", err)
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(dir)) {
		t.Fatalf("appDataDir = %q, want %q", got, dir)
	}
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		t.Fatalf("data directory was not created: info=%v err=%v", info, statErr)
	}
}

func TestParseWindowSizeDefaultsAndOverrides(t *testing.T) {
	defaults, warnings := parseWindowSize("", "")
	if len(warnings) != 0 {
		t.Fatalf("defaults produced warnings: %v", warnings)
	}
	if defaults.Width != 1180 || defaults.Height != 820 || defaults.MinWidth != 360 || defaults.MinHeight != 560 {
		t.Fatalf("defaults = %+v", defaults)
	}

	narrow, warnings := parseWindowSize("390", "844")
	if len(warnings) != 0 {
		t.Fatalf("valid override produced warnings: %v", warnings)
	}
	if narrow.Width != 390 || narrow.Height != 844 {
		t.Fatalf("narrow = %+v", narrow)
	}

	wide, warnings := parseWindowSize("1440", "900")
	if len(warnings) != 0 || wide.Width != 1440 || wide.Height != 900 {
		t.Fatalf("wide = %+v warnings=%v", wide, warnings)
	}

	fallback, warnings := parseWindowSize("100", "nope")
	if fallback.Width != 1180 || fallback.Height != 820 {
		t.Fatalf("invalid override did not fall back: %+v", fallback)
	}
	if len(warnings) != 2 {
		t.Fatalf("invalid override warnings = %v, want 2", warnings)
	}
}

func TestAssetHandlerServesIndexForAppRoutes(t *testing.T) {
	dist := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html><div id=\"root\">anvil-shell</div>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("console.log('anvil')"), 0o644); err != nil {
		t.Fatalf("write js: %v", err)
	}
	handler := newAssetHandler(dist, log.New(&bytes.Buffer{}, "", 0))

	for _, route := range []string{"/", "/chat", "/skills", "/unknown/route"} {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "anvil-shell") {
			t.Fatalf("GET %s = %d %q, want index.html", route, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "console.log") {
		t.Fatalf("GET /assets/app.js = %d %q", rec.Code, rec.Body.String())
	}
}

func TestLayoutProbeEndpointLogsRealMeasurements(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<div id=\"root\"></div>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	handler := newAssetHandler(dist, logger)

	req := httptest.NewRequest(http.MethodPost, probeLogPath, strings.NewReader("viewport=390x844 scrollWidth=390 clientWidth=390 overflow=false"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("probe status = %d, want 204", rec.Code)
	}
	if !strings.Contains(buf.String(), "layout-probe: viewport=390x844") {
		t.Fatalf("probe log = %q", buf.String())
	}
	if strings.Contains(buf.String(), "\nviewport=") {
		t.Fatalf("probe body injected a newline: %q", buf.String())
	}
}
