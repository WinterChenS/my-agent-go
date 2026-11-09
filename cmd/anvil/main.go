// Command anvil is the Anvil desktop host (Wails v3).
//
// R0-00 only delivers a launchable shell: the window loads the React bundle
// built by Vite and nothing else. Pages, theme and i18n are implemented by the
// following tasks. All writable locations must be explicit D: paths; the
// process refuses to fall back to a default location on another drive.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"my-agent-go/internal/preflight"
)

const appName = "Anvil"

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
	log.Printf("starting: dist=%s data=%s webview=%s", dist, dataDir, webviewDir)

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
			Handler:        application.AssetFileServerFS(os.DirFS(dist)),
			DisableLogging: true,
		},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            appName,
		Width:            1180,
		Height:           820,
		MinWidth:         880,
		MinHeight:        600,
		URL:              "/",
		BackgroundColour: application.NewRGB(245, 246, 248),
	})
	log.Printf("window created; running")
	if err := app.Run(); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
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
