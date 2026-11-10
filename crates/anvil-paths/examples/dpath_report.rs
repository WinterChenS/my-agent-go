//! R0-RS01 evidence report: real Windows volume resolution for the D: rule.
//!
//! Prints, for the same paths, the `GetFinalPathNameByHandleW` answer, the
//! volume the contract accepts or rejects, and where a marker file actually
//! landed on disk. Junctions inside D: and from D: to C: are created for real
//! (no `fsutil` is used as the criterion). The process exits 1 if any expected
//! outcome is not observed.
//!
//! Run from the repository root (TEMP must be on D:, see scripts/env-rs.ps1):
//!   cargo run --locked --quiet -p anvil-paths --example dpath_report

use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

use anvil_paths::{final_path, validate_d_path};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut failures = 0usize;
    let temp = std::env::temp_dir();
    println!("report     = anvil-paths R0-RS01 dpath-real");
    println!("temp_root  = {}", temp.display());

    let root = temp.join(format!("anvil-rs01-dpath-{}", std::process::id()));
    if root.exists() {
        fs::remove_dir_all(&root)?;
    }
    fs::create_dir_all(&root)?;

    println!("root              = {}", root.display());
    println!("root_final_path   = {}", final_path(&root)?.display());

    // 1. Plain existing D: directory.
    let plain = root.join("plain");
    fs::create_dir_all(&plain)?;
    let plain_resolution = validate_d_path(&plain, false)?;
    println!(
        "plain.requested   = {}",
        plain_resolution.requested.display()
    );
    println!("plain.final_path  = {}", final_path(&plain)?.display());
    println!("plain.volume      = {}", plain_resolution.volume);
    println!(
        "plain.resolved    = {}",
        plain_resolution.resolved.display()
    );
    if plain_resolution.volume != "D:" {
        failures += 1;
    }

    // 2. Junction inside D: -> reported as the real target, and a write through
    //    the junction really lands in the target directory.
    let real = root.join("real");
    fs::create_dir_all(&real)?;
    let inside_link = root.join("junction-inside-d");
    make_junction(&inside_link, &real)?;
    let target = inside_link.join("child");
    let inside_resolution = validate_d_path(&target, true)?;
    let real_final = final_path(&real)?;
    println!("inside.link                 = {}", inside_link.display());
    println!(
        "inside.link_final_path      = {}",
        final_path(&inside_link)?.display()
    );
    println!("inside.target               = {}", real.display());
    println!("inside.target_final_path    = {}", real_final.display());
    println!("inside.validate_volume      = {}", inside_resolution.volume);
    println!(
        "inside.validate_resolved    = {}",
        inside_resolution.resolved.display()
    );
    let marker_through_link = inside_link.join("marker.txt");
    fs::write(&marker_through_link, b"anvil-rs01")?;
    println!(
        "inside.marker_through_link  = {}",
        marker_through_link.display()
    );
    println!(
        "inside.marker_final_path    = {}",
        final_path(&marker_through_link)?.display()
    );
    println!(
        "inside.marker_in_real_dir   = {}",
        real.join("marker.txt").exists()
    );
    if inside_resolution.volume != "D:"
        || inside_resolution.resolved != real_final.join("child")
        || !real.join("marker.txt").exists()
    {
        failures += 1;
    }

    // 3. Junction from D: to C:\ must be rejected and must not write through
    //    to C:. C:\ itself needs elevation, so the write-through check is also
    //    repeated against a writable C: directory.
    let outside_link = root.join("junction-to-c");
    make_junction(&outside_link, Path::new(r"C:\"))?;
    let marker_name = format!("anvil-rs01-must-not-exist-{}", std::process::id());
    let forbidden = outside_link.join(&marker_name);
    let outcome = validate_d_path(&forbidden, true);
    let c_landed = Path::new(r"C:\").join(&marker_name).exists();
    println!("outside.link                = {}", outside_link.display());
    println!(
        "outside.link_final_path     = {}",
        final_path(&outside_link)?.display()
    );
    println!("outside.validate            = {outcome:?}");
    println!("outside.c_marker_exists     = {c_landed}");
    match &outcome {
        Err(anvil_paths::PathError::OutsideD { volume, .. }) if volume == "C:" && !c_landed => {}
        _ => failures += 1,
    }
    if let Some(c_temp) = writable_c_temp_dir() {
        let writable_link = root.join("junction-to-c-temp");
        make_junction(&writable_link, &c_temp)?;
        let writable_marker = format!("anvil-rs01-must-not-exist-writable-{}", std::process::id());
        let writable_outcome = validate_d_path(writable_link.join(&writable_marker), true);
        let writable_landed = c_temp.join(&writable_marker).exists();
        println!("outside_writable_c.link     = {}", writable_link.display());
        println!(
            "outside_writable_c.final    = {}",
            final_path(&writable_link)?.display()
        );
        println!("outside_writable_c.validate = {writable_outcome:?}");
        println!("outside_writable_c.marker   = {writable_landed}");
        match &writable_outcome {
            Err(anvil_paths::PathError::OutsideD { volume, .. })
                if volume == "C:" && !writable_landed => {}
            _ => failures += 1,
        }
        let _ = fs::remove_dir(&writable_link);
    } else {
        println!("outside_writable_c          = skipped (no writable C: temp directory)");
    }

    // 4. `\\?\D:\...`, `d:\...` and forward slashes describe the same volume.
    let plain_text = plain.to_string_lossy().replace('/', "\\");
    let verbatim = format!(r"\\?\{plain_text}");
    let lowercase = format!("d:{}", &plain_text[2..]);
    let forward = plain_text.replace('\\', "/");
    for (label, candidate) in [
        ("verbatim", verbatim),
        ("lowercase", lowercase),
        ("forward", forward),
    ] {
        match validate_d_path(&candidate, false) {
            Ok(resolution) => {
                println!(
                    "{label}.volume = {} {label}.resolved = {}",
                    resolution.volume,
                    resolution.resolved.display()
                );
                if resolution.volume != "D:" {
                    failures += 1;
                }
            }
            Err(error) => {
                println!("{label}.error = {error:?}");
                failures += 1;
            }
        }
    }

    // 5. Rejections that must never fall back.
    let rejected = [
        ("c_drive", PathBuf::from(r"C:\Windows")),
        ("unc", PathBuf::from(r"\\server\share\anvil")),
        ("relative", PathBuf::from(r"relative\dir")),
        ("empty", PathBuf::new()),
    ];
    for (label, candidate) in rejected {
        match validate_d_path(&candidate, false) {
            Ok(resolution) => {
                println!(
                    "reject.{label} = UNEXPECTED OK volume {}",
                    resolution.volume
                );
                failures += 1;
            }
            Err(error) => println!("reject.{label} = {error:?}"),
        }
    }

    let _ = fs::remove_file(&marker_through_link);
    let _ = fs::remove_dir(&inside_link);
    let _ = fs::remove_dir(&outside_link);
    fs::remove_dir_all(&root)?;
    println!("residue_removed = {}", !root.exists());
    println!("failures        = {failures}");

    if failures == 0 {
        Ok(())
    } else {
        Err(format!("{failures} unexpected outcome(s)").into())
    }
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

fn make_junction(link: &Path, target: &Path) -> Result<(), Box<dyn std::error::Error>> {
    let output = Command::new("cmd")
        .arg("/c")
        .arg("mklink")
        .arg("/J")
        .arg(link)
        .arg(target)
        .output()?;
    if output.status.success() {
        Ok(())
    } else {
        Err(format!(
            "mklink /J {} {} failed: {}{}",
            link.display(),
            target.display(),
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        )
        .into())
    }
}
