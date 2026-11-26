//! Prepare a Node runtime in an empty prefix, the way a first install does.
//!
//! ```text
//! cargo run -p anvil-catalog --example prepare_node -- <prefix>
//! ```
//!
//! The app is a GUI, so this is the only way to exercise the download without
//! uninstalling Node from a machine and clicking a button.

use std::path::PathBuf;

use anvil_catalog::runtime::node_bootstrap;

fn main() {
    let prefix = std::env::args()
        .nth(1)
        .map(PathBuf::from)
        .expect("usage: prepare_node <prefix>");
    println!("prefix = {}", prefix.display());
    let mut log = |line: &str| println!("{line}");
    match node_bootstrap::ensure(&prefix, &mut log) {
        Ok(runtime) => println!(
            "OK version={} node={} npm={}",
            runtime.version,
            runtime.node.display(),
            runtime.npm.display()
        ),
        Err(error) => {
            println!("ERR {}", error.to_wire());
            std::process::exit(1);
        }
    }
}
