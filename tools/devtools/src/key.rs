//! `key`: inject an Alt+Left or Escape keystroke into the client window.
//!
//! Ported from `cmd/devtools/key_windows.go`. The shell case needs Alt+Left
//! because the product has no separate back button, and the dialog cases need
//! Escape; `agent-desktop press` cannot verify focus inside a WebView2 child,
//! so the shortcuts go through `SendInput` after an explicit foreground
//! switch. Every failure is an environment problem (exit 2), never a product
//! verdict.

use serde_json::json;

/// Windows key codes used by the two supported combos.
const VK_MENU: u16 = 0x12;
const VK_LEFT: u16 = 0x25;
const VK_ESCAPE: u16 = 0x1B;

/// Inject `combo` into the first visible window titled `title`.
pub(crate) fn send_key_combo(title: &str, combo: &str) -> Result<(), String> {
    let events = key_events(combo)?;
    imp::send(&events, title)
}

/// Writes the JSON report the Go CLI printed and returns the exit code.
pub(crate) fn run(title: &str, combo: &str) -> i32 {
    let report = match send_key_combo(title, combo) {
        Ok(()) => json!({ "title": title, "combo": combo, "ok": true }),
        Err(err) => {
            eprintln!("key: {err}");
            json!({ "title": title, "combo": combo, "ok": false, "error": err })
        }
    };
    println!(
        "{}",
        serde_json::to_string_pretty(&report).unwrap_or_default()
    );
    if report["ok"] == json!(true) { 0 } else { 2 }
}

/// One keyboard event: virtual key plus the key-up flag.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct KeyEvent {
    vk: u16,
    key_up: bool,
}

/// The event sequence of a supported combo.
fn key_events(combo: &str) -> Result<Vec<KeyEvent>, String> {
    let normalised = combo.trim().to_lowercase();
    match normalised.as_str() {
        "alt+left" => Ok(vec![
            KeyEvent {
                vk: VK_MENU,
                key_up: false,
            },
            KeyEvent {
                vk: VK_LEFT,
                key_up: false,
            },
            KeyEvent {
                vk: VK_LEFT,
                key_up: true,
            },
            KeyEvent {
                vk: VK_MENU,
                key_up: true,
            },
        ]),
        "escape" => Ok(vec![
            KeyEvent {
                vk: VK_ESCAPE,
                key_up: false,
            },
            KeyEvent {
                vk: VK_ESCAPE,
                key_up: true,
            },
        ]),
        other => Err(format!(
            "unsupported key combo {other:?} (alt+left or escape)"
        )),
    }
}

#[cfg(windows)]
mod imp {
    use std::ffi::c_void;

    use windows_sys::Win32::Foundation::{HWND, LPARAM};
    use windows_sys::Win32::UI::Input::KeyboardAndMouse::{
        INPUT, INPUT_KEYBOARD, KEYBDINPUT, KEYEVENTF_KEYUP, SendInput,
    };
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        EnumWindows, GetForegroundWindow, GetWindowTextW, IsWindowVisible, SW_RESTORE,
        SetForegroundWindow, ShowWindow,
    };

    use super::KeyEvent;

    const MAX_TITLE: usize = 512;

    struct FindState {
        wanted: Vec<u16>,
        found: HWND,
    }

    /// Focus the window and send the events.
    pub(super) fn send(events: &[KeyEvent], title: &str) -> Result<(), String> {
        let hwnd = find_window(title)?;
        // SAFETY: both calls take a window handle we just resolved.
        unsafe {
            ShowWindow(hwnd, SW_RESTORE);
            SetForegroundWindow(hwnd);
        }
        std::thread::sleep(std::time::Duration::from_millis(350));
        // SAFETY: no pointer arguments.
        let foreground = unsafe { GetForegroundWindow() };
        if foreground != hwnd {
            return Err(format!("window {title:?} did not become foreground"));
        }
        let inputs: Vec<INPUT> = events.iter().map(to_input).collect();
        let size = std::mem::size_of::<INPUT>() as i32;
        // SAFETY: `inputs` is a live, correctly sized array of INPUT records.
        let sent = unsafe { SendInput(inputs.len() as u32, inputs.as_ptr(), size) };
        if sent as usize != inputs.len() {
            return Err(format!("SendInput sent {sent}/{} events", inputs.len()));
        }
        std::thread::sleep(std::time::Duration::from_millis(100));
        Ok(())
    }

    fn to_input(event: &KeyEvent) -> INPUT {
        let mut input: INPUT = unsafe { std::mem::zeroed() };
        input.r#type = INPUT_KEYBOARD;
        let mut keyboard: KEYBDINPUT = unsafe { std::mem::zeroed() };
        keyboard.wVk = event.vk;
        keyboard.dwFlags = if event.key_up { KEYEVENTF_KEYUP } else { 0 };
        input.Anonymous.ki = keyboard;
        input
    }

    /// First visible top-level window whose title equals `title`.
    fn find_window(title: &str) -> Result<HWND, String> {
        let mut state = FindState {
            wanted: title.encode_utf16().collect(),
            found: std::ptr::null_mut::<c_void>(),
        };
        // SAFETY: the callback only reads the state pointer we pass in and the
        // window handle the API supplies; both outlive the call.
        unsafe {
            EnumWindows(Some(enum_proc), &mut state as *mut FindState as LPARAM);
        }
        if state.found.is_null() {
            return Err(format!("window titled {title:?} was not found"));
        }
        Ok(state.found)
    }

    unsafe extern "system" fn enum_proc(hwnd: HWND, lparam: LPARAM) -> i32 {
        let state = unsafe { &mut *(lparam as *mut FindState) };
        // SAFETY: `hwnd` is supplied by EnumWindows.
        if unsafe { IsWindowVisible(hwnd) } == 0 {
            return 1;
        }
        let mut buffer = [0u16; MAX_TITLE];
        // SAFETY: the buffer is writable and its length is passed verbatim.
        let written = unsafe { GetWindowTextW(hwnd, buffer.as_mut_ptr(), MAX_TITLE as i32) };
        if written <= 0 {
            return 1;
        }
        if buffer[..written as usize] == state.wanted[..] {
            state.found = hwnd;
            return 0;
        }
        1
    }
}

#[cfg(not(windows))]
mod imp {
    use super::KeyEvent;

    pub(super) fn send(_events: &[KeyEvent], _title: &str) -> Result<(), String> {
        Err("the key injector is Windows-only".to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn alt_left_presses_and_releases_the_modifier_last() {
        let events = key_events("Alt+Left").expect("combo");
        assert_eq!(events.len(), 4);
        assert_eq!(events[0].vk, VK_MENU);
        assert!(!events[0].key_up);
        assert!(events[3].key_up, "Alt must be released last");
    }

    #[test]
    fn escape_is_a_single_press_and_release() {
        let events = key_events(" escape ").expect("combo");
        assert_eq!(events.len(), 2);
        assert_eq!(events[0].vk, VK_ESCAPE);
        assert!(events[1].key_up);
    }

    #[test]
    fn unsupported_combos_are_rejected() {
        let err = key_events("ctrl+alt+delete").expect_err("must reject");
        assert!(err.contains("unsupported key combo"), "{err}");
    }
}
