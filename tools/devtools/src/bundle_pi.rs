//! `bundle-pi`: stage the Pi runtime Anvil ships inside its installers.
//!
//! ADR 0009 moves Pi from "host dependency the app discovers" to "asset the
//! installer carries". This command is the only thing that produces that asset,
//! and it is deliberately strict: a wrong-architecture Node, a missing CLI, or a
//! missing sub-agent example all make it exit non-zero, so a release job stops
//! before uploading an installer that can be installed but never run.
//!
//! What it writes under `<out>` (the directory Tauri maps to `runtime/pi/`):
//!
//! ```text
//! node/node.exe            Windows; `node/bin/node` on macOS and Linux
//! node/LICENSE             Node's own licence, as distributed
//! pi/package.json          the pinned dependency input
//! pi/package-lock.json     ditto — `npm ci` only ever sees these two
//! pi/node_modules/…        `npm ci --omit=dev --ignore-scripts` output
//! manifest.json            what is inside, with hashes, for the host to check
//! ```
//!
//! Nothing here talks to the network except the two documented reads: the
//! official Node archive (verified against a pinned SHA-256) and the npm
//! registry the lockfile names.

use std::path::{Path, PathBuf};
use std::process::Command;

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::gate::{EXIT_ASSERTION, EXIT_INCOMPLETE, EXIT_PASS};

/// Node release the installers carry. Matches `engines.node` in
/// `src-tauri/runtime-pi/package.json` — Pi 1.6.0 needs `>=22.19.0`, and a
/// single pinned build keeps every platform on the same interpreter.
pub const NODE_VERSION: &str = "24.16.0";

/// Private npm project whose lockfile decides the Pi tree.
pub const NPM_PROJECT: &str = "src-tauri/runtime-pi";

/// Where the staged tree lives unless `--out` says otherwise.
pub const DEFAULT_OUT: &str = "src-tauri/bundle-runtime/pi";

/// Directory name inside the Tauri install resources (`bundle.resources` in
/// `src-tauri/tauri.bundle.conf.json` maps this tree to `runtime/pi/`).
pub const RESOURCE_DIR: &str = "runtime/pi";

const PI_CLI_TAIL: &str = "dist/bundle/cli.js";
const PI_AI_TAIL: &str = "dist/providers/all.js";
const SUBAGENT_TAIL: &str = "examples/extensions/subagent";

/// One release target: the repo's target id, the Node archive for it, and the
/// SHA-256 that archive must hash to.
///
/// The hashes are Node's published `SHASUMS256.txt` entries for v24.16.0,
/// copied in rather than fetched: a build that trusts whatever the network
/// hands it has no integrity check at all.
pub struct Target {
    pub id: &'static str,
    pub platform: &'static str,
    pub arch: &'static str,
    pub archive: &'static str,
    pub sha256: &'static str,
}

pub const TARGETS: [Target; 6] = [
    Target {
        id: "win-x64",
        platform: "windows",
        arch: "x64",
        archive: "node-v24.16.0-win-x64.zip",
        sha256: "edaca9bd58ec8e92037dac4e877d52f6b8f430b81c18b57e264b4e2fb111cd56",
    },
    Target {
        id: "win-arm64",
        platform: "windows",
        arch: "arm64",
        archive: "node-v24.16.0-win-arm64.zip",
        sha256: "14834611d4c6b3c06054e7007732b90474c16e0b32f395e05b55a571ef71c6d2",
    },
    Target {
        id: "mac-x64",
        platform: "macos",
        arch: "x64",
        archive: "node-v24.16.0-darwin-x64.tar.gz",
        sha256: "298b4c7b3cb80765c8703e42b90324a4ece3b6634947b89e769c3c980ab55185",
    },
    Target {
        id: "mac-arm64",
        platform: "macos",
        arch: "arm64",
        archive: "node-v24.16.0-darwin-arm64.tar.gz",
        sha256: "39189dab4eeb15706c424af0ac08a3044c9e48f7db12a7d77f6b7aafc7dd5df6",
    },
    Target {
        id: "linux-x64",
        platform: "linux",
        arch: "x64",
        archive: "node-v24.16.0-linux-x64.tar.gz",
        sha256: "2faf6a387e9b62b888e21c54f01249fb27537ffecf1842f29f4c919d0a59a0ff",
    },
    Target {
        id: "linux-arm64",
        platform: "linux",
        arch: "arm64",
        archive: "node-v24.16.0-linux-arm64.tar.gz",
        sha256: "589f5b6dd4fcfee4dfda73013903c966abaa8abd93dbc9d436544e472b4f0e74",
    },
];

/// The target with this id, if the build claims to support it.
#[must_use]
pub fn target(id: &str) -> Option<&'static Target> {
    TARGETS.iter().find(|entry| entry.id == id)
}

/// Machine type read out of a binary header.
///
/// Cross-builds cannot execute the Node they stage (a macOS Intel archive on an
/// arm64 runner, a Windows one on Linux CI), so "does it start and say 24.16.0"
/// is only answerable on the build host for its own platform. The header is
/// answerable anywhere, and it is exactly the failure that matters: a Node built
/// for the wrong architecture installs fine and dies with "cannot execute
/// binary file" on the user's machine.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Arch {
    X64,
    Arm64,
    Other(u16),
}

