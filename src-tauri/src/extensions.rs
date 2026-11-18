//! Runtime extension commands (currently: pi's `subagent` extension).
//!
//! Two commands, deliberately stateless: the answer is a filesystem fact, and a
//! cached copy would go stale the moment the user installs pi or runs the
//! installer. The chat page asks on mount for the session's runtime and draws
//! the install banner from the reply.
//!
//! Install runs as an `async` command: it writes several small files, and a sync
//! command would do that on the UI thread.

use anvil_session::extensions::{
    PiSubagentInstall, PiSubagentStatus, install_pi_subagent, pi_subagent_status as read_status,
};

/// Whether the current pi installation has the `subagent` extension, and
/// whether this build can install it.
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
