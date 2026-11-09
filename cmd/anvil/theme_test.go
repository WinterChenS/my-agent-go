package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestTheme(t *testing.T, systemDark bool) (*themeState, string) {
	t.Helper()
	dataDir := t.TempDir()
	theme := newThemeState(dataDir, log.New(io.Discard, "", 0))
	theme.systemDark = func() bool { return systemDark }
	return theme, dataDir
}

func TestThemeStateDefaultsPersistsAndFallsBack(t *testing.T) {
	theme, dataDir := newTestTheme(t, true)
	mode, systemDark, resolved := theme.snapshot()
	if mode != themeSystem || !systemDark || resolved != themeDark {
		t.Fatalf("default snapshot = %q/%v/%q", mode, systemDark, resolved)
	}

	if err := theme.setMode(themeLight); err != nil {
		t.Fatalf("setMode light: %v", err)
	}
	body, err := os.ReadFile(themePreferencePath(dataDir))
	if err != nil {
		t.Fatalf("read preferences: %v", err)
	}
	var pref themePreference
	if err := json.Unmarshal(body, &pref); err != nil || pref.Theme != themeLight {
		t.Fatalf("preferences = %q (%v)", body, err)
	}
	reloaded := newThemeState(dataDir, log.New(io.Discard, "", 0))
	reloaded.systemDark = func() bool { return true }
	if mode, _, _ := reloaded.snapshot(); mode != themeLight {
		t.Fatalf("reloaded mode = %q, want light", mode)
	}

	if err := os.WriteFile(themePreferencePath(dataDir), []byte(`{"theme":"purple"}`), 0o644); err != nil {
		t.Fatalf("write invalid preference: %v", err)
	}
	invalid := newThemeState(dataDir, log.New(io.Discard, "", 0))
	if mode, _, _ := invalid.snapshot(); mode != themeSystem {
		t.Fatalf("invalid preference mode = %q, want system", mode)
	}

	if err := theme.setMode("purple"); err == nil {
		t.Fatal("setMode accepted an unknown theme")
	}
}

func TestThemeStateUpdatesWindowBackground(t *testing.T) {
	theme, _ := newTestTheme(t, false)
	var backgrounds []string
	theme.setWindow(func(resolved string) { backgrounds = append(backgrounds, resolved) })
	if err := theme.setMode(themeDark); err != nil {
		t.Fatalf("setMode dark: %v", err)
	}
	if err := theme.setMode(themeLight); err != nil {
		t.Fatalf("setMode light: %v", err)
	}
	if err := theme.setMode(themeSystem); err != nil {
		t.Fatalf("setMode system: %v", err)
	}
	if len(backgrounds) == 0 || backgrounds[len(backgrounds)-1] != themeLight {
		t.Fatalf("background callbacks = %v, want trailing light", backgrounds)
	}
	if backgrounds[0] != themeLight {
		t.Fatalf("initial background = %q, want light", backgrounds[0])
	}
}

func TestThemeHandlerReadsWritesAndInjects(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html><html><head></head><body>shell</body></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	theme, dataDir := newTestTheme(t, false)
	var backgrounds []string
	theme.setWindow(func(resolved string) { backgrounds = append(backgrounds, resolved) })
	if err := theme.setMode(themeDark); err != nil {
		t.Fatalf("setMode dark: %v", err)
	}
	var logBuffer bytes.Buffer
	logger := log.New(&logBuffer, "", 0)
	handler := newAssetHandler(dist, logger, theme)

	// App route: injected initial theme is present before the page script runs.
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "shell") {
		t.Fatalf("GET /settings = %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `__ANVIL_INITIAL_THEME__`) || !strings.Contains(rec.Body.String(), `dataset.theme="dark"`) {
		t.Fatalf("index is missing the initial theme: %s", rec.Body.String())
	}

	// GET preferences.
	req = httptest.NewRequest(http.MethodGet, preferencesEndpoint, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("GET preferences = %d %q", rec.Code, rec.Body.String())
	}
	if response["theme"] != themeDark || response["resolved"] != themeDark {
		t.Fatalf("GET preferences = %v", response)
	}
	if len(backgrounds) == 0 || backgrounds[len(backgrounds)-1] != themeDark {
		t.Fatalf("background was not synced on GET: %v", backgrounds)
	}

	// PUT preferences persists light.
	req = httptest.NewRequest(http.MethodPut, preferencesEndpoint, strings.NewReader(`{"theme":"light"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT preferences = %d %q", rec.Code, rec.Body.String())
	}
	var stored themePreference
	body, err := os.ReadFile(themePreferencePath(dataDir))
	if err != nil || json.Unmarshal(body, &stored) != nil || stored.Theme != themeLight {
		t.Fatalf("stored preferences = %q (%v)", body, err)
	}
	if backgrounds[len(backgrounds)-1] != themeLight {
		t.Fatalf("background after PUT = %v", backgrounds)
	}

	// Invalid PUT is rejected without changing the file.
	req = httptest.NewRequest(http.MethodPut, preferencesEndpoint, strings.NewReader(`{"theme":"purple"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid PUT = %d, want 400", rec.Code)
	}
	if !strings.Contains(logBuffer.String(), "theme-options:") {
		t.Fatalf("theme changes were not logged: %q", logBuffer.String())
	}
}

func TestThemeLogEndpointDistinguishesThemeLines(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html><head></head></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	theme, _ := newTestTheme(t, false)
	var buffer bytes.Buffer
	handler := newAssetHandler(dist, log.New(&buffer, "", 0), theme)

	req := httptest.NewRequest(http.MethodPost, probeLogPath, strings.NewReader("theme: mode=dark resolved=dark bg=oklch(1 0 0)"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("theme log status = %d", rec.Code)
	}
	if !strings.Contains(buffer.String(), "theme: mode=dark") || strings.Contains(buffer.String(), "layout-probe: theme:") {
		t.Fatalf("theme log = %q", buffer.String())
	}

	req = httptest.NewRequest(http.MethodPost, probeLogPath, strings.NewReader("viewport=1x1 overflow=false"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(buffer.String(), "layout-probe: viewport=1x1") {
		t.Fatalf("layout probe log = %q", buffer.String())
	}
}
