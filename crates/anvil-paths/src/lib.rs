//! D-drive write-path contract for Anvil on Windows.
//!
//! Anvil only writes to the `D:` volume on Windows. The rule comes from
//! `docs/superpowers/specs/2026-11-09-anvil-rebuild-execution-standard.md`
//! section 4: every writable location (application data, WebView2 user data,
//! SQLite, logs, build/target directories, temp files, screenshots and
//! evidence) must be verified on the **actual** volume *after* resolving
//! relative components, environment variables, symlinks and junctions. When a
//! directory is missing and creation is allowed, it is created on `D:`.
//!
//! Two properties are non-negotiable, and every branch of this crate preserves
//! them:
//!
//! 1. **No fallback.** A rejected path never turns into a different writable
//!    directory. There is no default location, no retry on another drive and no
//!    environment-variable escape hatch.
//! 2. **Real volume, not string prefix.** `D:\link` pointing at `C:\Windows` is
//!    rejected even though its text starts with `D:`. The check opens the
//!    nearest existing ancestor and asks Windows for its final path
//!    (`GetFinalPathNameByHandleW`), so reparse points are always followed.
//!
//! Missing paths are handled explicitly:
//!
//! * `validate_d_path(path, false)`: an existing directory is volume-checked and
//!   write-probed; a missing path fails with [`PathError::Missing`] and nothing
//!   is created.
//! * `validate_d_path(path, true)`: the path is created (including missing
//!   parents), re-resolved against its real volume and write-probed.
//!
//! UNC shares (`\\host\share`) and device/volume-GUID namespaces (`\\?\Volume{…}`)
//! are reported as [`PathError::OutsideD`] with the share or namespace as the
//! volume, because they are not the local `D:` volume.
//!
//! The crate is Windows-only: macOS/Linux write roots have not been decided yet,
//! so building elsewhere fails instead of silently accepting a path.

#[cfg(not(windows))]
compile_error!(
    "crates/anvil-paths implements the Windows D:-only write rule. The macOS/Linux write roots \
     are not decided yet (docs/superpowers/specs/2026-11-10-anvil-rust-tauri-design.md), so this \
     crate refuses to build rather than silently accepting a non-D: root."
);

#[cfg(windows)]
mod win;

use std::ffi::OsString;
use std::fmt;
use std::io;
use std::path::{Component, Path, PathBuf, Prefix};
use std::sync::atomic::{AtomicU64, Ordering};

/// The only volume Anvil tooling may write to on Windows.
pub const REQUIRED_VOLUME: &str = "D:";

/// How a requested write path resolves on disk.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Resolution {
    /// Absolute, structurally cleaned input path (verbatim prefix removed).
    pub requested: PathBuf,
    /// Nearest existing ancestor after reparse-point resolution.
    pub existing_ancestor: PathBuf,
    /// Existing ancestor plus the still-missing path suffix.
    pub resolved: PathBuf,
    /// Actual volume of the existing ancestor, for example `D:`.
    pub volume: String,
}

impl Resolution {
    /// Whether the requested path has components that did not exist at resolve time.
    #[must_use]
    pub fn has_missing_components(&self) -> bool {
        self.resolved != self.existing_ancestor
    }
}

/// Errors produced by the D-drive path contract.
#[derive(Debug)]
pub enum PathError {
    /// The path was empty or whitespace only.
    EmptyPath,
    /// The path was relative or drive-relative (for example `D:foo`).
    RelativePath {
        /// Offending input.
        path: PathBuf,
    },
    /// The resolved path does not land on the required volume.
    OutsideD {
        /// Offending input (or the resolved path, for a post-create re-check).
        path: PathBuf,
        /// Volume the path actually lands on: `C:`, `\\host\share`, or the
        /// device namespace text for verbatim/volume-GUID paths.
        volume: String,
    },
    /// The path does not exist and creation was not requested.
    Missing {
        /// Missing path.
        path: PathBuf,
    },
    /// The path could not be created or write-probed.
    NotWritable {
        /// Path that failed.
        path: PathBuf,
        /// Underlying failure.
        source: Box<dyn std::error::Error + Send + Sync>,
    },
    /// The path could not be resolved to its real location.
    Resolve {
        /// Path that failed.
        path: PathBuf,
        /// Underlying failure.
        source: Box<dyn std::error::Error + Send + Sync>,
    },
    /// No existing ancestor could be found for the path.
    NoExistingAncestor {
        /// Offending input.
        path: PathBuf,
    },
}

