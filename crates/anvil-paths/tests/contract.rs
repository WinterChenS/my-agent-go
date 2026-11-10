//! R0-RS01 contract tests for the Anvil D-drive write-path rule.
//!
//! Rule source: `docs/superpowers/specs/2026-11-09-anvil-rebuild-execution-standard.md`
//! section 4 (all Anvil writes must land on D:, verified on the *actual* volume
//! after resolving reparse points, and never falling back to another drive).
//!
//! Every fixture that touches the filesystem uses a D: temporary directory and
//! fails loudly when TEMP/TMP is not on D:, so the suite cannot silently write
//! to C:. Reject cases that would need a C: fixture use the injectable
//! [`Resolver`] instead, except for the cross-volume junction case where the
//! absence of a C: marker is itself the assertion.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Mutex;
use std::sync::atomic::{AtomicU64, Ordering};

use anvil_paths::{
    PathError, Resolution, Resolver, SystemResolver, final_path, validate_d_path,
    validate_d_path_with,
};

const PROBE_PREFIX: &str = ".anvil-write-probe";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

static UNIQUE: AtomicU64 = AtomicU64::new(0);

/// Volume of an absolute path in `D:` form, used to police the test TEMP dir.
fn volume_of(path: &Path) -> String {
    let text = path.to_string_lossy();
    if text.len() >= 2 && text.as_bytes()[1] == b':' {
        text[..2].to_ascii_uppercase()
    } else {
        String::from("<none>")
    }
}

fn require_drive_d(path: &Path, what: &str) {
    assert_eq!(
        volume_of(path),
        "D:",
        "{what} is {} which is not on D:; set TEMP/TMP to a D: path (scripts/env-rs.ps1)",
        path.display()
    );
}

fn normalize_for_compare(path: &Path) -> String {
    let mut text = path.to_string_lossy().replace('/', "\\");
    for prefix in ["\\\\?\\", "\\??\\"] {
        if let Some(rest) = text.strip_prefix(prefix) {
            text = rest.to_string();
        }
    }
    text.trim_end_matches('\\').to_ascii_lowercase()
}

fn same_path(a: &Path, b: &Path) -> bool {
    normalize_for_compare(a) == normalize_for_compare(b)
}

fn entry_names(dir: &Path) -> Vec<String> {
    let mut names: Vec<String> = fs::read_dir(dir)
        .unwrap_or_else(|e| panic!("read_dir({}): {e}", dir.display()))
        .map(|entry| {
            entry
                .expect("directory entry")
                .file_name()
                .to_string_lossy()
                .into_owned()
        })
        .collect();
    names.sort();
    names
}

fn assert_no_probe_residue(dir: &Path) {
    let residue: Vec<String> = entry_names(dir)
        .into_iter()
        .filter(|name| name.starts_with(PROBE_PREFIX))
        .collect();
    assert!(
        residue.is_empty(),
        "writable probe left files behind in {}: {residue:?}",
        dir.display()
    );
}