impl Arch {
    fn matches(self, arch: &str) -> bool {
        matches!((self, arch), (Arch::X64, "x64") | (Arch::Arm64, "arm64"))
    }

    fn name(self) -> String {
        match self {
            Arch::X64 => "x64".to_string(),
            Arch::Arm64 => "arm64".to_string(),
            Arch::Other(value) => format!("machine 0x{value:04x}"),
        }
    }
}

/// Read the architecture out of a PE, Mach-O or ELF header.
pub fn binary_arch(path: &Path) -> Result<Arch, String> {
    use std::io::{Read, Seek, SeekFrom};

    let mut file =
        std::fs::File::open(path).map_err(|error| format!("{}: {error}", path.display()))?;
    let mut head = [0u8; 64];
    let read = file
        .read(&mut head)
        .map_err(|error| format!("{}: {error}", path.display()))?;
    if read < 4 {
        return Err(format!("{}: not a binary (too short)", path.display()));
    }
    // PE: "MZ" then, at e_lfanew, "PE\0\0" + machine.
    if &head[..2] == b"MZ" {
        let offset = u32::from_le_bytes([head[0x3c], head[0x3d], head[0x3e], head[0x3f]]) as u64;
        file.seek(SeekFrom::Start(offset))
            .map_err(|error| error.to_string())?;
        let mut pe = [0u8; 6];
        file.read_exact(&mut pe)
            .map_err(|error| format!("{}: truncated PE header: {error}", path.display()))?;
        if &pe[..4] != b"PE\0\0" {
            return Err(format!("{}: MZ without a PE header", path.display()));
        }
        let machine = u16::from_le_bytes([pe[4], pe[5]]);
        return Ok(match machine {
            0x8664 => Arch::X64,
            0xaa64 => Arch::Arm64,
            other => Arch::Other(other),
        });
    }
    // Mach-O: the four magic bytes say both "Mach-O" and which way the fields
    // are written. Node ships thin little-endian 64-bit binaries; a universal
    // binary is refused because it cannot answer "which architecture".
    let magic = [head[0], head[1], head[2], head[3]];
    let little = match magic {
        [0xce, 0xfa, 0xed, 0xfe] | [0xcf, 0xfa, 0xed, 0xfe] => Some(true),
        [0xfe, 0xed, 0xfa, 0xce] | [0xfe, 0xed, 0xfa, 0xcf] => Some(false),
        [0xca, 0xfe, 0xba, 0xbe] | [0xbe, 0xba, 0xfe, 0xca] => {
            return Err(format!(
                "{}: universal (fat) Mach-O; stage a single-architecture Node",
                path.display()
            ));
        }
        _ => None,
    };
    if let Some(little) = little {
        let raw = [head[4], head[5], head[6], head[7]];
        let cpu = if little {
            u32::from_le_bytes(raw)
        } else {
            u32::from_be_bytes(raw)
        };
        return Ok(macho_arch(cpu));
    }
    // ELF: e_machine at offset 18, endianness from EI_DATA.
    if &head[..4] == b"\x7fELF" {
        let little = head[5] == 1;
        let raw = [head[18], head[19]];
        let machine = if little {
            u16::from_le_bytes(raw)
        } else {
            u16::from_be_bytes(raw)
        };
        return Ok(match machine {
            0x3e => Arch::X64,
            0xb7 => Arch::Arm64,
            other => Arch::Other(other),
        });
    }
    Err(format!(
        "{}: unrecognised executable format (PE / Mach-O / ELF expected)",
        path.display()
    ))
}

fn macho_arch(cpu: u32) -> Arch {
    // CPU_ARCH_ABI64 (0x0100_0000) marks a 64-bit image; the low byte names the
    // architecture. Node ships 64-bit builds for both macOS targets.
    match (cpu & 0x0100_0000 != 0, cpu & 0x00ff_ffff) {
        (true, 7) => Arch::X64,
        (true, 12) => Arch::Arm64,
        _ => Arch::Other(cpu as u16),
    }
}

/// What `manifest.json` records, and what the host reads at runtime to build
/// its one and only `Command` for Pi.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct Manifest {
    /// Bumped when a field changes meaning, so an old installer is rejected
    /// instead of half-understood.
    pub schema: u32,
    pub target: String,
    pub platform: String,
    pub arch: String,
    pub node_version: String,
    pub node: String,
    pub pi_package: String,
    pub pi_version: String,
    pub cli: String,
    pub pi_ai: String,
    pub subagent_source: String,
    pub package_dir: String,
    pub licenses: Vec<String>,
    pub generated_at: String,
    /// Hashes of the two files whose corruption turns into a "cannot start pi"
    /// dialog, so a truncated install can be named instead of guessed at.
    pub sha256: std::collections::BTreeMap<String, String>,
}

pub const SCHEMA: u32 = 1;

impl Manifest {
    /// Whether two manifests describe the same tree.
    ///
    /// `generated_at` is a build stamp, not a property of the contents: a
    /// release job that re-checks its own staging must not fail because time
    /// passed between the two runs.
    #[must_use]
    pub fn matches_tree(&self, other: &Self) -> bool {
        let mut left = self.clone();
        let mut right = other.clone();
        left.generated_at.clear();
        right.generated_at.clear();
        left == right
    }
}

