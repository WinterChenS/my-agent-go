// Command anvil is the Anvil desktop host (Wails v3).
//
// R0-00 only delivers a launchable shell: the window loads the React bundle
// built by Vite and nothing else. Pages, theme and i18n are implemented by the
// following tasks. All writable locations must be explicit D: paths; the
// process refuses to fall back to a default location on another drive.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"my-agent-go/internal/preflight"
)

const appName = "Anvil"

const probeLogPath = "/__anvil/log"

// windowSize is the effective window geometry. Defaults are unchanged from
// R0-00; ANVIL_WINDOW_WIDTH/HEIGHT exist for the native acceptance case and
// development only.
type windowSize struct {
	Width     int
	Height    int
	MinWidth  int
	MinHeight int
}

var defaultWindowSize = windowSize{Width: 1180, Height: 820, MinWidth: 360, MinHeight: 560}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%s: %v", appName, err)
	}
}

func run() error {
	dist, err := frontendDist()
	if err != nil {
		return err
	}
	dataDir, err := appDataDir()
	if err != nil {
		return err
	}
	webviewDir := strings.TrimSpace(os.Getenv("ANVIL_WEBVIEW_DATA_DIR"))
	if webviewDir == "" {
		webviewDir = filepath.Join(dataDir, "webview2")
	}
	if _, err := preflight.EnsureDir(webviewDir, preflight.OSSystem{}); err != nil {
		return fmt.Errorf("WebView2 data directory: %w", err)
	}
	logDir := filepath.Join(dataDir, "logs")
	if _, err := preflight.EnsureDir(logDir, preflight.OSSystem{}); err != nil {
		return fmt.Errorf("log directory: %w", err)
	}
	if logFile, openErr := os.OpenFile(filepath.Join(logDir, "anvil.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); openErr == nil {
		defer logFile.Close()
		log.SetOutput(logFile)
	}
	log.SetFlags(log.LstdFlags | log.LUTC)
	theme := newThemeState(dataDir, log.Default())
	mode, _, resolved := theme.snapshot()
	log.Printf("theme-options: mode=%s resolved=%s", mode, resolved)
	log.Printf("i18n-options: language=%s", theme.languageValue())
	size, warnings := parseWindowSize(os.Getenv("ANVIL_WINDOW_WIDTH"), os.Getenv("ANVIL_WINDOW_HEIGHT"))
	for _, warning := range warnings {
		log.Printf("window-options: %s", warning)
	}
	log.Printf("starting: dist=%s data=%s webview=%s", dist, dataDir, webviewDir)
	log.Printf("window-options: requested=%dx%d min=%dx%d", size.Width, size.Height, size.MinWidth, size.MinHeight)
	initialURL := initialURLFromEnv(os.Getenv("ANVIL_INITIAL_URL"))
	log.Printf("window-options: initialURL=%s", initialURL)

	app := application.New(application.Options{
		Name:        appName,
		Description: appName,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: webviewDir,
		},
		Assets: application.AssetOptions{
			Handler:        newAssetHandler(dist, log.Default(), theme),
			DisableLogging: true,
		},
	})
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            appName,
		Width:            size.Width,
		Height:           size.Height,
		MinWidth:         size.MinWidth,
		MinHeight:        size.MinHeight,
		URL:              initialURL,
		BackgroundColour: windowBackground(resolved),
	})
	theme.setWindow(func(current string) {
		win.SetBackgroundColour(windowBackground(current))
	})
	log.Printf("window created; running")
	if err := app.Run(); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

// parseWindowSize reads the development window override. Invalid or
// out-of-range values log a warning and keep the default.
func parseWindowSize(widthRaw, heightRaw string) (windowSize, []string) {
	size := defaultWindowSize
	var warnings []string
	parse := func(raw string, current *int, min, max int, name string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < min || value > max {
			warnings = append(warnings, fmt.Sprintf("%s=%q is invalid or outside %d..%d; using %d", name, raw, min, max, *current))
			return
		}
		*current = value
	}
	parse(widthRaw, &size.Width, 360, 3840, "ANVIL_WINDOW_WIDTH")
	parse(heightRaw, &size.Height, 560, 2160, "ANVIL_WINDOW_HEIGHT")
	return size, warnings
}

// initialURLFromEnv allows acceptance runs to open an isolated sample route.
// Only same-origin absolute paths are accepted; malformed values fall back to
// "/" and are logged.
func initialURLFromEnv(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "/"
	}
	invalid := strings.Contains(value, "://") || strings.HasPrefix(value, "//")
	for _, r := range value {
		if r == 0x20 || r == 0x09 || r == 0x0A || r == 0x0D {
			invalid = true
		}
	}
	if !strings.HasPrefix(value, "/") || invalid {
		log.Printf("window-options: invalid ANVIL_INITIAL_URL %q; using /", raw)
		return "/"
	}
	return value
}

