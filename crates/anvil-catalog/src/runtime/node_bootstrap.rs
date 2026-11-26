//! A Node runtime for a machine that has none.
//!
//! Every runtime this build installs is an npm package, and npm is a Node
//! script. On a machine with no Node the one-click install therefore stopped
//! before it started, with "未找到 npm：请先安装 Node.js" — a dead end for
//! someone who just unboxed the app.
//!
//! So the install prepares its own interpreter, in the same prefix the shims
//! live in:
//!
//! * read `SHASUMS256.txt` from the official distribution server — it names the
//!   exact archive for this platform *and* carries its hash, so the version and
//!   the checksum always agree;
//! * download the archive with the operating system's `curl`, the same way
//!   model verification talks to a gateway, so the host needs no TLS stack;
//! * refuse to unpack anything whose digest differs from the manifest;
//! * flatten the archive's single top-level folder into the prefix, which is
//!   where `shared_npm` and the adapters already look (`npm.cmd` beside
//!   `node.exe` on Windows, `bin/npm` on macOS and Linux).
//!
//! Nothing here runs a shell, and nothing is installed system-wide: the whole
//! tree lands under the user's own prefix, exactly like the runtime itself.

use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use sha2::{Digest, Sha256};

use crate::error::{Error, Result};
use crate::runtime::process;

/// Where the official distribution lives.
const DIST: &str = "https://nodejs.org/dist";

/// The LTS line this build prepares. Overridable so a mirror or a pinned
/// version can be used without a rebuild.
const LINE_ENV: &str = "ANVIL_NODE_LINE";
const DEFAULT_LINE: &str = "latest-v22.x";

/// A manifest read is quick; a hung network must not hang the install.
const MANIFEST_TIMEOUT: Duration = Duration::from_secs(30);
/// A download of tens of megabytes over a slow link still needs a ceiling.
const DOWNLOAD_TIMEOUT: Duration = Duration::from_secs(600);

/// Where the work happens inside the prefix, so a failed attempt leaves nothing
/// that discovery would mistake for a runtime.
const WORK_DIR: &str = ".bootstrap";

/// What a successful bootstrap produced.
#[derive(Clone, Debug)]
pub struct NodeRuntime {
    pub version: String,
    pub node: PathBuf,
    pub npm: PathBuf,
}

/// `<os>-<arch>` as the distribution names it, and the archive extension.
///
/// Separate from the download so the naming can be checked without a network:
/// guessing this wrong downloads a 404.
pub fn target_triple(os: &str, arch: &str) -> Result<(&'static str, &'static str)> {
    let triple = match (os, arch) {
        ("windows", "x86_64") => ("win-x64", "zip"),
        ("windows", "aarch64") => ("win-arm64", "zip"),
        ("macos", "x86_64") => ("osx-x64", "tar.gz"),
        ("macos", "aarch64") => ("osx-arm64", "tar.gz"),
        ("linux", "x86_64") => ("linux-x64", "tar.gz"),
        ("linux", "aarch64") => ("linux-arm64", "tar.gz"),
        _ => {
            return Err(Error::Rejected(format!(
                "没有为 {os}/{arch} 准备的 Node 发行包；请手动安装 Node.js 后重试"
            )));
        }
    };
    Ok(triple)
}

/// One line of `SHASUMS256.txt`: `<sha256>  node-v<version>-<target>.<ext>`.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Archive {
    version: String,
    file: String,
    sha256: String,
}

/// The line naming `target`'s archive, if the manifest lists it.
fn pick_archive(manifest: &str, target: &str, extension: &str) -> Option<Archive> {
    let suffix = format!("-{target}.{extension}");
    for line in manifest.lines() {
        let mut parts = line.split_whitespace();
        let (Some(sha256), Some(file)) = (parts.next(), parts.next()) else {
            continue;
        };
        if !file.ends_with(&suffix) || !file.starts_with("node-v") {
            continue;
        }
        let version = file
            .trim_start_matches("node-v")
            .strip_suffix(&suffix)?
            .to_string();
        return Some(Archive {
            version,
            file: file.to_string(),
            sha256: sha256.to_lowercase(),
        });
    }
    None
}