/// Relative paths for one target, all inside the staged tree.
pub struct Layout {
    pub node: PathBuf,
    pub node_license: PathBuf,
    pub npm_project: PathBuf,
    pub package_dir: PathBuf,
    pub cli: PathBuf,
    pub pi_ai: PathBuf,
    pub subagent: PathBuf,
}

impl Layout {
    #[must_use]
    pub fn for_target(target: &Target) -> Self {
        let package_dir = PathBuf::from("pi")
            .join("node_modules")
            .join("@earendil-works")
            .join("pi-coding-agent");
        // npm hoists when it can and nests on a version conflict, and which one
        // happens depends on the resolver's mood; both layouts are checked at
        // run time by the host, so the manifest records what is actually here.
        let flat_ai = PathBuf::from("pi")
            .join("node_modules")
            .join("@earendil-works")
            .join("pi-ai")
            .join(PI_AI_TAIL);
        let nested_ai = package_dir
            .join("node_modules")
            .join("@earendil-works")
            .join("pi-ai")
            .join(PI_AI_TAIL);
        let pi_ai = if nested_ai.is_file() && !flat_ai.is_file() {
            nested_ai
        } else {
            flat_ai
        };
        Self {
            node: if target.platform == "windows" {
                PathBuf::from("node").join("node.exe")
            } else {
                PathBuf::from("node").join("bin").join("node")
            },
            node_license: PathBuf::from("node").join("LICENSE"),
            npm_project: PathBuf::from("pi"),
            package_dir: package_dir.clone(),
            cli: package_dir.join(PI_CLI_TAIL),
            pi_ai,
            subagent: package_dir.join(SUBAGENT_TAIL),
        }
    }
}

/// Files the sub-agent example needs but does not copy itself: the extension
/// entry, its agent loader and the two resource directories it renders from.
const SUBAGENT_FILES: [&str; 2] = ["index.ts", "agents.ts"];
const SUBAGENT_DIRS: [&str; 2] = ["agents", "prompts"];

/// Check a staged (or shipped) tree, returning the manifest it should have.
///
/// Every failure is collected before returning so one run names all the
/// problems — a release job that reports one missing file per attempt is a
/// minute per fix on four platforms.
pub fn verify_tree(root: &Path, target: &Target) -> Result<Manifest, Vec<String>> {
    let layout = Layout::for_target(target);
    let mut problems: Vec<String> = Vec::new();

    if !root.is_dir() {
        return Err(vec![format!(
            "{RESOURCE_DIR}: {} does not exist",
            root.display()
        )]);
    }

    // 1. Node: present, the right architecture, and (on a matching host) the
    //    version the installer promises.
    let node = root.join(&layout.node);
    match binary_arch(&node) {
        Ok(arch) if arch.matches(target.arch) => {}
        Ok(arch) => problems.push(format!(
            "{}: built for {} but {} needs {}",
            layout.node.display(),
            arch.name(),
            target.id,
            target.arch
        )),
        Err(error) => problems.push(error),
    }
    if !root.join(&layout.node_license).is_file() {
        problems.push(format!(
            "{}: Node's LICENSE is missing",
            layout.node_license.display()
        ));
    }

    // 2. The npm project the tree was built from must be the pinned one, or the
    //    staged tree cannot be reproduced by rerunning this command.
    for name in ["package.json", "package-lock.json"] {
        let path = root.join(&layout.npm_project).join(name);
        if !path.is_file() {
            problems.push(format!("{}: missing", path.display()));
        }
    }
    let installed = read_pi_manifest(&root.join(&layout.package_dir));
    match &installed {
        Some((name, version, license)) => {
            if name != "@earendil-works/pi-coding-agent" {
                problems.push(format!("installed package is {name}, not pi-coding-agent"));
            }
            if let Some(expected) = pinned_pi_version() {
                if *version != expected {
                    problems.push(format!(
                        "installed pi is {version} but the lockfile pins {expected}"
                    ));
                }
            }
            if license.trim().is_empty() {
                problems.push("pi's package.json declares no licence".to_string());
            }
        }
        None => problems.push(format!(
            "{}: unreadable package.json — pi was not installed",
            layout.package_dir.display()
        )),
    }

    // 3. The CLI. This is the file the host executes; a tree without it is the
    //    "installed but cannot start" package the plan refuses to publish.
    if !root.join(&layout.cli).is_file() {
        problems.push(format!("{}: the pi CLI is missing", layout.cli.display()));
    }

    // 4. The OAuth module the sign-in helper imports.
    if !root.join(&layout.pi_ai).is_file() {
        problems.push(format!(
            "{}: the pi-ai provider bundle is missing (app sign-in cannot work)",
            layout.pi_ai.display()
        ));
    }

    // 5. The sub-agent extension the app manages.
    for name in SUBAGENT_FILES {
        let path = root.join(&layout.subagent).join(name);
        if !path.is_file() {
            problems.push(format!("{}/{name}: missing", layout.subagent.display()));
        }
    }
    for name in SUBAGENT_DIRS {
        let path = root.join(&layout.subagent).join(name);
        if !path.is_dir() {
            problems.push(format!("{}/{name}/: missing", layout.subagent.display()));
        }
    }

    if !problems.is_empty() {
        return Err(problems);
    }

    let (_, version, _) = installed.expect("checked above");
    let mut sha256 = std::collections::BTreeMap::new();
    for relative in [&layout.node, &layout.cli] {
        sha256.insert(
            path_string(relative),
            sha256_file(&root.join(relative)).map_err(|error| vec![error])?,
        );
    }
    let mut licenses = vec![path_string(&layout.node_license)];
    licenses.extend(pi_license_files(root, &layout.package_dir));

    Ok(Manifest {
        schema: SCHEMA,
        target: target.id.to_string(),
        platform: target.platform.to_string(),
        arch: target.arch.to_string(),
        node_version: NODE_VERSION.to_string(),
        node: path_string(&layout.node),
        pi_package: "@earendil-works/pi-coding-agent".to_string(),
        pi_version: version,
        cli: path_string(&layout.cli),
        pi_ai: path_string(&layout.pi_ai),
        subagent_source: path_string(&layout.subagent),
        package_dir: path_string(&layout.package_dir),
        licenses,
        generated_at: String::new(),
        sha256,
    })
}

