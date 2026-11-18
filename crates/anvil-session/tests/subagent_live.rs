//! Real-runtime evidence that the installer produces a working delegation.
//!
//! The unit tests prove the files land and that the sample model pins are
//! commented out. This one proves the point of the feature: after an install,
//! `pi` loads the extension, the `subagent` tool exists, a child agent runs on
//! the parent's model, and it can read a file the parent handed it.
//!
//! It needs `pi` on `PATH`, its configured provider, and one model turn per
//! agent, so it is ignored by the default suite:
//!
//! `cargo test -p anvil-session --test subagent_live -- --ignored`
//!
//! The install goes into a scratch agent directory (`PI_CODING_AGENT_DIR`), not
//! the user's `~/.pi/agent`: the test must not change the machine it runs on.

use std::process::{Command, Stdio};

use anvil_session::adapters::find_on_path;
use anvil_session::extensions::{install_pi_subagent, pi_subagent_status};

#[test]
#[ignore = "requires pi on PATH, its configured provider, and model turns"]
fn an_installed_subagent_extension_makes_pi_delegate_to_a_real_child() {
    let root = std::env::temp_dir().join(format!("anvil-subagent-live-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    let agent_dir = root.join("agent");
    std::fs::create_dir_all(&agent_dir).expect("agent dir");
    let work = root.join("work");
    std::fs::create_dir_all(&work).expect("work dir");
    std::fs::write(work.join("probe.txt"), "probe-value-7421\n").expect("probe file");

    // The child pi needs a provider and a credential. They are copied, never
    // read by this process, and the whole scratch tree is removed at the end.
    let real = std::env::var_os("USERPROFILE")
        .map(|home| std::path::PathBuf::from(home).join(".pi").join("agent"))
        .or_else(|| {
            std::env::var_os("HOME").map(|home| std::path::PathBuf::from(home).join(".pi/agent"))
        });
    let Some(real) = real.filter(|dir| dir.is_dir()) else {
        panic!("no ~/.pi/agent: this test needs a configured pi");
    };
    for name in ["models.json", "auth.json", "settings.json"] {
        let from = real.join(name);
        if from.is_file() {
            std::fs::copy(&from, agent_dir.join(name)).expect("seed provider config");
        }
    }

    // `pi` reads the directory from its environment, and the host commands read
    // the same variable. One process, one test: the write is not racing anyone.
    unsafe {
        std::env::set_var("PI_CODING_AGENT_DIR", &agent_dir);
    }

    let before = pi_subagent_status();
    assert!(!before.installed, "the scratch agent dir must start empty");
    assert!(
        before.can_install,
        "the installed pi package must ship the example: {before:?}"
    );

    let report = install_pi_subagent().expect("install");
    assert!(report.status.installed, "{report:?}");
    assert!(report.status.agents >= 4, "{report:?}");
    assert!(!report.status.can_install, "{report:?}");

    let exe = find_on_path("pi").expect("pi on PATH");
    let prompt = "Use the subagent tool exactly once with agent 'scout' and task \
                  'Read probe.txt in the current directory and reply with its exact contents'. \
                  Then answer with the subagent result.";
    let output = Command::new(exe)
        .args([
            "-p",
            prompt,
            "--mode",
            "json",
            "--tools",
            "subagent",
            "--thinking",
            "low",
            "--session-dir",
        ])
        .arg(root.join("sessions"))
        .arg("--session-id")
        .arg("probe")
        .current_dir(&work)
        // A live stdin makes `pi -p` wait forever; see the pi adapter's docs.
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .output()
        .expect("run pi");

    let stdout = String::from_utf8_lossy(&output.stdout).to_string();
    let stderr = String::from_utf8_lossy(&output.stderr).to_string();
    assert!(output.status.success(), "pi failed: {stderr}");
    assert!(
        stdout.contains("\"toolName\":\"subagent\""),
        "the subagent tool was never called. stderr: {stderr}\nstdout: {stdout}"
    );
    assert!(
        stdout.contains("probe-value-7421"),
        "the child did not read the file. stderr: {stderr}\nstdout: {stdout}"
    );
    // The two failures this feature exists to prevent: an unknown tool (pi
    // ignores the name) and the ambiguous sample model pin.
    assert!(
        !stdout.contains("ambiguous across providers"),
        "the installed agents still pin the author's model: {stdout}"
    );

    let _ = std::fs::remove_dir_all(&root);
}