impl fmt::Display for PathError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            PathError::EmptyPath => write!(f, "anvil-paths: empty path"),
            PathError::RelativePath { path } => {
                write!(f, "anvil-paths: relative path: {}", path.display())
            }
            PathError::OutsideD { path, volume } => write!(
                f,
                "anvil-paths: {} resolves to volume {volume}, required {REQUIRED_VOLUME}",
                path.display()
            ),
            PathError::Missing { path } => {
                write!(f, "anvil-paths: path does not exist: {}", path.display())
            }
            PathError::NotWritable { path, source } => write!(
                f,
                "anvil-paths: path is not writable: {}: {source}",
                path.display()
            ),
            PathError::Resolve { path, source } => {
                write!(
                    f,
                    "anvil-paths: cannot resolve {}: {source}",
                    path.display()
                )
            }
            PathError::NoExistingAncestor { path } => write!(
                f,
                "anvil-paths: no existing ancestor for {}",
                path.display()
            ),
        }
    }
}

impl std::error::Error for PathError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            PathError::NotWritable { source, .. } | PathError::Resolve { source, .. } => {
                Some(source.as_ref())
            }
            _ => None,
        }
    }
}

/// Filesystem operations the contract needs.
///
/// Tests inject fake resolvers so reject branches (other volume, volume change
/// between check and create, mkdir failure, probe failure) never need a fixture
/// on another drive. Production code uses [`SystemResolver`].
pub trait Resolver {
    /// Resolve `path` to the volume the write actually lands on.
    ///
    /// Implementations must follow reparse points and report the real volume.
    fn resolve(&self, path: &Path) -> Result<Resolution, PathError>;
    /// Create the directory and any missing parents.
    fn create_dir_all(&self, path: &Path) -> io::Result<()>;
    /// Verify the directory accepts a temporary file, then remove that file.
    fn probe_writable(&self, dir: &Path) -> io::Result<()>;
}

/// Production resolver backed by the operating system.
#[derive(Debug, Default, Clone, Copy)]
pub struct SystemResolver;

impl Resolver for SystemResolver {
    fn resolve(&self, path: &Path) -> Result<Resolution, PathError> {
        let requested = RequestedPath::parse(path)?;
        let (existing, missing) = existing_ancestor(&requested.absolute)?;
        let real = final_path(&existing).map_err(|source| PathError::Resolve {
            path: existing.clone(),
            source: Box::new(source),
        })?;
        let mut resolved = real.clone();
        for component in &missing {
            resolved.push(component);
        }
        let volume = volume_of(&real)?;
        Ok(Resolution {
            requested: requested.absolute,
            existing_ancestor: real,
            resolved,
            volume,
        })
    }

    fn create_dir_all(&self, path: &Path) -> io::Result<()> {
        std::fs::create_dir_all(path)
    }

    fn probe_writable(&self, dir: &Path) -> io::Result<()> {
        static PROBE_SEQUENCE: AtomicU64 = AtomicU64::new(0);
        let sequence = PROBE_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let probe = dir.join(format!(
            ".anvil-write-probe-{}-{sequence}",
            std::process::id()
        ));
        let file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&probe)?;
        drop(file);
        std::fs::remove_file(&probe)
    }
}

/// Return the final path of `path` with every reparse point (symlink, junction,
/// mount point) followed, as reported by `GetFinalPathNameByHandleW` with
/// `VOLUME_NAME_DOS`. The `\\?\` verbatim marker is removed from the result.
///
/// This is the same volume resolution the contract uses; it is exposed so
/// callers and evidence tooling can show where a path really lands.
#[cfg(windows)]
pub fn final_path(path: &Path) -> io::Result<PathBuf> {
    win::final_path(path)
}

