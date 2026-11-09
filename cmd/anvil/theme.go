package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"

	preferencesEndpoint = "/__anvil/preferences"
)

type themePreference struct {
	Theme    string `json:"theme"`
	Language string `json:"language"`
}

const (
	languageZhCN = "zh-CN"
	languageEnUS = "en-US"
)

// themeState is the host-side source of truth for the persisted theme mode and
// the current resolved light/dark value. The frontend reads and writes it via
// /__anvil/preferences so the preference survives restarts and the Window
// background can be resolved before the page paints.
type themeState struct {
	mu         sync.RWMutex
	dataDir    string
	logger     *log.Logger
	mode       string
	language   string
	systemDark func() bool
	window     func(resolved string)
	lastLogged string
}

func themePreferencePath(dataDir string) string {
	return filepath.Join(dataDir, "preferences.json")
}

func newThemeState(dataDir string, logger *log.Logger) *themeState {
	if logger == nil {
		logger = log.Default()
	}
	state := &themeState{
		dataDir:    dataDir,
		logger:     logger,
		mode:       themeSystem,
		language:   languageZhCN,
		systemDark: readSystemDark,
	}
	body, err := os.ReadFile(themePreferencePath(dataDir))
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Printf("theme-options: read preferences: %v; using system", err)
		}
		return state
	}
	var pref themePreference
	if err := json.Unmarshal(body, &pref); err != nil {
		logger.Printf("theme-options: invalid preferences file: %v; using system", err)
		return state
	}
	normalized, ok := normalizeThemeMode(pref.Theme)
	if !ok {
		logger.Printf("theme-options: unknown theme %q; using system", pref.Theme)
	} else {
		state.mode = normalized
	}
	if language, ok := normalizeLanguage(pref.Language); ok {
		state.language = language
	} else if pref.Language != "" {
		logger.Printf("i18n-options: unknown language %q; using zh-CN", pref.Language)
	}
	return state
}

func normalizeLanguage(language string) (string, bool) {
	switch language {
	case languageZhCN:
		return languageZhCN, true
	case languageEnUS:
		return languageEnUS, true
	default:
		return languageZhCN, false
	}
}

func normalizeThemeMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case themeSystem:
		return themeSystem, true
	case themeLight:
		return themeLight, true
	case themeDark:
		return themeDark, true
	default:
		return themeSystem, false
	}
}

func (t *themeState) snapshot() (mode string, systemDark bool, resolved string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.mode, t.systemDarkValue(), resolveThemeMode(t.mode, t.systemDarkValue())
}

// systemDarkValue must be called with t.mu held (read or write).
func (t *themeState) systemDarkValue() bool {
	if t.systemDark == nil {
		return false
	}
	return t.systemDark()
}

func resolveThemeMode(mode string, systemDark bool) string {
	if mode == themeSystem {
		if systemDark {
			return themeDark
		}
		return themeLight
	}
	return mode
}

func (t *themeState) setMode(mode string) error {
	normalized, ok := normalizeThemeMode(mode)
	if !ok {
		return fmt.Errorf("unknown theme %q", mode)
	}
	t.mu.Lock()
	t.mode = normalized
	t.mu.Unlock()
	if err := t.writePreference(); err != nil {
		return err
	}
	t.syncBackground()
	return nil
}

func (t *themeState) setLanguage(language string) error {
	normalized, ok := normalizeLanguage(language)
	if !ok {
		return fmt.Errorf("unknown language %q", language)
	}
	t.mu.Lock()
	t.language = normalized
	t.mu.Unlock()
	if err := t.writePreference(); err != nil {
		return err
	}
	t.logger.Printf("i18n-options: language=%s", normalized)
	return nil
}

func (t *themeState) languageValue() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.language
}

func (t *themeState) writePreference() error {
	t.mu.RLock()
	pref := themePreference{Theme: t.mode, Language: t.language}
	t.mu.RUnlock()
	body, err := json.MarshalIndent(pref, "", "  ")
	if err != nil {
		return err
	}
	path := themePreferencePath(t.dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func (t *themeState) setWindow(callback func(resolved string)) {
	t.mu.Lock()
	t.window = callback
	t.mu.Unlock()
	t.syncBackground()
}

// syncBackground resolves the current theme and updates the native window
// background. It logs only when the value changes (the frontend polls GET).
func (t *themeState) syncBackground() {
	t.mu.Lock()
	defer t.mu.Unlock()
	systemDark := t.systemDarkValue()
	resolved := resolveThemeMode(t.mode, systemDark)
	signature := t.mode + "|" + resolved
	if signature != t.lastLogged {
		t.logger.Printf("theme-options: mode=%s systemDark=%t resolved=%s", t.mode, systemDark, resolved)
		t.lastLogged = signature
	}
	if t.window != nil {
		t.window(resolved)
	}
}

// injectedIndex returns index.html with the persisted theme applied before CSS
// and first paint. This removes the light flash when starting in dark mode.
func (t *themeState) injectedIndex(dist string) ([]byte, error) {
	body, err := os.ReadFile(filepath.Join(dist, "index.html"))
	if err != nil {
		return nil, err
	}
	mode, _, resolved := t.snapshot()
	language := t.languageValue()
	payload, err := json.Marshal(map[string]string{"mode": mode, "resolved": resolved, "language": language})
	if err != nil {
		return nil, err
	}
	script := fmt.Sprintf(
		"<script>window.__ANVIL_INITIAL_THEME__=%s;window.__ANVIL_INITIAL_LANGUAGE__=%q;document.documentElement.lang=%q;document.documentElement.dataset.theme=%q;document.documentElement.style.colorScheme=%q;</script>",
		payload, language, language, resolved, resolved,
	)
	html := strings.Replace(string(body), `<html lang="zh-CN">`, `<html lang="`+language+`">`, 1)
	if index := strings.Index(html, "</head>"); index >= 0 {
		return []byte(html[:index] + script + html[index:]), nil
	}
	return []byte(script + html), nil
}