/// Fetch or verify a Node runtime inside `prefix`, reporting progress as lines.
///
/// The caller has already established that no usable `npm` exists; this returns
/// the interpreter it put there, ready to run the package install.
pub fn ensure(prefix: &Path, log: &mut dyn FnMut(&str)) -> Result<NodeRuntime> {
    let (target, extension) = target_triple(std::env::consts::OS, std::env::consts::ARCH)?;
    let line = std::env::var(LINE_ENV)
        .ok()
        .filter(|value| !value.trim().is_empty())
        .unwrap_or_else(|| DEFAULT_LINE.to_string());
    let curl = process::which("curl")
        .ok_or_else(|| Error::Rejected("找不到 curl，无法下载 Node；请手动安装 Node.js".into()))?;

    let manifest_url = format!("{DIST}/{line}/SHASUMS256.txt");
    log(&format!("$ curl {manifest_url}"));
    let manifest = curl_capture(&curl, &manifest_url, MANIFEST_TIMEOUT)?;
    let archive = pick_archive(&manifest, target, extension).ok_or_else(|| {
        Error::Rejected(format!(
            "Node 发行清单里没有 {target}.{extension}，请手动安装 Node.js 后重试"
        ))
    })?;
    log(&format!(
        "将在当前用户目录准备 Node v{}（{target}），不改系统 PATH",
        archive.version
    ));

    let work = prefix.join(WORK_DIR);
    // A previous attempt may have died mid-way; its leftovers are never reused.
    let _ = std::fs::remove_dir_all(&work);
    std::fs::create_dir_all(&work).map_err(|error| {
        Error::Io(format!("创建临时目录 {} 失败：{error}", work.display()))
    })?;

    let archive_path = work.join(&archive.file);
    let url = format!("{DIST}/v{}/{}", archive.version, archive.file);
    let started = Instant::now();
    curl_download(&curl, &url, &archive_path)?;
    let bytes = std::fs::metadata(&archive_path)
        .map(|metadata| metadata.len())
        .unwrap_or(0);
    log(&format!(
        "  已下载 {:.1} MB（{}s），正在校验",
        bytes as f64 / 1_048_576.0,
        started.elapsed().as_secs()
    ));

    let digest = sha256_file(&archive_path)?;
    if digest != archive.sha256 {
        let _ = std::fs::remove_dir_all(&work);
        return Err(Error::Rejected(format!(
            "Node 归档校验失败（期望 {}，实际 {digest}）；已删除下载内容",
            archive.sha256
        )));
    }
    log("SHA-256 校验通过");

    let unpacked = work.join("unpacked");
    std::fs::create_dir_all(&unpacked)
        .map_err(|error| Error::Io(format!("创建解压目录失败：{error}")))?;
    extract(&archive_path, &unpacked)?;

    // The archive has one top-level folder; its contents are what belong in the
    // prefix. Unpacking straight into the prefix would also work, but then a
    // half-written archive would be visible to the next scan.
    let root = single_directory(&unpacked)?;
    move_entries(&root, prefix)?;
    let _ = std::fs::remove_dir_all(&work);

    let node = if cfg!(windows) {
        prefix.join("node.exe")
    } else {
        prefix.join("bin").join("node")
    };
    let npm = if cfg!(windows) {
        prefix.join("npm.cmd")
    } else {
        prefix.join("bin").join("npm")
    };
    if !node.is_file() || !npm.is_file() {
        return Err(Error::Rejected(format!(
            "解压后没有找到 node/npm（{}）；请手动安装 Node.js 后重试",
            prefix.display()
        )));
    }
    // A `.tar.gz` carries the mode bits, but a mode that did not survive the
    // copy would turn `node` into a file the shell refuses to run — and the
    // failure would read as "Node 无法运行". Set it explicitly.
    #[cfg(not(windows))]
    for executable in [&node, &npm] {
        use std::os::unix::fs::PermissionsExt;
        let mut permissions = std::fs::metadata(executable)
            .map_err(|error| Error::Io(format!("读取 {} 失败：{error}", executable.display())))?
            .permissions();
        permissions.set_mode(0o755);
        std::fs::set_permissions(executable, permissions)
            .map_err(|error| Error::Io(format!("设置 {} 可执行失败：{error}", executable.display())))?;
    }

    // The archive could still be one this machine cannot execute (wrong arch,
    // blocked directory); asking it for its version is the same check the
    // runtime probe does.
    let output = process::run(&node, &["--version"], Duration::from_secs(10))?;
    if !output.success() {
        return Err(Error::Rejected(format!(
            "Node 已解压到 {} 但无法运行：{}",
            node.display(),
            output.text().lines().next().unwrap_or("没有输出")
        )));
    }
    log(&format!("Node 就绪：{}", output.text()));

    Ok(NodeRuntime {
        version: archive.version,
        node,
        npm,
    })
}

/// `curl` as a text fetch: small bodies only.
fn curl_capture(curl: &Path, url: &str, timeout: Duration) -> Result<String> {
    let max_time = timeout.as_secs().to_string();
    let output = process::run(
        curl,
        &["-sSL", "--fail", "--max-time", &max_time, url],
        timeout + Duration::from_secs(5),
    )?;
    if !output.success() {
        return Err(Error::Rejected(format!(
            "读取 {url} 失败：{}",
            output.text().lines().next().unwrap_or("curl 没有返回内容")
        )));
    }
    Ok(output.stdout)
}

/// `curl` writing the body to `path`.
fn curl_download(curl: &Path, url: &str, path: &Path) -> Result<()> {
    let destination = path.to_string_lossy().into_owned();
    let max_time = DOWNLOAD_TIMEOUT.as_secs().to_string();
    let output = process::run(
        curl,
        &[
            "-sSL",
            "--fail",
            "--retry",
            "2",
            "--retry-delay",
            "1",
            "--max-time",
            &max_time,
            "-o",
            &destination,
            url,
        ],
        DOWNLOAD_TIMEOUT + Duration::from_secs(20),
    )?;
    if !output.success() || !path.is_file() {
        let _ = std::fs::remove_file(path);
        return Err(Error::Rejected(format!(
            "下载 {url} 失败：{}",
            output.text().lines().next().unwrap_or("curl 没有返回内容")
        )));
    }
    Ok(())
}

