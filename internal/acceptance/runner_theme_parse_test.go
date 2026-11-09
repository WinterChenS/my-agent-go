package acceptance

import "testing"

func TestParseThemeLogs(t *testing.T) {
	log := "2026/11/09 theme-options: mode=system resolved=dark\n" +
		"2026/11/09 theme: mode=system resolved=dark bg=oklch(16% .012 255)\n" +
		"2026/11/09 theme: mode=light resolved=light bg=oklch(99% .002 240)\n" +
		"2026/11/09 theme-options: user selected light\n"
	entries := parseThemeLogs(log)
	if len(entries) != 2 {
		t.Fatalf("theme logs = %#v", entries)
	}
	if entries[1].Mode != "light" || entries[1].Resolved != "light" || entries[1].Background == "" {
		t.Fatalf("last theme log = %#v", entries[1])
	}
	if latest, ok := latestThemeLog(log); !ok || latest.Resolved != "light" {
		t.Fatalf("latest = %#v %v", latest, ok)
	}
}

func TestParseElementBounds(t *testing.T) {
	body := []byte(`{"ok":true,"data":{"live":true,"property":"bounds","value":{"x":875,"y":343,"width":15,"height":16}}}`)
	bounds, ok := parseElementBounds(body)
	if !ok || bounds.X != 875 || bounds.Width != 15 || bounds.Height != 16 {
		t.Fatalf("bounds = %#v %v", bounds, ok)
	}
	if _, ok := parseElementBounds([]byte(`{"data":{"value":{"width":0,"height":0}}}`)); ok {
		t.Fatal("zero bounds accepted")
	}
}

func TestParseRegAppsUseLightTheme(t *testing.T) {
	if value, ok := parseRegAppsUseLightTheme("    AppsUseLightTheme    REG_DWORD    0x1"); !ok || value != 1 {
		t.Fatalf("parse 0x1 = %d %v", value, ok)
	}
	if value, ok := parseRegAppsUseLightTheme("    AppsUseLightTheme    REG_DWORD    0x0"); !ok || value != 0 {
		t.Fatalf("parse 0x0 = %d %v", value, ok)
	}
	if _, ok := parseRegAppsUseLightTheme("ERROR: not found"); ok {
		t.Fatal("missing value parsed as found")
	}
}
