fn main() {
    if let Err(error) = anvil::desktop::run() {
        eprintln!("Anvil: {error}");
        report_startup_failure(&error.to_string());
        std::process::exit(1);
    }
}

/// A desktop app started by double-click has no console, so an error printed to
/// stderr is invisible and a failed preflight looks like "it will not start".
/// Put the message somewhere it can be read, and show it.
fn report_startup_failure(error: &str) {
    let path = std::env::temp_dir().join("anvil-startup-error.txt");
    let text = format!(
        "Anvil 无法启动 / Anvil could not start\n\n{error}\n\n诊断信息已写入 / details: {}\n",
        path.display()
    );
    let _ = std::fs::write(&path, &text);
    #[cfg(windows)]
    {
        use windows_sys::Win32::UI::WindowsAndMessaging::{MB_ICONERROR, MB_OK, MessageBoxW};

        let wide = |text: &str| -> Vec<u16> { text.encode_utf16().chain(std::iter::once(0)).collect() };
        let body = wide(&text);
        let title = wide("Anvil");
        // Ignoring the result is deliberate: if the dialog itself fails there is
        // nothing left to report to.
        unsafe {
            MessageBoxW(
                std::ptr::null_mut(),
                body.as_ptr(),
                title.as_ptr(),
                MB_OK | MB_ICONERROR,
            );
        }
    }
}