fn sha256_file(path: &Path) -> Result<String> {
    let mut file = std::fs::File::open(path)
        .map_err(|error| Error::Io(format!("读取 {} 失败：{error}", path.display())))?;
    let mut hasher = Sha256::new();
    let mut buffer = vec![0u8; 256 * 1024];
    loop {
        let read = file
            .read(&mut buffer)
            .map_err(|error| Error::Io(format!("读取 {} 失败：{error}", path.display())))?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
    }
    Ok(format!("{:x}", hasher.finalize()))
}

#[cfg(windows)]
fn extract(archive: &Path, into: &Path) -> Result<()> {
    let file = std::fs::File::open(archive)
        .map_err(|error| Error::Io(format!("打开 {} 失败：{error}", archive.display())))?;
    let mut zip = zip::ZipArchive::new(file)
        .map_err(|error| Error::Rejected(format!("读取 Node 归档失败：{error}")))?;
    zip.extract(into)
        .map_err(|error| Error::Rejected(format!("解压 Node 归档失败：{error}")))
}

#[cfg(not(windows))]
fn extract(archive: &Path, into: &Path) -> Result<()> {
    let file = std::fs::File::open(archive)
        .map_err(|error| Error::Io(format!("打开 {} 失败：{error}", archive.display())))?;
    let mut tar = tar::Archive::new(flate2::read::GzDecoder::new(file));
    tar.unpack(into)
        .map_err(|error| Error::Rejected(format!("解压 Node 归档失败：{error}")))
}

/// The archive's single top-level folder, which holds the runtime.
fn single_directory(root: &Path) -> Result<PathBuf> {
    let mut directories = std::fs::read_dir(root)
        .map_err(|error| Error::Io(format!("读取 {} 失败：{error}", root.display())))?
        .flatten()
        .filter(|entry| entry.path().is_dir())
        .map(|entry| entry.path());
    match (directories.next(), directories.next()) {
        (Some(only), None) => Ok(only),
        _ => Err(Error::Rejected(format!(
            "Node 归档的结构不符合预期：{} 下应该只有一个目录",
            root.display()
        ))),
    }
}

/// Move every entry of `from` into `to`, replacing what is already there.
fn move_entries(from: &Path, to: &Path) -> Result<()> {
    let entries = std::fs::read_dir(from)
        .map_err(|error| Error::Io(format!("读取 {} 失败：{error}", from.display())))?;
    for entry in entries.flatten() {
        let source = entry.path();
        let target = to.join(entry.file_name());
        if target.is_dir() {
            std::fs::remove_dir_all(&target).map_err(|error| {
                Error::Io(format!("替换 {} 失败：{error}", target.display()))
            })?;
        } else if target.exists() {
            std::fs::remove_file(&target).map_err(|error| {
                Error::Io(format!("替换 {} 失败：{error}", target.display()))
            })?;
        }
        std::fs::rename(&source, &target).map_err(|error| {
            Error::Io(format!(
                "移动 {} 到 {} 失败：{error}",
                source.display(),
                target.display()
            ))
        })?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A wrong triple downloads a 404, so the names are pinned by a test.
    #[test]
    fn target_names_match_the_distribution() {
        assert_eq!(
            target_triple("windows", "x86_64").expect("windows x64"),
            ("win-x64", "zip")
        );
        assert_eq!(
            target_triple("windows", "aarch64").expect("windows arm64"),
            ("win-arm64", "zip")
        );
        assert_eq!(
            target_triple("macos", "aarch64").expect("mac arm64"),
            ("osx-arm64", "tar.gz")
        );
        assert_eq!(
            target_triple("linux", "x86_64").expect("linux x64"),
            ("linux-x64", "tar.gz")
        );
        assert!(target_triple("freebsd", "x86_64").is_err());
    }

    /// The version and the hash come from the same line, which is the whole
    /// point of reading the manifest instead of hard-coding a URL.
    #[test]
    fn the_manifest_names_the_archive_and_its_hash() {
        let manifest = "\
8bd3dc69eee8f3f226315a80d7d1d4faccf217a4c8fae500168ac2e38f3bdba3  node-v22.23.5-linux-arm64.tar.gz
4a191082e9706866830379d577d2cc8a50c776fcd9436a8d061b063ed0d9a0e3  node-v22.23.5-win-x64.zip
";
        let picked = pick_archive(manifest, "win-x64", "zip").expect("win archive");
        assert_eq!(picked.version, "22.23.5");
        assert_eq!(picked.file, "node-v22.23.5-win-x64.zip");
        assert_eq!(
            picked.sha256,
            "4a191082e9706866830379d577d2cc8a50c776fcd9436a8d061b063ed0d9a0e3"
        );
        assert!(pick_archive(manifest, "osx-arm64", "tar.gz").is_none());
        // `win-x64.zip` must not match `win-arm64.zip`.
        assert!(pick_archive(manifest, "win-arm64", "zip").is_none());
    }
}