fn create_junction(link: &Path, target: &Path) {
    let output = Command::new("cmd")
        .arg("/c")
        .arg("mklink")
        .arg("/J")
        .arg(link)
        .arg(target)
        .output()
        .expect("run `cmd /c mklink /J`");
    assert!(
        output.status.success(),
        "mklink /J {} {} failed: {}{}",
        link.display(),
        target.display(),
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

/// A writable directory that really lives on C:, used to prove that a junction
/// rejection is not an artefact of C:\ root permissions.
fn writable_c_temp_dir() -> Option<PathBuf> {
    let local = PathBuf::from(std::env::var_os("LOCALAPPDATA")?);
    if !local
        .to_string_lossy()
        .to_ascii_uppercase()
        .starts_with("C:")
    {
        return None;
    }
    let temp = local.join("Temp");
    temp.is_dir().then_some(temp)
}

fn deny_writes(dir: &Path) {
    let output = Command::new("icacls")
        .arg(dir)
        .arg("/deny")
        .arg("*S-1-1-0:(W)")
        .output()
        .expect("run icacls");
    assert!(
        output.status.success(),
        "icacls /deny on {} failed: {}{}",
        dir.display(),
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn restore_writes(dir: &Path) {
    let _ = Command::new("icacls")
        .arg(dir)
        .arg("/remove:d")
        .arg("*S-1-1-0")
        .output();
}

/// A D: temporary directory that removes itself (and its junctions) on drop.
struct TestDir {
    path: PathBuf,
    junctions: Vec<PathBuf>,
}

impl TestDir {
    fn new(tag: &str) -> Self {
        let base = std::env::temp_dir();
        require_drive_d(&base, "test TEMP");
        let unique = UNIQUE.fetch_add(1, Ordering::SeqCst);
        let path = base.join(format!("anvil-rs01-{tag}-{}-{unique}", std::process::id()));
        fs::create_dir_all(&path)
            .unwrap_or_else(|e| panic!("create fixture {}: {e}", path.display()));
        Self {
            path,
            junctions: Vec::new(),
        }
    }

    fn path(&self) -> &Path {
        &self.path
    }

    fn join(&self, name: &str) -> PathBuf {
        self.path.join(name)
    }

    fn junction(&mut self, name: &str, target: &Path) -> PathBuf {
        let link = self.path.join(name);
        create_junction(&link, target);
        self.junctions.push(link.clone());
        link
    }
}

impl Drop for TestDir {
    fn drop(&mut self) {
        for junction in &self.junctions {
            // Removing the junction drops only the link, never the target.
            let _ = fs::remove_dir(junction);
        }
        let _ = fs::remove_dir_all(&self.path);
    }
}

// ---------------------------------------------------------------------------
// Injectable resolver: reject branches without any fixture on another volume
// ---------------------------------------------------------------------------

type ResolveFn = Box<dyn Fn(&Path, usize) -> Result<Resolution, PathError> + Send + Sync>;
type CreateFn = Box<dyn Fn(&Path) -> io::Result<()> + Send + Sync>;
type ProbeFn = Box<dyn Fn(&Path) -> io::Result<()> + Send + Sync>;

struct FakeResolver {
    resolve_fn: ResolveFn,
    create_fn: CreateFn,
    probe_fn: ProbeFn,
    resolve_calls: AtomicU64,
    created: Mutex<Vec<PathBuf>>,
    probed: Mutex<Vec<PathBuf>>,
}

impl FakeResolver {
    fn new() -> Self {
        Self {
            resolve_fn: Box::new(|path, _call| Ok(d_resolution(path))),
            create_fn: Box::new(|_| Ok(())),
            probe_fn: Box::new(|_| Ok(())),
            resolve_calls: AtomicU64::new(0),
            created: Mutex::new(Vec::new()),
            probed: Mutex::new(Vec::new()),
        }
    }

    fn with_resolve(
        mut self,
        resolve: impl Fn(&Path, usize) -> Result<Resolution, PathError> + Send + Sync + 'static,
    ) -> Self {
        self.resolve_fn = Box::new(resolve);
        self
    }

    fn with_create(
        mut self,
        create: impl Fn(&Path) -> io::Result<()> + Send + Sync + 'static,
    ) -> Self {
        self.create_fn = Box::new(create);
        self
    }

    fn with_probe(
        mut self,
        probe: impl Fn(&Path) -> io::Result<()> + Send + Sync + 'static,
    ) -> Self {
        self.probe_fn = Box::new(probe);
        self
    }

    fn created(&self) -> Vec<PathBuf> {
        self.created.lock().expect("created lock").clone()
    }

    fn probed(&self) -> Vec<PathBuf> {
        self.probed.lock().expect("probed lock").clone()
    }
}

impl Resolver for FakeResolver {
    fn resolve(&self, path: &Path) -> Result<Resolution, PathError> {
        let call = self.resolve_calls.fetch_add(1, Ordering::SeqCst) as usize;
        (self.resolve_fn)(path, call)
    }

    fn create_dir_all(&self, path: &Path) -> io::Result<()> {
        self.created
            .lock()
            .expect("created lock")
            .push(path.to_path_buf());
        (self.create_fn)(path)
    }

    fn probe_writable(&self, dir: &Path) -> io::Result<()> {
        self.probed
            .lock()
            .expect("probed lock")
            .push(dir.to_path_buf());
        (self.probe_fn)(dir)
    }
}

fn resolution_on(path: &Path, volume: &str) -> Resolution {
    Resolution {
        requested: path.to_path_buf(),
        existing_ancestor: path.to_path_buf(),
        resolved: path.to_path_buf(),
        volume: volume.to_string(),
    }
}

fn d_resolution(path: &Path) -> Resolution {
    resolution_on(path, "D:")
}

// ---------------------------------------------------------------------------
// 1. Existing D: directory
// ---------------------------------------------------------------------------

#[test]
fn accepts_existing_d_directory() {
    let dir = TestDir::new("existing");
    let resolution =
        validate_d_path(dir.path(), false).expect("existing D: directory must validate");

    assert_eq!(resolution.volume, "D:");
    assert!(same_path(&resolution.resolved, dir.path()));
    assert!(same_path(&resolution.existing_ancestor, dir.path()));
    assert!(!resolution.has_missing_components());
    assert_no_probe_residue(dir.path());
}

// ---------------------------------------------------------------------------
// 2. Missing subdirectory: diagnose when create=false, create when create=true
// ---------------------------------------------------------------------------

#[test]
fn reports_missing_subdirectory_without_creating_it() {
    let dir = TestDir::new("missing");
    let target = dir.join("missing").join("nested");

    let error = validate_d_path(&target, false).expect_err("missing path must not validate");
    assert!(
        matches!(error, PathError::Missing { .. }),
        "expected Missing, got {error:?}"
    );
    assert!(
        !target.exists(),
        "create=false must not create the directory"
    );
    assert!(!dir.join("missing").exists());
}

#[test]
fn creates_missing_subdirectory_on_d() {
    let dir = TestDir::new("create");
    let target = dir.join("a").join("b");
    let resolution = validate_d_path(&target, true).expect("creating a D: subdirectory must work");

    assert_eq!(resolution.volume, "D:");
    assert!(target.is_dir(), "target directory was not created");
    let real_base = final_path(dir.path()).expect("final path of fixture");
    assert!(
        normalize_for_compare(&resolution.resolved).starts_with(&normalize_for_compare(&real_base)),
        "resolved {} must live under the real D: fixture {}",
        resolution.resolved.display(),
        real_base.display()
    );
    assert_no_probe_residue(&target);
}

// ---------------------------------------------------------------------------
// 3. Another drive is rejected and nothing is created
// ---------------------------------------------------------------------------

#[test]
fn rejects_other_drive_without_creating_anything() {
    let target = Path::new(r"C:\Users\Administrator\AppData\Local\anvil-rs01-must-not-exist");
    let error = validate_d_path(target, true).expect_err("C: must be rejected");

    match error {
        PathError::OutsideD { volume, .. } => assert_eq!(volume, "C:"),
        other => panic!("expected OutsideD, got {other:?}"),
    }
    assert!(!target.exists(), "no directory may be created on C:");
}

// ---------------------------------------------------------------------------
// 4. Junction from D: to another volume is rejected without write-through
// ---------------------------------------------------------------------------

#[test]
fn rejects_junction_to_other_volume_without_write_through() {
    let mut dir = TestDir::new("junction-c");
    let c_root = Path::new(r"C:\");
    let link = dir.junction("to-c", c_root);
    let marker = format!(
        "anvil-rs01-marker-{}-{}",
        std::process::id(),
        UNIQUE.fetch_add(1, Ordering::SeqCst)
    );
    let target = link.join(&marker);

    let error = validate_d_path(&target, true).expect_err("junction to C: must be rejected");
    assert!(
        matches!(error, PathError::OutsideD { .. }),
        "expected OutsideD, got {error:?}"
    );

    let c_side = c_root.join(&marker);
    assert!(
        !c_side.exists(),
        "write went through the junction to {}",
        c_side.display()
    );

    // C:\ root needs elevation, so repeat against a writable C: directory: a
    // write-through there would really create the marker.
    if let Some(c_temp) = writable_c_temp_dir() {
        let writable_link = dir.junction("to-c-temp", &c_temp);
        let writable_target = writable_link.join(&marker);
        let error = validate_d_path(&writable_target, true)
            .expect_err("junction to a writable C: directory must be rejected");
        assert!(
            matches!(error, PathError::OutsideD { .. }),
            "expected OutsideD, got {error:?}"
        );
        assert!(
            !c_temp.join(&marker).exists(),
            "write went through the junction to writable C: directory {}",
            c_temp.display()
        );
    } else {
        eprintln!("note: no writable C: temp directory; only the C:\\ root check ran");
    }
}

// ---------------------------------------------------------------------------
// 5. Junction inside D: is accepted and reported as the real path
// ---------------------------------------------------------------------------

#[test]
fn accepts_junction_inside_d_and_reports_real_path() {
    let mut dir = TestDir::new("junction-d");
    let real = dir.join("real");
    fs::create_dir_all(&real).expect("create real dir");
    let link = dir.junction("to-real", &real);
    let target = link.join("child");

    let resolution = validate_d_path(&target, true).expect("junction inside D: must validate");

    assert_eq!(resolution.volume, "D:");
    let real_final = final_path(&real).expect("final path of real dir");
    assert!(
        same_path(&resolution.resolved, &real_final.join("child")),
        "resolved {} must be the real path {}, not the junction {}",
        resolution.resolved.display(),
        real_final.join("child").display(),
        link.display()
    );
    assert!(
        target.is_dir(),
        "the child must exist in the junction target"
    );
    assert!(
        same_path(
            &final_path(&link).expect("final path of junction"),
            &real_final
        ),
        "final_path must follow the junction to the real directory"
    );
}

// ---------------------------------------------------------------------------
// 6. Unwritable D: directory: NotWritable, no fallback, no residue
// ---------------------------------------------------------------------------

#[test]
fn rejects_unwritable_directory_without_fallback() {
    let dir = TestDir::new("unwritable");
    let denied = dir.join("denied");
    fs::create_dir_all(&denied).expect("create denied dir");
    deny_writes(&denied);

    let before = entry_names(dir.path());
    let error = validate_d_path(&denied, false).expect_err("unwritable directory must be rejected");
    let after = entry_names(dir.path());
    restore_writes(&denied);

    assert!(
        matches!(error, PathError::NotWritable { .. }),
        "expected NotWritable, got {error:?}"
    );
    assert_eq!(
        before, after,
        "a fallback directory was created next to the unwritable target"
    );
    assert!(denied.is_dir(), "the denied directory must be left alone");
    assert_no_probe_residue(&denied);
}

#[test]
fn rejects_unwritable_directory_when_create_is_requested() {
    let dir = TestDir::new("unwritable-create");
    let denied = dir.join("denied");
    fs::create_dir_all(&denied).expect("create denied dir");
    deny_writes(&denied);

    let error = validate_d_path(&denied, true).expect_err("unwritable directory must be rejected");
    restore_writes(&denied);

    assert!(
        matches!(error, PathError::NotWritable { .. }),
        "expected NotWritable, got {error:?}"
    );
}

// ---------------------------------------------------------------------------
// 7. Relative, empty, drive-relative and UNC paths are rejected explicitly
// ---------------------------------------------------------------------------

#[test]
fn rejects_empty_paths() {
    for candidate in ["", " ", "\t\r\n"] {
        let error = validate_d_path(candidate, false).expect_err("empty path must be rejected");
        assert!(
            matches!(error, PathError::EmptyPath),
            "expected EmptyPath for {candidate:?}, got {error:?}"
        );
    }
}

#[test]
fn rejects_relative_paths() {
    for candidate in [
        r"relative\dir",
        r".\relative",
        r"..\up",
        r"D:drive-relative",
    ] {
        let error = validate_d_path(candidate, true).expect_err("relative path must be rejected");
        assert!(
            matches!(error, PathError::RelativePath { .. }),
            "expected RelativePath for {candidate:?}, got {error:?}"
        );
    }
}

#[test]
fn rejects_unc_paths() {
    let error = validate_d_path(r"\\server\share\anvil", true).expect_err("UNC must be rejected");
    match error {
        PathError::OutsideD { volume, .. } => assert!(
            volume.eq_ignore_ascii_case(r"\\server\share"),
            "UNC volume should be reported as the share, got {volume:?}"
        ),
        other => panic!("expected OutsideD for a UNC path, got {other:?}"),
    }
}

#[test]
fn rejects_verbatim_unc_and_device_paths() {
    for candidate in [
        r"\\?\UNC\server\share\anvil",
        r"\\?\Volume{00000000-0000-0000-0000-000000000000}\anvil",
    ] {
        let error = validate_d_path(candidate, true).expect_err("non-drive path must be rejected");
        assert!(
            matches!(error, PathError::OutsideD { .. }),
            "expected OutsideD for {candidate:?}, got {error:?}"
        );
    }
}

// ---------------------------------------------------------------------------
// 8. Verbatim prefix and drive-letter case normalization
// ---------------------------------------------------------------------------

#[test]
fn normalizes_verbatim_prefix_and_drive_case() {
    let dir = TestDir::new("normalize");
    let plain = dir.path().to_string_lossy().replace('/', "\\");
    let verbatim = format!(r"\\?\{plain}");
    let lower = format!("d:{}", &plain[2..]);
    let forward = plain.replace('\\', "/");

    for candidate in [plain.clone(), verbatim, lower, forward] {
        let resolution = validate_d_path(&candidate, false)
            .unwrap_or_else(|e| panic!("{candidate} must validate: {e:?}"));
        assert_eq!(resolution.volume, "D:", "candidate {candidate}");
        assert!(
            same_path(&resolution.resolved, dir.path()),
            "candidate {candidate} resolved to {}",
            resolution.resolved.display()
        );
    }

    // `\\?\D:\...` and `D:\...` describe the same volume, so they must agree.
    let verbatim_volume = validate_d_path(format!(r"\\?\{plain}"), false)
        .expect("verbatim D: path")
        .volume;
    let plain_volume = validate_d_path(&plain, false)
        .expect("plain D: path")
        .volume;
    assert_eq!(verbatim_volume, plain_volume);
}

#[test]
fn normalizes_dot_and_parent_components() {
    let dir = TestDir::new("structural");
    let plain = dir.join("plain");
    fs::create_dir_all(&plain).expect("create plain dir");

    let dotted = dir.join("plain").join(".").join("child");
    let parented = dir.join("plain").join("child").join("..").join("other");
    for (candidate, expected) in [
        (dotted, plain.join("child")),
        (parented, plain.join("other")),
    ] {
        let resolution = SystemResolver
            .resolve(&candidate)
            .unwrap_or_else(|e| panic!("{} must resolve: {e:?}", candidate.display()));
        assert!(
            same_path(&resolution.resolved, &expected),
            "{} resolved to {} instead of {}",
            candidate.display(),
            resolution.resolved.display(),
            expected.display()
        );
        assert_eq!(resolution.volume, "D:");
    }
}

// ---------------------------------------------------------------------------
// 9. A component replaced by a cross-volume junction between check and create
// ---------------------------------------------------------------------------

#[test]
fn rejects_volume_change_between_check_and_create() {
    let target = Path::new(r"D:\project\anvil-rs01\swapped");
    let fake = FakeResolver::new().with_resolve(|path, call| {
        if call == 0 {
            Ok(d_resolution(path))
        } else {
            Ok(resolution_on(path, "C:"))
        }
    });

    let error = validate_d_path_with(target, true, &fake)
        .expect_err("volume change after creation must be rejected");

    assert!(
        matches!(error, PathError::OutsideD { .. }),
        "expected OutsideD, got {error:?}"
    );
    assert_eq!(
        fake.created(),
        vec![target.to_path_buf()],
        "the implementation must create exactly the requested path"
    );
    assert!(
        fake.probed().is_empty(),
        "a path that moved off D: must never be probed: {:?}",
        fake.probed()
    );
}

#[test]
fn never_creates_when_resolution_is_outside_d() {
    let target = Path::new(r"D:\project\anvil-rs01\junction-to-c");
    let fake = FakeResolver::new().with_resolve(|path, _call| Ok(resolution_on(path, "C:")));

    let error =
        validate_d_path_with(target, true, &fake).expect_err("C: resolution must be rejected");

    assert!(matches!(error, PathError::OutsideD { .. }), "got {error:?}");
    assert!(
        fake.created().is_empty(),
        "nothing may be created before the volume check: {:?}",
        fake.created()
    );
    assert!(fake.probed().is_empty());
}

// ---------------------------------------------------------------------------
// 10. mkdir failure and probe failure: NotWritable, no fallback
// ---------------------------------------------------------------------------

#[test]
fn mkdir_failure_is_not_writable_and_does_not_fall_back() {
    let target = Path::new(r"D:\project\anvil-rs01\mkdir-fails");
    let fake =
        FakeResolver::new().with_create(|_| Err(io::Error::from(io::ErrorKind::PermissionDenied)));

    let error = validate_d_path_with(target, true, &fake).expect_err("mkdir failure must not pass");

    assert!(
        matches!(error, PathError::NotWritable { .. }),
        "expected NotWritable, got {error:?}"
    );
    assert_eq!(
        fake.created(),
        vec![target.to_path_buf()],
        "the implementation must not try any other directory"
    );
    assert!(
        fake.probed().is_empty(),
        "a failed create must not be probed: {:?}",
        fake.probed()
    );
}

#[test]
fn probe_failure_is_not_writable_and_does_not_fall_back() {
    let target = Path::new(r"D:\project\anvil-rs01\probe-fails");
    let fake =
        FakeResolver::new().with_probe(|_| Err(io::Error::from(io::ErrorKind::PermissionDenied)));

    let error = validate_d_path_with(target, true, &fake).expect_err("probe failure must not pass");

    assert!(
        matches!(error, PathError::NotWritable { .. }),
        "expected NotWritable, got {error:?}"
    );
    assert_eq!(fake.created(), vec![target.to_path_buf()]);
    assert_eq!(fake.probed(), vec![target.to_path_buf()]);
}

#[test]
fn resolve_error_is_propagated() {
    let target = Path::new(r"D:\project\anvil-rs01\boom");
    let fake = FakeResolver::new().with_resolve(|path, _call| {
        Err(PathError::Resolve {
            path: path.to_path_buf(),
            source: Box::new(io::Error::other("boom")),
        })
    });

    let error =
        validate_d_path_with(target, true, &fake).expect_err("resolve error must propagate");
    assert!(matches!(error, PathError::Resolve { .. }), "got {error:?}");
    assert!(fake.created().is_empty());
}

// ---------------------------------------------------------------------------
// 12. Real D: writability probe leaves no residue
// ---------------------------------------------------------------------------

#[test]
fn probes_real_d_temp_directory_without_residue() {
    let dir = TestDir::new("probe");
    let before = entry_names(dir.path());
    let resolution = validate_d_path(dir.path(), false).expect("D: temp dir must be writable");

    assert_eq!(resolution.volume, "D:");
    assert_eq!(
        resolution.resolved,
        final_path(dir.path()).expect("final path")
    );
    assert_eq!(entry_names(dir.path()), before, "probe created residue");
    assert_no_probe_residue(dir.path());
}

// ---------------------------------------------------------------------------
// SystemResolver surface: resolve() reports the real volume, not the text prefix
// ---------------------------------------------------------------------------

#[test]
fn system_resolver_reports_real_volume() {
    let mut dir = TestDir::new("resolver");
    let real = dir.join("real");
    fs::create_dir_all(&real).expect("create real dir");
    let link = dir.junction("to-c", Path::new(r"C:\Windows"));

    let resolver = SystemResolver;
    let junction = resolver
        .resolve(&link)
        .expect("resolving a junction must work");
    assert_eq!(junction.volume, "C:");
    assert!(same_path(&junction.resolved, Path::new(r"C:\Windows")));

    let inside = resolver.resolve(&real).expect("resolving D: dir");
    assert_eq!(inside.volume, "D:");
}

#[test]
fn system_resolver_rejects_relative_and_empty_paths() {
    let resolver = SystemResolver;
    assert!(matches!(
        resolver.resolve(Path::new("")).expect_err("empty"),
        PathError::EmptyPath
    ));
    assert!(matches!(
        resolver
            .resolve(Path::new(r"relative\dir"))
            .expect_err("relative"),
        PathError::RelativePath { .. }
    ));
}
