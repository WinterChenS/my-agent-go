package acceptance

import "testing"

func TestParseI18nLogs(t *testing.T) {
	log := "i18n-options: language=zh-CN\n" +
		"2026 i18n: language=zh-CN htmlLang=zh-CN\n" +
		"2026 i18n-title: title=设置 · Anvil\n" +
		"2026 i18n: language=en-US htmlLang=en-US\n" +
		"2026 i18n-title: title=Settings · Anvil\n"
	entries := parseI18nLogs(log)
	if len(entries) != 2 || entries[1].Language != "en-US" || entries[1].HTMLLang != "en-US" {
		t.Fatalf("i18n logs = %#v", entries)
	}
	if latest, ok := latestI18nLog(log); !ok || latest.Language != "en-US" {
		t.Fatalf("latest = %#v %v", latest, ok)
	}
	if title, ok := parseTitleLog(log); !ok || title != "Settings · Anvil" {
		t.Fatalf("last title = %q %v", title, ok)
	}
	if language, ok := parseLanguagePreference([]byte(`{"theme":"system","language":"en-US"}`)); !ok || language != "en-US" {
		t.Fatalf("language preference = %q %v", language, ok)
	}
}
