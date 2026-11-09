package acceptance

import (
	"encoding/json"
	"regexp"
	"strings"
)

type languageLogEntry struct {
	Language string
	HTMLLang string
}

var i18nPattern = regexp.MustCompile(`i18n: language=(zh-CN|en-US) htmlLang=([A-Za-z-]+)`)
var titlePattern = regexp.MustCompile(`i18n-title: title=(.+)$`)

func parseI18nLogs(text string) []languageLogEntry {
	var entries []languageLogEntry
	for _, line := range strings.Split(text, "\n") {
		match := i18nPattern.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		entries = append(entries, languageLogEntry{Language: match[1], HTMLLang: match[2]})
	}
	return entries
}

func latestI18nLog(text string) (languageLogEntry, bool) {
	entries := parseI18nLogs(text)
	if len(entries) == 0 {
		return languageLogEntry{}, false
	}
	return entries[len(entries)-1], true
}

func parseTitleLog(text string) (string, bool) {
	title := ""
	found := false
	for _, line := range strings.Split(text, "\n") {
		match := titlePattern.FindStringSubmatch(strings.TrimSpace(line))
		if match != nil {
			title = strings.TrimSpace(match[1])
			found = true
		}
	}
	return title, found
}

func parseLanguagePreference(body []byte) (string, bool) {
	var pref struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(body, &pref); err != nil || pref.Language == "" {
		return "", false
	}
	return pref.Language, true
}