// isAppRoute treats extension-less paths as React Router routes. Assets and
// the Wails runtime keep their normal asset-server handling.
func isAppRoute(urlPath string) bool {
	if urlPath == "" || urlPath == "/" {
		return true
	}
	if strings.HasPrefix(urlPath, "/wails/") || strings.HasPrefix(urlPath, "/assets/") {
		return false
	}
	return path.Ext(urlPath) == ""
}

// newAssetHandler serves the built bundle, SPA route fallbacks for pushState
// navigation/reload, and the layout probe log endpoint.
func newAssetHandler(dist string, logger *log.Logger, theme *themeState) http.Handler {
	files := application.AssetFileServerFS(os.DirFS(dist))
	if logger == nil {
		logger = log.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == preferencesEndpoint {
			handlePreferences(w, r, theme, logger)
			return
		}
		if r.URL.Path == probeLogPath {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 2048))
			if err != nil {
				http.Error(w, "read probe", http.StatusBadRequest)
				return
			}
			message := sanitizeProbe(string(body))
			if strings.HasPrefix(message, "theme:") || strings.HasPrefix(message, "i18n:") || strings.HasPrefix(message, "i18n-title:") {
				logger.Printf("%s", message)
			} else {
				logger.Printf("layout-probe: %s", message)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if isAppRoute(r.URL.Path) {
			if theme == nil {
				http.Error(w, "theme state is not configured", http.StatusInternalServerError)
				return
			}
			body, err := theme.injectedIndex(dist)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(body)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func handlePreferences(w http.ResponseWriter, r *http.Request, theme *themeState, logger *log.Logger) {
	if theme == nil {
		http.Error(w, "theme state is not configured", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		mode, systemDark, resolved := theme.snapshot()
		theme.syncBackground()
		writeThemeJSON(w, mode, systemDark, resolved, theme.languageValue())
	case http.MethodPut:
		var patch struct {
			Theme    *string `json:"theme"`
			Language *string `json:"language"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if patch.Theme == nil && patch.Language == nil {
			http.Error(w, "theme or language is required", http.StatusBadRequest)
			return
		}
		if patch.Theme != nil {
			if err := theme.setMode(*patch.Theme); err != nil {
				logger.Printf("theme-options: reject %q: %v", *patch.Theme, err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mode, _, resolved := theme.snapshot()
			logger.Printf("theme-options: user selected %s (resolved %s)", mode, resolved)
		}
		if patch.Language != nil {
			if err := theme.setLanguage(*patch.Language); err != nil {
				logger.Printf("i18n-options: reject %q: %v", *patch.Language, err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		mode, systemDark, resolved := theme.snapshot()
		writeThemeJSON(w, mode, systemDark, resolved, theme.languageValue())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeThemeJSON(w http.ResponseWriter, mode string, systemDark bool, resolved, language string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"theme":      mode,
		"systemDark": systemDark,
		"resolved":   resolved,
		"language":   language,
	})
}

func windowBackground(resolved string) application.RGBA {
	if resolved == themeDark {
		return application.NewRGB(20, 22, 28)
	}
	return application.NewRGB(245, 246, 248)
}

func sanitizeProbe(body string) string {
	body = strings.NewReplacer("\r", " ", "\n", " ").Replace(body)
	body = strings.TrimSpace(body)
	if len(body) > 512 {
		body = body[:512]
	}
	return body
}

// frontendDist locates the built React bundle. R0-00 deliberately serves the
// files from disk (not go:embed) so `go build ./...` works in a clean checkout
// before the frontend is built.
func frontendDist() (string, error) {
	var candidates []string
	if override := strings.TrimSpace(os.Getenv("ANVIL_FRONTEND_DIST")); override != "" {
		candidates = append(candidates, override)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "frontend", "dist"))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(exeDir, "frontend", "dist"), filepath.Join(exeDir, "dist"))
	}
	for _, candidate := range candidates {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if info, statErr := os.Stat(filepath.Join(abs, "index.html")); statErr == nil && !info.IsDir() {
			return abs, nil
		}
	}
	return "", fmt.Errorf("frontend dist not found (looked for index.html in %s); run npm run build or set ANVIL_FRONTEND_DIST",
		strings.Join(candidates, ", "))
}

// appDataDir requires an explicit D: path. There is no default on C:.
func appDataDir() (string, error) {
	raw := strings.TrimSpace(os.Getenv("ANVIL_DATA_DIR"))
	if raw == "" {
		return "", errors.New("ANVIL_DATA_DIR is required and must be a writable D: directory (no fallback to another drive)")
	}
	res, err := preflight.EnsureDir(raw, preflight.OSSystem{})
	if err != nil {
		return "", fmt.Errorf("ANVIL_DATA_DIR: %w", err)
	}
	return res.Resolved, nil
}
