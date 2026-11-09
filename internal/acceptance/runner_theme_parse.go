package acceptance

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

type themeLogEntry struct {
	Mode       string
	Resolved   string
	Background string
}

var themeLogPattern = regexp.MustCompile(`theme: mode=(system|light|dark) resolved=(light|dark) bg=(.+)$`)
var regDwordPattern = regexp.MustCompile(`0x([0-9a-fA-F]+)`)

func parseThemeLogs(text string) []themeLogEntry {
	var entries []themeLogEntry
	for _, line := range strings.Split(text, "\n") {
		match := themeLogPattern.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		entries = append(entries, themeLogEntry{
			Mode:       match[1],
			Resolved:   match[2],
			Background: strings.TrimSpace(match[3]),
		})
	}
	return entries
}

func latestThemeLog(text string) (themeLogEntry, bool) {
	entries := parseThemeLogs(text)
	if len(entries) == 0 {
		return themeLogEntry{}, false
	}
	return entries[len(entries)-1], true
}

type elementBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func parseElementBounds(body []byte) (elementBounds, bool) {
	var envelope struct {
		Data struct {
			Value elementBounds `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err != nil {
		return elementBounds{}, false
	}
	bounds := envelope.Data.Value
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return elementBounds{}, false
	}
	return bounds, true
}

// parseRegAppsUseLightTheme reads the DWORD printed by `reg query`.
func parseRegAppsUseLightTheme(output string) (int, bool) {
	match := regDwordPattern.FindStringSubmatch(output)
	if match == nil {
		return 0, false
	}
	value, err := strconv.ParseInt(match[1], 16, 32)
	if err != nil {
		return 0, false
	}
	return int(value), true
}
