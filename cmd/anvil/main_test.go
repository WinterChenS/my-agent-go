package main

import (
	"errors"
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
