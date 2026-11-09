//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procEnumWindows     = user32.NewProc("EnumWindows")
	procGetWindowTextW  = user32.NewProc("GetWindowTextW")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procShowWindow      = user32.NewProc("ShowWindow")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	procGetForeground   = user32.NewProc("GetForegroundWindow")
	procSendInput       = user32.NewProc("SendInput")
)

const (
	inputKeyboard  = 1
	keyeventfKeyUp = 0x0002
	vkMenu         = 0x12
	vkLeft         = 0x25
	vkEscape       = 0x1B
	swRestore      = 9
)

type keyboardInput struct {
	Vk        uint16
	Scan      uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// input mirrors the Win32 INPUT struct. The union is 32 bytes on x64 (the
// largest member is MOUSEINPUT); KEYBDINPUT occupies its first 24 bytes.
type input struct {
	Type uint32
	_    uint32
	Data [32]byte
}

// sendKeyCombo brings the window titled windowTitle to the foreground and
// injects Alt+Left (the browser-history shortcut).
func sendKeyCombo(windowTitle, combo string) error {
	events, err := keyEvents(strings.ToLower(strings.TrimSpace(combo)))
	if err != nil {
		return err
	}
	hwnd, err := findWindowByTitle(windowTitle)
	if err != nil {
		return err
	}
	procShowWindow.Call(hwnd, swRestore)
	procSetForeground.Call(hwnd)
	time.Sleep(350 * time.Millisecond)
	foreground, _, _ := procGetForeground.Call()
	if foreground != hwnd {
		return fmt.Errorf("window %q did not become foreground", windowTitle)
	}

	sent, _, callErr := procSendInput.Call(
		uintptr(len(events)),
		uintptr(unsafe.Pointer(&events[0])),
		unsafe.Sizeof(events[0]),
	)
	if sent != uintptr(len(events)) {
		return fmt.Errorf("SendInput sent %d/%d events: %v", sent, len(events), callErr)
	}
	time.Sleep(100 * time.Millisecond)
	return nil
}

func keyEvents(combo string) ([]input, error) {
	switch combo {
	case "alt+left":
		return []input{
			newKeyInput(vkMenu, 0),
			newKeyInput(vkLeft, 0),
			newKeyInput(vkLeft, keyeventfKeyUp),
			newKeyInput(vkMenu, keyeventfKeyUp),
		}, nil
	case "escape":
		return []input{newKeyInput(vkEscape, 0), newKeyInput(vkEscape, keyeventfKeyUp)}, nil
	default:
		return nil, fmt.Errorf("unsupported key combo %q (alt+left or escape)", combo)
	}
}

func newKeyInput(vk uint16, flags uint32) input {
	event := input{Type: inputKeyboard}
	keyboard := (*keyboardInput)(unsafe.Pointer(&event.Data[0]))
	keyboard.Vk = vk
	keyboard.Flags = flags
	return event
}

func findWindowByTitle(title string) (uintptr, error) {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1
		}
		var buf [512]uint16
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 {
			return 1
		}
		if strings.EqualFold(syscall.UTF16ToString(buf[:n]), title) {
			found = hwnd
			return 0
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	if found == 0 {
		return 0, fmt.Errorf("window titled %q was not found", title)
	}
	return found, nil
}
