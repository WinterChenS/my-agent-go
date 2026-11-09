package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

type layoutProbe struct {
	Viewport    string
	ScrollWidth int
	ClientWidth int
	Overflow    bool
}

var probePattern = regexp.MustCompile(`layout-probe: viewport=(\d+x\d+) scrollWidth=(\d+) clientWidth=(\d+) overflow=(true|false)`)
var windowOptionsPattern = regexp.MustCompile(`window-options: requested=(\d+)x(\d+) min=(\d+)x(\d+)`)

func parseLayoutProbes(log string) []layoutProbe {
	var probes []layoutProbe
	for _, match := range probePattern.FindAllStringSubmatch(log, -1) {
		scrollWidth, _ := strconv.Atoi(match[2])
		clientWidth, _ := strconv.Atoi(match[3])
		probes = append(probes, layoutProbe{
			Viewport:    match[1],
			ScrollWidth: scrollWidth,
			ClientWidth: clientWidth,
			Overflow:    match[4] == "true",
		})
	}
	return probes
}

func countClearProbes(probes []layoutProbe) int {
	clear := 0
	for _, probe := range probes {
		if !probe.Overflow {
			clear++
		}
	}
	return clear
}

func parseFindRef(body []byte) (string, bool) {
	var envelope struct {
		Data struct {
			Match *struct {
				RefID string `json:"ref_id"`
			} `json:"match"`
			Matches []struct {
				RefID string `json:"ref_id"`
			} `json:"matches"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err != nil {
		return "", false
	}
	if envelope.Data.Match != nil && envelope.Data.Match.RefID != "" {
		return envelope.Data.Match.RefID, true
	}
	for _, match := range envelope.Data.Matches {
		if match.RefID != "" {
			return match.RefID, true
		}
	}
	return "", false
}

func parseFindCount(body []byte) int {
	var envelope struct {
		Data struct {
			TotalMatches int               `json:"total_matches"`
			Count        int               `json:"count"`
			Matches      []json.RawMessage `json:"matches"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err != nil {
		return 0
	}
	if envelope.Data.TotalMatches > 0 {
		return envelope.Data.TotalMatches
	}
	if envelope.Data.Count > 0 {
		return envelope.Data.Count
	}
	return len(envelope.Data.Matches)
}

type windowGeometry struct {
	Width     int
	Height    int
	MinWidth  int
	MinHeight int
}

func parseWindowOptions(line string) (windowGeometry, bool) {
	match := windowOptionsPattern.FindStringSubmatch(line)
	if match == nil {
		return windowGeometry{}, false
	}
	parse := func(value string) int {
		number, _ := strconv.Atoi(value)
		return number
	}
	return windowGeometry{
		Width:     parse(match[1]),
		Height:    parse(match[2]),
		MinWidth:  parse(match[3]),
		MinHeight: parse(match[4]),
	}, true
}

type windowBounds struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func parseFirstWindow(body []byte) (windowBounds, bool) {
	var envelope struct {
		Data []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Bounds struct {
				X      float64 `json:"x"`
				Y      float64 `json:"y"`
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"bounds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err != nil || len(envelope.Data) == 0 {
		return windowBounds{}, false
	}
	first := envelope.Data[0]
	return windowBounds{ID: first.ID, Title: first.Title, X: first.Bounds.X, Y: first.Bounds.Y, Width: first.Bounds.Width, Height: first.Bounds.Height}, true
}

type shellRoute struct {
	ID               string `json:"id"`
	Path             string `json:"path"`
	Source           string `json:"source"`
	Title            string `json:"title"`
	NavLabel         string `json:"navLabel"`
	Group            string `json:"group"`
	Chrome           string `json:"chrome"`
	BreadcrumbParent string `json:"breadcrumbParent"`
	FutureTask       string `json:"futureTask"`
	Marker           string `json:"marker"`
}

func readShellRoutes(repo string) ([]shellRoute, error) {
	path := filepath.Join(repo, "frontend", "src", "shell", "routes.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read shell routes: %w", err)
	}
	var document struct {
		Screens []shellRoute `json:"screens"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("parse shell routes: %w", err)
	}
	if len(document.Screens) != 9 {
		return nil, fmt.Errorf("shell routes declares %d screens, want 9", len(document.Screens))
	}
	return document.Screens, nil
}

func parseFindNames(body []byte) []string {
	var envelope struct {
		Data struct {
			Match *struct {
				Name string `json:"name"`
			} `json:"match"`
			Matches []struct {
				Name string `json:"name"`
			} `json:"matches"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err != nil {
		return nil
	}
	var names []string
	if envelope.Data.Match != nil && envelope.Data.Match.Name != "" {
		names = append(names, envelope.Data.Match.Name)
	}
	for _, match := range envelope.Data.Matches {
		if match.Name != "" {
			names = append(names, match.Name)
		}
	}
	return names
}

type manifestViewport struct {
	Name                      string `json:"name"`
	Width                     int    `json:"width"`
	Height                    int    `json:"height"`
	Category                  string `json:"category"`
	MustAvoidHorizontalScroll bool   `json:"mustAvoidHorizontalScroll"`
}

func readManifestViewports(repo string) ([]manifestViewport, error) {
	path := filepath.Join(repo, "docs", "ui", "Agent-客户端产品原型", "DESIGN-MANIFEST.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read prototype manifest: %w", err)
	}
	var manifest struct {
		ResponsiveViewports []manifestViewport `json:"responsiveViewports"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("parse prototype manifest: %w", err)
	}
	if len(manifest.ResponsiveViewports) == 0 {
		return nil, fmt.Errorf("prototype manifest declares no responsive viewports")
	}
	return manifest.ResponsiveViewports, nil
}