fn read_pi_manifest(dir: &Path) -> Option<(String, String, String)> {
    let text = std::fs::read_to_string(dir.join("package.json")).ok()?;
    let value: serde_json::Value = serde_json::from_str(&text).ok()?;
    let name = value.get("name")?.as_str()?.to_string();
    let version = value.get("version")?.as_str()?.to_string();
    let license = value
        .get("license")
        .and_then(|value| value.as_str())
        .unwrap_or_default()
        .to_string();
    Some((name, version, license))
}

/// Licence files the package actually ships, relative to the bundle root.
///
/// npm publishes a package's root LICENSE/COPYING when it has one. Pi's tarball
/// does not (its repository licence lives outside the package directory), so
/// "no file" is a fact to record rather than a failure — the declared SPDX id in
/// `package.json` is what ships with the code.
fn pi_license_files(root: &Path, package_dir: &Path) -> Vec<String> {
    let Ok(entries) = std::fs::read_dir(root.join(package_dir)) else {
        return Vec::new();
    };
    let mut found: Vec<String> = entries
        .filter_map(|entry| entry.ok())
        .filter(|entry| entry.file_type().is_ok_and(|kind| kind.is_file()))
        .map(|entry| entry.file_name().to_string_lossy().into_owned())
        .filter(|name| {
            let upper = name.to_ascii_uppercase();
            upper.starts_with("LICENSE") || upper.starts_with("COPYING")
        })
        .map(|name| path_string(&package_dir.join(name)))
        .collect();
    found.sort();
    found
}

/// Version the lockfile pins, read from the checked-in npm project.
#[must_use]
pub fn pinned_pi_version() -> Option<String> {
    let root = repo_root().ok()?;
    let text = std::fs::read_to_string(root.join(NPM_PROJECT).join("package-lock.json")).ok()?;
    let value: serde_json::Value = serde_json::from_str(&text).ok()?;
    value
        .get("packages")?
        .get("node_modules/@earendil-works/pi-coding-agent")?
        .get("version")?
        .as_str()
        .map(str::to_string)
}

fn path_string(path: &Path) -> String {
    path.to_string_lossy().replace('\\', "/")
}

