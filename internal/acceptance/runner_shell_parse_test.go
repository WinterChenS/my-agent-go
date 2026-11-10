package acceptance

import "testing"

func TestParseWindowsKeepsProcessIdentity(t *testing.T) {
	body := []byte(`{"data":[{"id":"w-1","title":"Anvil","pid":4242,"process_instance":"abc-123","bounds":{"x":10,"y":20,"width":900,"height":700}}]}`)
	windows, ok := parseWindows(body)
	if !ok || len(windows) != 1 {
		t.Fatalf("parseWindows = %+v, %v", windows, ok)
	}
	if windows[0].PID != 4242 || windows[0].ProcessInstance != "abc-123" {
		t.Fatalf("identity = pid %d instance %q", windows[0].PID, windows[0].ProcessInstance)
	}
	if windows[0].Title != "Anvil" || windows[0].Width != 900 || windows[0].Height != 700 {
		t.Fatalf("window = %+v", windows[0])
	}
}

func TestParseWindowsEmptyList(t *testing.T) {
	windows, ok := parseWindows([]byte(`{"data":[]}`))
	if !ok || len(windows) != 0 {
		t.Fatalf("parseWindows empty = %+v, %v", windows, ok)
	}
	if _, ok := parseFirstWindow([]byte(`{"data":[]}`)); ok {
		t.Fatal("parseFirstWindow returned a window for an empty list")
	}
}