/// Validate that `path` is a writable location on `D:`.
///
/// With `create == false` the path must already exist and be writable;
/// a missing path returns [`PathError::Missing`] and nothing is created.
/// With `create == true` missing directories are created and the result is
/// re-resolved so a component swapped for a cross-volume junction between the
/// check and the write is rejected.
///
/// No branch substitutes another directory: the returned [`Resolution`] always
/// describes the requested path, and an error never carries a fallback path.
pub fn validate_d_path<P: AsRef<Path>>(path: P, create: bool) -> Result<Resolution, PathError> {
    validate_d_path_with(path.as_ref(), create, &SystemResolver)
}

/// [`validate_d_path`] with an injected [`Resolver`].
pub fn validate_d_path_with(
    path: &Path,
    create: bool,
    resolver: &dyn Resolver,
) -> Result<Resolution, PathError> {
    let first = resolver.resolve(path)?;
    ensure_required_volume(path, &first)?;

    let resolution = if create {
        resolver
            .create_dir_all(&first.resolved)
            .map_err(|source| PathError::NotWritable {
                path: first.resolved.clone(),
                source: Box::new(source),
            })?;
        // A component may have been replaced by a junction between the volume
        // check and the directory creation: re-resolve before probing.
        let second = resolver.resolve(&first.resolved)?;
        ensure_required_volume(&first.resolved, &second)?;
        second
    } else {
        if first.has_missing_components() {
            return Err(PathError::Missing {
                path: first.resolved,
            });
        }
        first
    };

    resolver
        .probe_writable(&resolution.resolved)
        .map_err(|source| PathError::NotWritable {
            path: resolution.resolved.clone(),
            source: Box::new(source),
        })?;
    Ok(resolution)
}

/// Validate that `path` is a writable location on **any** volume.
///
/// This is the same reparse/junction-safe check as [`validate_d_path`] minus the
/// hard `D:` requirement. It exists for the app's own per-user data directory
/// (`%USERPROFILE%\.anvil`), which lives on whichever drive the profile does —
/// typically `C:`. Everything else (no silent fallback, no probing residue, a
/// re-resolve after creation) is unchanged.
pub fn validate_writable_path<P: AsRef<Path>>(path: P, create: bool) -> Result<Resolution, PathError> {
    validate_writable_path_with(path.as_ref(), create, &SystemResolver)
}

/// [`validate_writable_path`] with an injected [`Resolver`].
pub fn validate_writable_path_with(
    path: &Path,
    create: bool,
    resolver: &dyn Resolver,
) -> Result<Resolution, PathError> {
    let first = resolver.resolve(path)?;

    let resolution = if create {
        resolver
            .create_dir_all(&first.resolved)
            .map_err(|source| PathError::NotWritable {
                path: first.resolved.clone(),
                source: Box::new(source),
            })?;
        // Re-resolve after creation so a component swapped for a junction in
        // between cannot redirect the write.
        resolver.resolve(&first.resolved)?
    } else {
        if first.has_missing_components() {
            return Err(PathError::Missing {
                path: first.resolved,
            });
        }
        first
    };

    resolver
        .probe_writable(&resolution.resolved)
        .map_err(|source| PathError::NotWritable {
            path: resolution.resolved.clone(),
            source: Box::new(source),
        })?;
    Ok(resolution)
}

/// A structurally validated absolute request: `D:` volume plus cleaned path.
struct RequestedPath {
    absolute: PathBuf,
}

impl RequestedPath {
    fn parse(path: &Path) -> Result<Self, PathError> {
        if path.as_os_str().is_empty() || path.to_string_lossy().trim().is_empty() {
            return Err(PathError::EmptyPath);
        }

        let mut components = path.components();
        let prefix = match components.next() {
            Some(Component::Prefix(prefix)) => prefix,
            _ => {
                return Err(PathError::RelativePath {
                    path: path.to_path_buf(),
                });
            }
        };
        let kind = prefix.kind();
        let volume = prefix_volume(&kind, path);

        let mut parts: Vec<OsString> = Vec::new();
        let mut rooted = false;
        for component in components {
            match component {
                Component::Prefix(_) => unreachable!("std emits a single path prefix"),
                Component::RootDir => rooted = true,
                Component::CurDir => {}
                Component::ParentDir => {
                    // Windows cannot walk above the volume root.
                    parts.pop();
                }
                Component::Normal(name) => parts.push(name.to_os_string()),
            }
        }
        if !rooted {
            return Err(PathError::RelativePath {
                path: path.to_path_buf(),
            });
        }
        if !matches!(kind, Prefix::Disk(_) | Prefix::VerbatimDisk(_)) {
            // UNC shares and device/volume-GUID namespaces are not the local D:
            // volume. They are rejected before any filesystem access.
            return Err(PathError::OutsideD {
                path: path.to_path_buf(),
                volume,
            });
        }

        let mut absolute = PathBuf::from(format!("{volume}\\"));
        absolute.extend(parts.iter());
        Ok(Self { absolute })
    }
}