pub fn sha256_file(path: &Path) -> Result<String, String> {
    use std::io::Read;

    let mut file =
        std::fs::File::open(path).map_err(|error| format!("{}: {error}", path.display()))?;
    let mut hasher = Sha256::new();
    let mut buffer = vec![0u8; 1 << 20];
    loop {
        let read = file
            .read(&mut buffer)
            .map_err(|error| format!("{}: {error}", path.display()))?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
    }
    Ok(hex(&hasher.finalize()))
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn repo_root() -> Result<PathBuf, String> {
    // The binary lives at `<repo>/target/<profile>/anvil-devtools`; the manifest
    // dir is the one place cargo guarantees is inside the repo.
    let manifest = Path::new(env!("CARGO_MANIFEST_DIR"));
    manifest
        .ancestors()
        .nth(2)
        .map(Path::to_path_buf)
        .ok_or_else(|| "cannot find the repository root from tools/devtools".to_string())
}

/// Options for one `bundle-pi` run.
struct Options {
    target: String,
    out: PathBuf,
    cache: PathBuf,
    node_archive: Option<PathBuf>,
    check_only: bool,
}

fn parse(args: &[String]) -> Result<Options, String> {
    let mut target = String::new();
    let mut out = None;
    let mut cache = None;
    let mut node_archive = None;
    let mut check_only = false;
    for arg in args {
        let (key, value) = arg
            .split_once('=')
            .map_or((arg.as_str(), None), |(key, value)| (key, Some(value)));
        match key {
            "--target" => {
                target = value
                    .ok_or_else(|| "--target needs a value".to_string())?
                    .to_string()
            }
            "--out" => out = value.map(PathBuf::from),
            "--cache" => cache = value.map(PathBuf::from),
            "--node-archive" => node_archive = value.map(PathBuf::from),
            "--check-only" => check_only = true,
            other => return Err(format!("unknown argument {other:?}")),
        }
    }
    let repo = repo_root()?;
    Ok(Options {
        target,
        out: out.unwrap_or_else(|| repo.join(DEFAULT_OUT)),
        cache: cache.unwrap_or_else(|| repo.join(".tmp").join("bundle-cache")),
        node_archive,
        check_only,
    })
}

/// Entry point. Returns the process exit code.
#[must_use]
pub fn run(args: &[String]) -> i32 {
    let options = match parse(args) {
        Ok(options) => options,
        Err(error) => {
            eprintln!("bundle-pi: {error}");
            return EXIT_INCOMPLETE;
        }
    };
    let Some(target) = target(&options.target) else {
        eprintln!(
            "bundle-pi: unknown target {:?} (known: {})",
            options.target,
            TARGETS
                .iter()
                .map(|entry| entry.id)
                .collect::<Vec<_>>()
                .join(", ")
        );
        return EXIT_INCOMPLETE;
    };

    if options.check_only {
        return match verify_tree(&options.out, target) {
            Ok(manifest) => match read_manifest(&options.out) {
                Some(on_disk) if on_disk.matches_tree(&manifest) => {
                    println!(
                        "bundle-pi check: {} is complete ({}, pi {})",
                        options.out.display(),
                        manifest.target,
                        manifest.pi_version
                    );
                    EXIT_PASS
                }
                Some(on_disk) => {
                    eprintln!(
                        "bundle-pi: manifest.json does not match the tree (on disk pi {}, expected pi {})",
                        on_disk.pi_version, manifest.pi_version
                    );
                    EXIT_ASSERTION
                }
                None => {
                    eprintln!(
                        "bundle-pi: manifest.json is missing from {}",
                        options.out.display()
                    );
                    EXIT_ASSERTION
                }
            },
            Err(problems) => {
                for problem in problems {
                    eprintln!("bundle-pi: {problem}");
                }
                EXIT_ASSERTION
            }
        };
    }

    match prepare(&options, target) {
        Ok(manifest) => {
            println!(
                "bundle-pi: staged {} for {} (node {}, pi {})",
                options.out.display(),
                manifest.target,
                manifest.node_version,
                manifest.pi_version
            );
            for (file, hash) in &manifest.sha256 {
                println!("  {hash}  {file}");
            }
            EXIT_PASS
        }
        Err(error) => {
            eprintln!("bundle-pi: {error}");
            EXIT_ASSERTION
        }
    }
}

fn prepare(options: &Options, target: &Target) -> Result<Manifest, String> {
    let repo = repo_root()?;
    let out = &options.out;
    // Start from an empty tree: a stale file from a previous target would ship
    // in the installer and be indistinguishable from a real one.
    if out.exists() {
        std::fs::remove_dir_all(out)
            .map_err(|error| format!("clean {}: {error}", out.display()))?;
    }
    std::fs::create_dir_all(out).map_err(|error| format!("create {}: {error}", out.display()))?;

    stage_node(options, target, out)?;
    stage_npm_project(&repo, out)?;
    run_npm_ci(out)?;

    let mut manifest = verify_tree(out, target).map_err(|problems| {
        format!(
            "the staged tree is incomplete:\n  {}",
            problems.join("\n  ")
        )
    })?;
    manifest.generated_at = now_rfc3339();
    write_manifest(out, &manifest)?;
    // Re-verify with the manifest present: `--check-only` in the release job has
    // to agree with what this run just claimed.
    verify_tree(out, target).map_err(|problems| {
        format!(
            "manifest written, but the tree re-check failed:\n  {}",
            problems.join("\n  ")
        )
    })?;
    Ok(manifest)
}

/// Fetch (or accept) the pinned Node archive and stage the interpreter only.
fn stage_node(options: &Options, target: &Target, out: &Path) -> Result<(), String> {
    let archive = match &options.node_archive {
        Some(path) => path.clone(),
        None => {
            let path = options.cache.join(target.archive);
            if path.is_file() {
                let actual = sha256_file(&path)?;
                if actual != target.sha256 {
                    // A cache hit that does not match is a corrupted download,
                    // not a reason to fail the release: fetch it again.
                    eprintln!(
                        "bundle-pi: cached {} hashes to {actual}, refetching",
                        path.display()
                    );
                    let _ = std::fs::remove_file(&path);
                }
            }
            if !path.is_file() {
                std::fs::create_dir_all(&options.cache)
                    .map_err(|error| format!("create {}: {error}", options.cache.display()))?;
                download(target, &path)?;
            }
            path
        }
    };
    let actual = sha256_file(&archive)?;
    if actual != target.sha256 {
        return Err(format!(
            "{}: SHA-256 is {actual}, but {} pins {}",
            archive.display(),
            target.id,
            target.sha256
        ));
    }

    let scratch = out.join(".unpack");
    let _ = std::fs::remove_dir_all(&scratch);
    std::fs::create_dir_all(&scratch)
        .map_err(|error| format!("create {}: {error}", scratch.display()))?;
    let extracted = run_tool(
        "tar",
        &[
            "-xf".to_string(),
            archive.display().to_string(),
            "-C".to_string(),
            scratch.display().to_string(),
        ],
    )?;
    if extracted.code != Some(0) {
        return Err(format!(
            "extracting {} failed (exit {:?}): {}",
            archive.display(),
            extracted.code,
            extracted.stderr.trim()
        ));
    }
    // The archive unpacks into a single `node-v<version>-<platform>-<arch>/`.
    let root = std::fs::read_dir(&scratch)
        .map_err(|error| error.to_string())?
        .filter_map(|entry| entry.ok())
        .map(|entry| entry.path())
        .find(|path| path.is_dir())
        .ok_or_else(|| format!("{} contained no directory", archive.display()))?;

    let layout = Layout::for_target(target);
    let source = if target.platform == "windows" {
        root.join("node.exe")
    } else {
        root.join("bin").join("node")
    };
    if !source.is_file() {
        return Err(format!(
            "{} does not contain {}",
            archive.display(),
            source.display()
        ));
    }
    let destination = out.join(&layout.node);
    if let Some(parent) = destination.parent() {
        std::fs::create_dir_all(parent).map_err(|error| error.to_string())?;
    }
    std::fs::copy(&source, &destination)
        .map_err(|error| format!("copy {}: {error}", destination.display()))?;
    // The licence sits beside the staged `node/` root, not beside the binary:
    // on Windows both are the same directory, on Unix the binary is in `bin/`.
    let license = out.join(&layout.node_license);
    std::fs::copy(root.join("LICENSE"), &license)
        .map_err(|error| format!("copy Node LICENSE: {error}"))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mut permissions = std::fs::metadata(&destination)
            .map_err(|error| error.to_string())?
            .permissions();
        permissions.set_mode(0o755);
        std::fs::set_permissions(&destination, permissions).map_err(|error| error.to_string())?;
    }
    let _ = std::fs::remove_dir_all(&scratch);
    Ok(())
}

