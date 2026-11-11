//! Tauri build script.
//!
//! R0-RS02 does not embed `frontend/dist` at compile time: like the retired Go
//! host (`application.AssetFileServerFS`), the desktop host serves the built
//! React bundle from disk at runtime, so `cargo test`/`cargo build` work in a
//! clean checkout before the frontend is built. `build.frontendDist` in
//! `tauri.conf.json` stays the canonical location description.
fn main() {
    tauri_build::build();
}