/// Nearest existing ancestor (with `symlink_metadata`, so a junction itself
/// counts as existing) plus the still-missing components, in order.
fn existing_ancestor(path: &Path) -> Result<(PathBuf, Vec<OsString>), PathError> {
    let mut missing: Vec<OsString> = Vec::new();
    let mut current = path.to_path_buf();
    loop {
        match std::fs::symlink_metadata(&current) {
            Ok(_) => return Ok((current, missing)),
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                let name = current.file_name().map(std::ffi::OsStr::to_os_string);
                match (current.parent(), name) {
                    (Some(parent), Some(name))
                        if !name.is_empty() && !parent.as_os_str().is_empty() =>
                    {
                        missing.insert(0, name);
                        current = parent.to_path_buf();
                    }
                    _ => {
                        return Err(PathError::NoExistingAncestor {
                            path: path.to_path_buf(),
                        });
                    }
                }
            }
            Err(source) => {
                return Err(PathError::Resolve {
                    path: current,
                    source: Box::new(source),
                });
            }
        }
    }
}

/// Canonical volume text for a path prefix: `D:`, `\\host\share`, or the raw
/// device namespace text.
fn prefix_volume(prefix: &Prefix<'_>, path: &Path) -> String {
    match prefix {
        Prefix::Disk(drive) | Prefix::VerbatimDisk(drive) => {
            format!("{}:", (*drive as char).to_ascii_uppercase())
        }
        Prefix::UNC(server, share) | Prefix::VerbatimUNC(server, share) => {
            format!(
                "\\\\{}\\{}",
                server.to_string_lossy(),
                share.to_string_lossy()
            )
        }
        Prefix::DeviceNS(_) | Prefix::Verbatim(_) => path.to_string_lossy().into_owned(),
    }
}

/// Volume of an absolute path, derived from its prefix.
fn volume_of(path: &Path) -> Result<String, PathError> {
    match path.components().next() {
        Some(Component::Prefix(prefix)) => Ok(prefix_volume(&prefix.kind(), path)),
        _ => Err(PathError::Resolve {
            path: path.to_path_buf(),
            source: Box::new(io::Error::new(
                io::ErrorKind::InvalidInput,
                "path has no volume prefix",
            )),
        }),
    }
}

/// Compare a reported volume against `D:` after normalizing `\\?\`/`\\?\UNC\`
/// prefixes, separators and case. `d:`, `D:\`, `\\?\D:` and `\\?\d:\` all match.
fn normalized_volume(volume: &str) -> String {
    let mut value = volume.trim().to_string();
    for prefix in ["\\\\?\\UNC\\", "\\??\\UNC\\"] {
        if let Some(rest) = value.strip_prefix(prefix) {
            value = format!("\\\\{rest}");
            break;
        }
    }
    for prefix in ["\\\\?\\", "\\??\\"] {
        if let Some(rest) = value.strip_prefix(prefix) {
            value = rest.to_string();
            break;
        }
    }
    value.trim_end_matches(['\\', '/']).to_ascii_uppercase()
}

fn ensure_required_volume(requested: &Path, resolution: &Resolution) -> Result<(), PathError> {
    if normalized_volume(&resolution.volume) == REQUIRED_VOLUME {
        Ok(())
    } else {
        Err(PathError::OutsideD {
            path: requested.to_path_buf(),
            volume: resolution.volume.clone(),
        })
    }
}