fn download(target: &Target, destination: &Path) -> Result<(), String> {
    // curl rather than a TLS stack in the tool: the archive is verified against
    // a pinned hash either way, and `--fail` keeps an HTML error page from
    // being written where a .zip is expected.
    let url = format!("https://nodejs.org/dist/v{NODE_VERSION}/{}", target.archive);
    println!("bundle-pi: downloading {url}");
    let output = run_tool(
        "curl",
        &[
            "--fail".to_string(),
            "--location".to_string(),
            "--silent".to_string(),
            "--show-error".to_string(),
            "--retry".to_string(),
            "3".to_string(),
            "--output".to_string(),
            destination.display().to_string(),
            url,
        ],
    )?;
    if output.code != Some(0) {
        return Err(format!(
            "downloading {} failed (exit {:?}): {}",
            target.archive,
            output.code,
            output.stderr.trim()
        ));
    }
    Ok(())
}

fn stage_npm_project(repo: &Path, out: &Path) -> Result<(), String> {
    let source = repo.join(NPM_PROJECT);
    let destination = out.join("pi");
    std::fs::create_dir_all(&destination).map_err(|error| error.to_string())?;
    for name in ["package.json", "package-lock.json"] {
        let from = source.join(name);
        if !from.is_file() {
            return Err(format!("{}: missing dependency input", from.display()));
        }
        std::fs::copy(&from, destination.join(name))
            .map_err(|error| format!("copy {name}: {error}"))?;
    }
    Ok(())
}

fn run_npm_ci(out: &Path) -> Result<(), String> {
    let npm = which_npm().ok_or_else(|| {
        "npm was not found on PATH; the bundle is produced from a lockfile, so npm is required"
            .to_string()
    })?;
    let project = out.join("pi");
    let cache = repo_root()?.join(".tmp").join("npm-cache");
    let temp = repo_root()?.join(".tmp").join("temp");
    std::fs::create_dir_all(&cache).map_err(|error| error.to_string())?;
    std::fs::create_dir_all(&temp).map_err(|error| error.to_string())?;

    // npm is a Node script: its own directory has to come first so the sibling
    // Node runs it, exactly as the runtime installer does on this machine.
    let env = child_env_with(npm.parent(), &cache, &temp);
    println!("bundle-pi: npm ci in {}", project.display());
    let output = std::process::Command::new(&npm)
        .current_dir(&project)
        .args([
            "ci",
            "--omit=dev",
            "--ignore-scripts",
            "--no-audit",
            "--no-fund",
            "--loglevel=error",
        ])
        .envs(env)
        .output()
        .map_err(|error| format!("running {}: {error}", npm.display()))?;
    if output.status.code() != Some(0) {
        return Err(format!(
            "npm ci failed (exit {:?}):\n{}{}",
            output.status.code(),
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        ));
    }
    Ok(())
}

