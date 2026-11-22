//! Runtime extension commands (currently: pi's `subagent` extension).
//!
//! Stateless on purpose: the answer is a filesystem fact — what the pi package
//! ships now versus what was copied earlier — and a cached copy would go stale
//! the moment the user upgrades pi. The chat page asks on mount for the
//! session's runtime and draws the install banner from the reply; 设置 → 运行时
//! asks the same question and offers install / update.
//!
//! Both writes run as `async` commands: they touch several small files, and a
//! sync command would do that on the UI thread.

use anvil_session::extensions::{
    PiSubagentInstall, PiSubagentStatus, install_pi_subagent, pi_subagent_status as read_status,
    update_pi_subagent,
};

/// Whether the current pi installation has the `subagent` extension, whether
/// this build can install it, and whether the installed copy is behind the one
/// inside the current pi package.
#[tauri::command]
pub fn pi_subagent_status() -> PiSubagentStatus {
    read_status()
}

/// Copy the `subagent` extension shipped inside the installed pi into the agent
/// directory, then report the status that resulted.
#[tauri::command]
pub async fn pi_subagent_install() -> Result<PiSubagentInstall, String> {
    install_pi_subagent().map_err(|error| error.to_wire().to_string())
}

/// Replace the installed extension sources with the ones the current pi package
/// ships. Agent and prompt files the user edited are left alone.
#[tauri::command]
pub async fn pi_subagent_update() -> Result<PiSubagentInstall, String> {
    update_pi_subagent().map_err(|error| error.to_wire().to_string())
}