fn which_npm() -> Option<PathBuf> {
    let names: &[&str] = if cfg!(windows) {
        &["npm.cmd", "npm.exe"]
    } else {
        &["npm"]
    };
    let path = std::env::var_os("PATH")?;
    for dir in std::env::split_paths(&path) {
        for name in names {
            let candidate = dir.join(name);
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    None
}

fn child_env_with(npm_dir: Option<&Path>, cache: &Path, temp: &Path) -> Vec<(String, String)> {
    let mut env: Vec<(String, String)> = std::env::vars()
        .filter(|(key, _)| {
            // A preset npm cache or temp dir would put the build on C:.
            !key.eq_ignore_ascii_case("npm_config_cache")
                && !key.eq_ignore_ascii_case("TEMP")
                && !key.eq_ignore_ascii_case("TMP")
                && !key.eq_ignore_ascii_case("TMPDIR")
        })
        .collect();
    env.push(("npm_config_cache".to_string(), cache.display().to_string()));
    for key in ["TEMP", "TMP", "TMPDIR"] {
        env.push((key.to_string(), temp.display().to_string()));
    }
    if let Some(dir) = npm_dir {
        let mut dirs = vec![dir.to_path_buf()];
        if let Some(existing) = std::env::var_os("PATH") {
            dirs.extend(std::env::split_paths(&existing));
        }
        if let Ok(joined) = std::env::join_paths(dirs) {
            env.push(("PATH".to_string(), joined.to_string_lossy().into_owned()));
        }
    }
    env
}

struct ToolOutput {
    code: Option<i32>,
    stderr: String,
}

fn run_tool(program: &str, args: &[String]) -> Result<ToolOutput, String> {
    let output = Command::new(program)
        .args(args)
        .output()
        .map_err(|error| format!("running {program}: {error}"))?;
    Ok(ToolOutput {
        code: output.status.code(),
        stderr: String::from_utf8_lossy(&output.stderr).into_owned(),
    })
}

pub fn manifest_path(root: &Path) -> PathBuf {
    root.join("manifest.json")
}

pub fn read_manifest(root: &Path) -> Option<Manifest> {
    let text = std::fs::read_to_string(manifest_path(root)).ok()?;
    serde_json::from_str(&text).ok()
}

fn write_manifest(root: &Path, manifest: &Manifest) -> Result<(), String> {
    let text = serde_json::to_string_pretty(manifest).map_err(|error| error.to_string())?;
    std::fs::write(manifest_path(root), format!("{text}\n"))
        .map_err(|error| format!("write manifest.json: {error}"))
}

fn now_rfc3339() -> String {
    // No chrono in this crate; the release log only needs a stable stamp, and
    // the file's own mtime carries the same information. A fixed format keeps
    // the manifest diffable between builds.
    match std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH) {
        Ok(elapsed) => format!("unix:{}", elapsed.as_secs()),
        Err(_) => "unix:0".to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture(name: &str) -> PathBuf {
        let dir = repo_root()
            .expect("repo")
            .join(".tmp")
            .join("test")
            .join("bundle-pi")
            .join(name);
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create fixture");
        dir
    }

    /// A convincing-enough PE header for the architecture under test.
    fn write_pe(path: &Path, machine: u16) {
        let mut bytes = vec![0u8; 0x100];
        bytes[0..2].copy_from_slice(b"MZ");
        bytes[0x3c..0x40].copy_from_slice(&0x80u32.to_le_bytes());
        bytes[0x80..0x84].copy_from_slice(b"PE\0\0");
        bytes[0x84..0x86].copy_from_slice(&machine.to_le_bytes());
        write_file(path, &bytes);
    }

    fn write_elf(path: &Path, machine: u16) {
        let mut bytes = vec![0u8; 0x40];
        bytes[0..4].copy_from_slice(b"\x7fELF");
        bytes[5] = 1;
        bytes[18..20].copy_from_slice(&machine.to_le_bytes());
        write_file(path, &bytes);
    }

    fn write_file(path: &Path, bytes: &[u8]) {
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).expect("mkdir");
        }
        std::fs::write(path, bytes).expect("write file");
    }

    /// A tree that would pass, so each test can break exactly one thing.
    fn complete_tree(name: &str, target: &Target) -> PathBuf {
        let root = fixture(name);
        let layout = Layout::for_target(target);
        if target.platform == "windows" {
            write_pe(&root.join(&layout.node), 0x8664);
        } else {
            write_elf(&root.join(&layout.node), 0x3e);
        }
        write_file(&root.join(&layout.node_license), b"MIT");
        write_file(&root.join(&layout.npm_project).join("package.json"), b"{}");
        write_file(
            &root.join(&layout.npm_project).join("package-lock.json"),
            b"{}",
        );
        write_file(
            &root.join(&layout.package_dir).join("package.json"),
            br#"{"name":"@earendil-works/pi-coding-agent","version":"1.6.0","license":"MIT"}"#,
        );
        write_file(&root.join(&layout.cli), b"// cli");
        write_file(&root.join(&layout.pi_ai), b"export {}");
        for name in SUBAGENT_FILES {
            write_file(&root.join(&layout.subagent).join(name), b"//");
        }
        for name in SUBAGENT_DIRS {
            std::fs::create_dir_all(root.join(&layout.subagent).join(name)).expect("subagent dir");
        }
        root
    }

    fn win() -> &'static Target {
        target("win-x64").expect("win-x64 is a supported target")
    }

    #[test]
    fn every_release_target_has_a_pinned_archive_hash() {
        assert_eq!(TARGETS.len(), 6);
        for entry in &TARGETS {
            assert_eq!(entry.sha256.len(), 64, "{}", entry.id);
            assert!(entry.archive.contains(NODE_VERSION), "{}", entry.id);
            assert!(target(entry.id).is_some());
        }
        assert!(target("solaris-sparc").is_none());
    }

    #[test]
    fn a_complete_tree_passes_and_records_paths_and_hashes() {
        let root = complete_tree("complete", win());
        let manifest = verify_tree(&root, win()).expect("complete tree must pass");
        assert_eq!(manifest.target, "win-x64");
        assert_eq!(manifest.node_version, NODE_VERSION);
        assert_eq!(manifest.pi_version, "1.6.0");
        assert_eq!(
            manifest.cli,
            "pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js"
        );
        assert_eq!(manifest.sha256.len(), 2);
        assert!(manifest.sha256.contains_key("node/node.exe"));
        assert!(manifest.licenses.contains(&"node/LICENSE".to_string()));
    }

    /// The failure this check exists for: a Node for the wrong architecture
    /// installs cleanly and then cannot start anything on the user's machine.
    #[test]
    fn a_node_for_the_wrong_architecture_is_rejected() {
        let root = complete_tree("wrong-arch", win());
        write_pe(
            &root.join(Layout::for_target(win()).node),
            0xaa64, // arm64 binary in an x64 bundle
        );
        let problems = verify_tree(&root, win()).expect_err("wrong arch must fail");
        assert!(
            problems
                .iter()
                .any(|problem| problem.contains("arm64") && problem.contains("x64")),
            "{problems:?}"
        );
    }

    #[test]
    fn a_missing_cli_is_rejected() {
        let root = complete_tree("missing-cli", win());
        std::fs::remove_file(root.join(Layout::for_target(win()).cli)).expect("remove cli");
        let problems = verify_tree(&root, win()).expect_err("missing cli must fail");
        assert!(
            problems
                .iter()
                .any(|problem| problem.contains("pi CLI is missing")),
            "{problems:?}"
        );
    }

    #[test]
    fn a_missing_subagent_example_is_rejected() {
        let root = complete_tree("missing-subagent", win());
        let layout = Layout::for_target(win());
        std::fs::remove_dir_all(root.join(&layout.subagent).join("agents")).expect("remove agents");
        let problems = verify_tree(&root, win()).expect_err("missing example must fail");
        assert!(
            problems.iter().any(|problem| problem.contains("agents/")),
            "{problems:?}"
        );
    }

    #[test]
    fn a_pi_package_at_the_wrong_version_is_rejected() {
        let root = complete_tree("wrong-version", win());
        let layout = Layout::for_target(win());
        std::fs::write(
            root.join(&layout.package_dir).join("package.json"),
            r#"{"name":"@earendil-works/pi-coding-agent","version":"1.4.0","license":"MIT"}"#,
        )
        .expect("rewrite");
        let problems = verify_tree(&root, win()).expect_err("stale pi must fail");
        assert!(
            problems
                .iter()
                .any(|problem| problem.contains("lockfile pins")),
            "{problems:?}"
        );
    }

    #[test]
    fn headers_are_read_for_all_three_formats() {
        let root = fixture("headers");
        let pe = root.join("a.exe");
        write_pe(&pe, 0x8664);
        assert_eq!(binary_arch(&pe), Ok(Arch::X64));
        let elf = root.join("b");
        write_elf(&elf, 0xb7);
        assert_eq!(binary_arch(&elf), Ok(Arch::Arm64));
        let macho = root.join("c");
        let mut bytes = vec![0u8; 32];
        // 64-bit little-endian Mach-O (`MH_MAGIC_64` byte order) + arm64 cputype.
        bytes[0..4].copy_from_slice(&[0xcf, 0xfa, 0xed, 0xfe]);
        bytes[4..8].copy_from_slice(&0x0100_000cu32.to_le_bytes());
        std::fs::write(&macho, bytes).expect("write macho");
        assert_eq!(binary_arch(&macho), Ok(Arch::Arm64));
        let text = root.join("d");
        std::fs::write(&text, "not a binary").expect("write text");
        assert!(binary_arch(&text).is_err());
    }

    #[test]
    fn the_pinned_version_comes_from_the_lockfile() {
        let pinned = pinned_pi_version().expect("lockfile must pin pi");
        assert_eq!(pinned, "1.6.0");
    }

    /// A release job re-checks the tree it just staged. The build stamp moves
    /// between the two runs, so it must not count as a difference.
    #[test]
    fn the_build_stamp_is_not_a_difference() {
        let root = complete_tree("stamp", win());
        let mut manifest = verify_tree(&root, win()).expect("complete tree must pass");
        manifest.generated_at = "unix:1".to_string();
        write_manifest(&root, &manifest).expect("write manifest");
        let reread = read_manifest(&root).expect("manifest must parse");
        let fresh = verify_tree(&root, win()).expect("still complete");
        assert!(reread.matches_tree(&fresh), "{reread:?} vs {fresh:?}");
        let mut changed = reread.clone();
        changed.pi_version = "1.5.0".to_string();
        assert!(!changed.matches_tree(&fresh));
    }
}
