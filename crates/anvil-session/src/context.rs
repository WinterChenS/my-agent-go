//! `@` references: resolution, confinement and prompt folding.
//!
//! Three rules shape this module:
//!
//! * **The session's project is the boundary.** A reference inside it is read
//!   without ceremony; one outside it is not read until the user approved that
//!   specific path, and a refusal leaves nothing behind.
//! * **Resolution goes through `canonicalize`.** `..`, junction points and
//!   symlinks all collapse to one absolute path, so a link that points out of the
//!   project is caught by the same check as a literal `..\..`.
//! * **The runtime reads what only it can read.** Text is inlined here; images
//!   (PNG, JPEG, GIF, WebP, BMP) are handed to the runtime as `@path`
//!   attachments, because an image becomes an image content block the model can
//!   actually see. Everything else is handed over as a path and left to the
//!   agent's own tools: a coding agent opens a PDF or a DOCX with a script,
//!   which beats any guess this module could make about the bytes.
//! * **Budgets are visible.** A cut file says it was cut, a directory says how
//!   many entries were not listed, and the ledger records what was inlined.
//!   Silently sending half a file would make the model's answer look wrong for
//!   no stated reason.

use std::path::{Path, PathBuf};

use crate::error::{Error, Result};
use crate::model::{ContextEntry, ContextKind, ContextRef, ResolvedRef};

/// More references than this in one turn is a mistake, not a workflow.
pub const MAX_REFS: usize = 8;
/// How much of one file is inlined.
pub const MAX_FILE_BYTES: usize = 8 * 1024;
/// How much context one turn may carry in total.
pub const MAX_TOTAL_CHARS: usize = 24 * 1024;
/// Directory listings are shallow and bounded.
const MAX_DIR_ENTRIES: usize = 50;
/// How deep the composer's `@` list walks.
const LIST_DEPTH: usize = 3;
/// Directories the `@` list never walks into.
const LIST_SKIP: [&str; 7] = [
    ".git",
    "node_modules",
    "target",
    "dist",
    ".tmp",
    ".venv",
    "__pycache__",
];

/// One reference after planning: either ready to inline, or waiting for consent.
#[derive(Debug)]
pub enum Step {
    Ready(Box<Prepared>),
    /// The path resolves outside the project; the caller must ask the user.
    Awaiting {
        reference: ContextRef,
        path: PathBuf,
    },
}

/// A reference that was read (or listed) and rendered.
#[derive(Debug)]
pub struct Prepared {
    pub resolved: ResolvedRef,
    /// Prompt text. Empty for an image: the runtime attaches it natively.
    pub block: String,
    /// Set for images, which the runtime turns into an image content block.
    pub attachment: Option<PathBuf>,
}

/// What the bytes of a file are, as far as this build can tell.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Media {
    /// A bitmap the runtime attaches as an image content block.
    Image,
    /// UTF-8 text, inlined into the prompt here.
    Text,
    /// Neither: inlining it would be mojibake, so only its path is handed over
    /// and the agent reads it with its own tools.
    Binary,
}

/// Enough bytes to see an image header and a PNG's `acTL` chunk.
const SNIFF_BYTES: usize = 4100;

/// Resolve every reference, reading the ones that are inside the boundary.
///
/// Outside-root references come back as [`Step::Awaiting`] with their canonical
/// path: the caller decides whether to ask, and reads them with [`allow`] only
/// after the user said yes.
pub fn plan(
    project_root: &str,
    skill_roots: &[PathBuf],
    references: &[ContextRef],
) -> Result<Vec<Step>> {
    if references.len() > MAX_REFS {
        return Err(Error::Rejected(format!(
            "at most {MAX_REFS} context references per turn, got {}",
            references.len()
        )));
    }
    let root = canonical_dir(project_root)?;
    let mut steps = Vec::with_capacity(references.len());
    let mut seen: Vec<PathBuf> = Vec::new();
    for reference in references {
        let path = resolve_path(&root, &reference.path)?;
        // Two ways of naming the same file must not double the prompt.
        if seen.iter().any(|known| same_path(known, &path)) {
            continue;
        }
        seen.push(path.clone());
        if reference.kind == ContextKind::Skill {
            let inside = skill_roots
                .iter()
                .any(|skill_root| is_within(skill_root, &path));
            if !inside {
                return Err(Error::Rejected(format!(
                    "skill {} is not under a configured skill root",
                    path.display()
                )));
            }
            steps.push(Step::Ready(Box::new(read(reference, &path, false)?)));
            continue;
        }
        // No file type is refused here. Text is inlined, images travel as
        // attachments, and anything else is handed over as a path so the agent
        // can read it with its own tools — a coding agent writes a script for a
        // PDF or unzips a DOCX far more capably than a byte sniff can guess.
        if is_within(&root, &path) {
            steps.push(Step::Ready(Box::new(read(reference, &path, false)?)));
        } else {
            steps.push(Step::Awaiting {
                reference: reference.clone(),
                path,
            });
        }
    }
    Ok(steps)
}

/// Read a reference the user explicitly approved, even though it is outside the
/// project. Kept separate from [`plan`] so "outside" can never be silently read.
pub fn allow(reference: &ContextRef, path: &Path) -> Result<Prepared> {
    read(reference, path, true)
}

/// Fold the prepared references into the prompt the runtime receives.
///
/// The user's words stay last and untouched: every runtime reads the tail of a
/// prompt more reliably than the middle. Images contribute no block — the
/// runtime prepends its own reference for each attachment — so a turn made of
/// images only leaves the prompt exactly as the user typed it.
pub fn fold(prompt: &str, prepared: &[Prepared]) -> String {
    let mut budget = MAX_TOTAL_CHARS;
    let mut blocks: Vec<&str> = Vec::new();
    for entry in prepared {
        if entry.block.trim().is_empty() {
            continue;
        }
        if entry.block.chars().count() > budget {
            blocks.push("<context omitted=\"budget exhausted\"/>");
            budget = 0;
            continue;
        }
        budget -= entry.block.chars().count();
        blocks.push(&entry.block);
    }
    if blocks.is_empty() {
        return prompt.to_string();
    }
    format!(
        "The user attached the following context. Use it when answering the last message.\n\n{}\n\n{prompt}",
        blocks.join("\n\n")
    )
}

/// Entries for the composer's `@` list, relative to `project_root`.
pub fn entries(project_root: &str, query: &str, limit: usize) -> Result<Vec<ContextEntry>> {
    let root = canonical_dir(project_root)?;
    let needle = query.trim().to_lowercase();
    let limit = limit.clamp(1, 100);
    let mut found: Vec<ContextEntry> = Vec::new();
    walk(&root, &root, 0, &needle, limit, &mut found);
    // Shallow names first: the thing the user is typing is usually near the top.
    found.sort_by_key(|entry| {
        (
            entry.relative.matches(['/', '\\']).count(),
            entry.relative.clone(),
        )
    });
    found.truncate(limit);
    Ok(found)
}

fn walk(
    root: &Path,
    directory: &Path,
    depth: usize,
    needle: &str,
    limit: usize,
    found: &mut Vec<ContextEntry>,
) {
    if depth > LIST_DEPTH || found.len() >= limit * 4 {
        return;
    }
    let Ok(entries) = std::fs::read_dir(directory) else {
        return;
    };
    let mut paths: Vec<PathBuf> = entries.flatten().map(|entry| entry.path()).collect();
    paths.sort();
    for path in paths {
        let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
            continue;
        };
        if name.starts_with('.') && name != ".env" {
            continue;
        }
        let is_dir = path.is_dir();
        if is_dir && LIST_SKIP.contains(&name) {
            continue;
        }
        let relative = path
            .strip_prefix(root)
            .unwrap_or(&path)
            .to_string_lossy()
            .replace('\\', "/");
        if needle.is_empty() || relative.to_lowercase().contains(needle) {
            found.push(ContextEntry {
                path: path.to_string_lossy().to_string(),
                relative: if is_dir {
                    format!("{relative}/")
                } else {
                    relative.clone()
                },
                kind: if is_dir {
                    ContextKind::Dir
                } else {
                    ContextKind::File
                },
                bytes: if is_dir {
                    0
                } else {
                    std::fs::metadata(&path).map(|meta| meta.len()).unwrap_or(0)
                },
            });
        }
        if is_dir && found.len() < limit * 4 {
            walk(root, &path, depth + 1, needle, limit, found);
        }
    }
}

/// What the prompt says about a file whose bytes cannot be inlined.
///
/// The agent gets the path and is told to read it with its own tools: a PDF, a
/// DOCX or an archive is exactly the kind of thing a coding agent handles by
/// writing a short script, and refusing it here would mean the user cannot ask
/// about a file they clearly attached.
fn binary_block(source: &Path, bytes: u64) -> String {
    format!(
        "--- file {} (binary, {bytes} bytes) ---\n\
         This file is not plain text and is not one of the image formats the runtime attaches, so its bytes were not inlined. \
         Read it with your own tools before answering: for example a short script that extracts the text \
         (a zip archive such as DOCX/XLSX/PPTX opens with python's `zipfile` plus the XML inside; a PDF needs a text extractor such as `pypdf`). \
         If the file turns out to contain no readable text, say so instead of guessing.\n\
         --- end ---",
        source.display()
    )
}

/// Classify the head of a file.
///
/// The image signatures mirror the runtime's own sniffer: declaring an image
/// it will not accept would replace a clear refusal with a crashed child.
fn sniff(head: &[u8]) -> Media {
    if is_image(head) {
        return Media::Image;
    }
    // NUL bytes never appear in UTF-8 text. A multi-byte character cut off by
    // the buffer ends with `error_len() == None`, which a text file does too, so
    // only a malformed *complete* sequence counts as binary.
    if head.contains(&0)
        || matches!(std::str::from_utf8(head), Err(error) if error.error_len().is_some())
    {
        return Media::Binary;
    }
    Media::Text
}

fn is_image(head: &[u8]) -> bool {
    // JPEG: SOI plus a marker; 0xF7 is JPEG-LS, which the runtime skips.
    if head.starts_with(&[0xff, 0xd8, 0xff]) {
        return head.get(3) != Some(&0xf7);
    }
    // PNG: signature, IHDR as the first chunk, and not animated (APNG loses
    // frames when it is flattened, so the runtime refuses it as well).
    if head.starts_with(&[0x89, b'P', b'N', b'G', 0x0d, 0x0a, 0x1a, 0x0a]) {
        let ihdr = head.len() >= 16 && head[8..12] == [0, 0, 0, 13] && head[12..16] == *b"IHDR";
        return ihdr && !is_animated_png(head);
    }
    // GIF, WebP.
    if head.starts_with(b"GIF87a") || head.starts_with(b"GIF89a") {
        return true;
    }
    if head.len() >= 12 && head.starts_with(b"RIFF") && head[8..12] == *b"WEBP" {
        return true;
    }
    // BMP: the declared size and pixel offset have to be consistent, otherwise
    // a text file starting with "BM" would look like a bitmap.
    if head.len() >= 26 && head.starts_with(b"BM") {
        let declared = u32::from_le_bytes([head[2], head[3], head[4], head[5]]);
        let offset = u32::from_le_bytes([head[10], head[11], head[12], head[13]]);
        let dib = u32::from_le_bytes([head[14], head[15], head[16], head[17]]);
        return (declared == 0 || declared >= 26) && offset >= 14 + dib;
    }
    false
}

fn is_animated_png(head: &[u8]) -> bool {
    let mut offset = 8;
    while offset + 8 <= head.len() {
        let length = u32::from_be_bytes([
            head[offset],
            head[offset + 1],
            head[offset + 2],
            head[offset + 3],
        ]) as usize;
        let kind = &head[offset + 4..offset + 8];
        if kind == b"acTL" {
            return true;
        }
        if kind == b"IDAT" {
            return false;
        }
        let next = offset + 8 + length + 4;
        if next <= offset || next > head.len() {
            return false;
        }
        offset = next;
    }
    false
}

/// The image attachments of a prepared set, in the order the user added them.
#[must_use]
pub fn attachments(prepared: &[Prepared]) -> Vec<PathBuf> {
    prepared
        .iter()
        .filter_map(|entry| entry.attachment.clone())
        .collect()
}

fn read(reference: &ContextRef, path: &Path, outside_root: bool) -> Result<Prepared> {
    let metadata = std::fs::metadata(path)
        .map_err(|error| Error::NotFound(format!("{}: {error}", path.display())))?;
    let label = reference.label.clone();
    match reference.kind {
        ContextKind::Dir => {
            if !metadata.is_dir() {
                return Err(Error::Rejected(format!(
                    "{} is not a directory",
                    path.display()
                )));
            }
            let listing = listing(path)?;
            Ok(Prepared {
                block: format!(
                    "<directory path=\"{}\">\n{}\n</directory>",
                    path.display(),
                    listing.0
                ),
                resolved: ResolvedRef {
                    kind: ContextKind::Dir,
                    path: path.to_string_lossy().to_string(),
                    label,
                    bytes: 0,
                    chars: listing.0.chars().count(),
                    truncated: listing.1,
                    outside_root,
                },
                attachment: None,
            })
        }
        ContextKind::File | ContextKind::Skill => {
            let source = if reference.kind == ContextKind::Skill && metadata.is_dir() {
                path.join("SKILL.md")
            } else {
                path.to_path_buf()
            };
            if source.is_dir() {
                return Err(Error::Rejected(format!(
                    "{} is a directory; reference it as a directory",
                    path.display()
                )));
            }
            // Only the head is read. A 500 MB video attached as context must
            // not be pulled into memory just to learn that it is not text.
            let (raw, complete) = read_head(&source, MAX_FILE_BYTES)
                .map_err(|error| Error::NotFound(format!("{}: {error}", source.display())))?;
            let bytes = metadata.len();
            let media = sniff(&raw[..raw.len().min(SNIFF_BYTES)]);
            // An image is not text: inlining its bytes would be mojibake and
            // the model could not look at the picture. Hand the path to the
            // runtime, which attaches it as an image content block.
            if media == Media::Image {
                return Ok(Prepared {
                    block: String::new(),
                    attachment: Some(source.clone()),
                    resolved: ResolvedRef {
                        kind: reference.kind,
                        path: path.to_string_lossy().to_string(),
                        label,
                        bytes,
                        chars: 0,
                        truncated: false,
                        outside_root,
                    },
                });
            }
            // Everything else that is not text is handed over as a path. The
            // agent opens a PDF, unzips a DOCX or runs `file` far more capably
            // than a byte sniff can guess, and refusing here would mean the
            // user cannot ask about a file they clearly attached.
            let text = match valid_prefix(&raw) {
                Some(text) if media == Media::Text => text,
                _ => {
                    let block = binary_block(&source, bytes);
                    return Ok(Prepared {
                        resolved: ResolvedRef {
                            kind: reference.kind,
                            path: path.to_string_lossy().to_string(),
                            label,
                            bytes,
                            chars: block.chars().count(),
                            truncated: false,
                            outside_root,
                        },
                        block,
                        attachment: None,
                    });
                }
            };
            let (body, cut_by_budget) = truncate(text, MAX_FILE_BYTES);
            // A file longer than the head we read is truncated too, even when
            // what arrived fits the budget.
            let truncated = cut_by_budget || !complete;
            let label_text = if reference.kind == ContextKind::Skill {
                let name = label.clone().unwrap_or_else(|| {
                    source
                        .parent()
                        .and_then(|dir| dir.file_name())
                        .map(|name| name.to_string_lossy().to_string())
                        .unwrap_or_default()
                });
                format!("skill {name} ({})", source.display())
            } else {
                format!("file {}", path.display())
            };
            let mut block = format!("--- {label_text} ---\n{body}\n--- end ---");
            if truncated {
                block.push_str(&format!(
                    "\n<!-- truncated at {MAX_FILE_BYTES} bytes of {bytes} -->"
                ));
            }
            Ok(Prepared {
                block,
                resolved: ResolvedRef {
                    kind: reference.kind,
                    path: path.to_string_lossy().to_string(),
                    label,
                    bytes,
                    chars: body.chars().count(),
                    truncated,
                    outside_root,
                },
                attachment: None,
            })
        }
    }
}

fn listing(directory: &Path) -> Result<(String, bool)> {
    let mut names: Vec<String> = std::fs::read_dir(directory)?
        .flatten()
        .map(|entry| {
            let name = entry.file_name().to_string_lossy().to_string();
            let is_dir = entry.path().is_dir();
            if is_dir { format!("{name}/") } else { name }
        })
        .collect();
    names.sort();
    let total = names.len();
    let truncated = total > MAX_DIR_ENTRIES;
    names.truncate(MAX_DIR_ENTRIES);
    let mut text = names.join("\n");
    if truncated {
        text.push_str(&format!(
            "\n(+{} more entries not listed)",
            total - MAX_DIR_ENTRIES
        ));
    }
    Ok((text, truncated))
}

/// Read at most `limit` bytes plus room for one cut character.
///
/// `false` in the second slot means the file is longer than what came back, so
/// the caller can mark the block as truncated without ever loading the rest.
fn read_head(path: &Path, limit: usize) -> std::io::Result<(Vec<u8>, bool)> {
    use std::io::Read;
    let mut file = std::fs::File::open(path)?;
    let mut buffer = vec![0u8; limit + 4];
    let mut filled = 0;
    while filled < buffer.len() {
        match file.read(&mut buffer[filled..])? {
            0 => break,
            read => filled += read,
        }
    }
    let complete = if filled < buffer.len() {
        true
    } else {
        let mut probe = [0u8; 1];
        file.read(&mut probe)? == 0
    };
    buffer.truncate(filled);
    Ok((buffer, complete))
}

/// The longest prefix of `raw` that is valid UTF-8.
///
/// A read that stopped mid-character leaves at most three trailing bytes, which
/// are dropped. Malformed content anywhere else returns `None`: that is a
/// binary file, not text that happens to end early.
fn valid_prefix(raw: &[u8]) -> Option<&str> {
    if let Ok(text) = std::str::from_utf8(raw) {
        return Some(text);
    }
    for cut in 1..=3.min(raw.len()) {
        if let Ok(text) = std::str::from_utf8(&raw[..raw.len() - cut]) {
            return Some(text);
        }
    }
    None
}

/// Cut on a character boundary; a multi-byte character is never split.
fn truncate(text: &str, limit: usize) -> (String, bool) {
    if text.len() <= limit {
        return (text.to_string(), false);
    }
    let mut end = limit;
    while end > 0 && !text.is_char_boundary(end) {
        end -= 1;
    }
    (text[..end].to_string(), true)
}

fn canonical_dir(path: &str) -> Result<PathBuf> {
    let trimmed = path.trim();
    if trimmed.is_empty() {
        return Err(Error::Rejected(
            "a session without a project directory cannot take context references".into(),
        ));
    }
    std::fs::canonicalize(trimmed)
        .map_err(|error| Error::NotFound(format!("project directory {trimmed}: {error}")))
}

fn resolve_path(root: &Path, reference: &str) -> Result<PathBuf> {
    let candidate = Path::new(reference);
    let joined = if candidate.is_absolute() {
        candidate.to_path_buf()
    } else {
        root.join(candidate)
    };
    std::fs::canonicalize(&joined)
        .map_err(|error| Error::NotFound(format!("{}: {error}", joined.display())))
}

/// Whether `path` is inside `root`, after both were canonicalised.
///
/// Windows paths compare case-insensitively; on every platform the comparison is
/// by component so `D:\work\anvil` does not contain `D:\work\anvil-other`.
pub(crate) fn is_within(root: &Path, path: &Path) -> bool {
    let Ok(root) = std::fs::canonicalize(root) else {
        return false;
    };
    let root_parts: Vec<String> = root
        .components()
        .map(|part| part.as_os_str().to_string_lossy().to_lowercase())
        .collect();
    let path_parts: Vec<String> = path
        .components()
        .map(|part| part.as_os_str().to_string_lossy().to_lowercase())
        .collect();
    path_parts.len() >= root_parts.len() && path_parts[..root_parts.len()] == root_parts[..]
}

fn same_path(left: &Path, right: &Path) -> bool {
    left.to_string_lossy().to_lowercase() == right.to_string_lossy().to_lowercase()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    fn scratch(name: &str) -> PathBuf {
        let root = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("context")
            .join(name);
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(root.join("src")).expect("create scratch");
        std::fs::write(root.join("src/main.rs"), "fn main() {}\n").expect("write");
        std::fs::write(root.join("README.md"), "# demo\n").expect("write");
        std::fs::create_dir_all(root.join("src/nested")).expect("create");
        root
    }

    fn file(path: &str) -> ContextRef {
        ContextRef {
            kind: ContextKind::File,
            path: path.to_string(),
            label: None,
        }
    }

    #[test]
    fn a_relative_reference_is_read_inside_the_project() {
        let root = scratch("relative");
        let steps = plan(&root.to_string_lossy(), &[], &[file("src/main.rs")]).expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert!(!prepared.resolved.outside_root);
                assert!(prepared.block.contains("fn main"));
                assert_eq!(prepared.resolved.chars, "fn main() {}\n".chars().count());
                assert!(!prepared.resolved.truncated);
            }
            Step::Awaiting { .. } => panic!("inside the project must not ask"),
        }
    }

    #[test]
    fn a_parent_escape_is_not_read_without_consent() {
        let root = scratch("escape");
        let outside = root.parent().expect("parent").join("secret.txt");
        std::fs::write(&outside, "top-secret").expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("../secret.txt")]).expect("plan");
        match &steps[0] {
            Step::Awaiting { path, .. } => {
                assert!(path.ends_with("secret.txt"));
            }
            Step::Ready(_) => panic!("a path outside the project must need approval"),
        }
        let _ = std::fs::remove_file(&outside);
    }

    #[test]
    fn an_unreadable_binary_is_handed_over_as_a_path_not_refused() {
        // Its own scratch directory: two cases sharing one would delete each
        // other's files when cargo runs the module in parallel.
        let root = scratch("binary-path");
        let report = root.join("report.pdf");
        let payload = b"%PDF-1.7\n\x00\x01\x02binary";
        std::fs::write(&report, payload).expect("write");
        // The agent opens the file with its own tools; the prompt carries the
        // path and says why the bytes were not inlined.
        let steps = plan(&root.to_string_lossy(), &[], &[file("report.pdf")]).expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert!(prepared.attachment.is_none(), "a PDF is not an image");
                assert!(prepared.block.contains("report.pdf"), "{}", prepared.block);
                assert!(prepared.block.contains("binary"), "{}", prepared.block);
                assert!(
                    prepared.block.contains("your own tools"),
                    "{}",
                    prepared.block
                );
                assert_eq!(prepared.resolved.bytes, payload.len() as u64);
            }
            Step::Awaiting { .. } => panic!("inside the project must not ask"),
        }
    }

    #[test]
    fn a_binary_outside_the_project_still_asks_before_it_is_described() {
        let root = scratch("binary-outside");
        let outside = root.parent().expect("parent").join("outside.pdf");
        std::fs::write(&outside, b"%PDF-1.7\n\x00\x01").expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("../outside.pdf")]).expect("plan");
        assert!(
            matches!(steps[0], Step::Awaiting { .. }),
            "a path outside the project needs consent whatever it contains"
        );
        let _ = std::fs::remove_file(&outside);
    }

    /// Only the head is read, so the truncation has to be reported from the
    /// bytes on disk rather than from what happened to be buffered.
    #[test]
    fn a_text_file_longer_than_the_budget_is_marked_truncated() {
        let root = scratch("long-text");
        std::fs::write(root.join("big.txt"), "x".repeat(MAX_FILE_BYTES * 2)).expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("big.txt")]).expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert!(prepared.resolved.truncated);
                assert_eq!(prepared.resolved.chars, MAX_FILE_BYTES);
                assert!(
                    prepared.block.contains("truncated at"),
                    "{}",
                    prepared.block
                );
            }
            Step::Awaiting { .. } => panic!("inside the project"),
        }
    }

    /// The head read stops at a byte boundary; a character straddling it must be
    /// dropped, not decoded into a replacement character.
    #[test]
    fn a_character_cut_by_the_head_read_is_dropped_not_mangled() {
        let root = scratch("cut-char");
        let mut body = "a".repeat(MAX_FILE_BYTES + 3);
        body.push('中');
        std::fs::write(root.join("cut.txt"), &body).expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("cut.txt")]).expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert!(!prepared.block.contains('中'), "{}", prepared.block);
                assert!(!prepared.block.contains('\u{fffd}'), "{}", prepared.block);
                assert!(prepared.resolved.truncated);
            }
            Step::Awaiting { .. } => panic!("inside the project"),
        }
    }

    /// A real PNG header. The runtime turns it into an image content block, so
    /// the reference has to leave here as an attachment instead of being
    /// appended to the prompt as bytes that are not text.
    fn png_header() -> Vec<u8> {
        let mut bytes = vec![0x89, b'P', b'N', b'G', 0x0d, 0x0a, 0x1a, 0x0a];
        bytes.extend_from_slice(&13u32.to_be_bytes());
        bytes.extend_from_slice(b"IHDR");
        bytes.extend_from_slice(&1u32.to_be_bytes());
        bytes.extend_from_slice(&1u32.to_be_bytes());
        bytes.extend_from_slice(&[8, 6, 0, 0, 0]);
        bytes
    }

    #[test]
    fn an_image_becomes_an_attachment_instead_of_prompt_text() {
        let root = scratch("image");
        std::fs::write(root.join("shot.png"), png_header()).expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("shot.png")]).expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                let expected = std::fs::canonicalize(root.join("shot.png")).expect("canonical");
                assert_eq!(prepared.attachment.as_deref(), Some(expected.as_path()));
                assert!(prepared.block.is_empty(), "an image is not inline text");
                assert_eq!(prepared.resolved.chars, 0);
            }
            Step::Awaiting { .. } => panic!("the image is inside the project"),
        }
    }

    #[test]
    fn an_image_outside_the_project_still_asks_before_it_is_attached() {
        let root = scratch("image-outside");
        let outside = root.parent().expect("parent").join("outside.png");
        std::fs::write(&outside, png_header()).expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("../outside.png")]).expect("plan");
        assert!(
            matches!(steps[0], Step::Awaiting { .. }),
            "trust is about the path, not the medium"
        );
        let _ = std::fs::remove_file(&outside);
    }

    #[test]
    fn folding_an_image_only_turn_leaves_the_prompt_untouched() {
        let root = scratch("image-fold");
        std::fs::write(root.join("shot.png"), png_header()).expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("shot.png")]).expect("plan");
        let prepared: Vec<Prepared> = steps
            .into_iter()
            .filter_map(|step| match step {
                Step::Ready(prepared) => Some(*prepared),
                Step::Awaiting { .. } => None,
            })
            .collect();
        assert_eq!(attachments(&prepared).len(), 1);
        assert_eq!(fold("看看这张图", &prepared), "看看这张图");
    }

    #[test]
    fn duplicate_references_collapse_to_one_block() {
        let root = scratch("dupe");
        let steps = plan(
            &root.to_string_lossy(),
            &[],
            &[file("src/main.rs"), file("src/./main.rs")],
        )
        .expect("plan");
        assert_eq!(steps.len(), 1);
    }

    /// The bytes never reach the prompt, so a .bin cannot turn into mojibake
    /// text the model would quote back as file content.
    #[test]
    fn binary_bytes_are_never_inlined_as_mojibake() {
        let root = scratch("binary");
        let mut handle = std::fs::File::create(root.join("blob.bin")).expect("create");
        handle.write_all(&[0xff, 0xfe, 0x00, 0x01]).expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("blob.bin")]).expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert!(prepared.attachment.is_none());
                assert!(prepared.block.contains("blob.bin"));
                assert!(
                    !prepared.block.contains('\u{fffd}'),
                    "invalid bytes must not be decoded"
                );
            }
            Step::Awaiting { .. } => panic!("inside the project"),
        }
    }

    #[test]
    fn a_missing_reference_is_not_found_not_an_empty_block() {
        let root = scratch("missing");
        let error = plan(&root.to_string_lossy(), &[], &[file("nope.rs")]).expect_err("missing");
        assert_eq!(error.code(), "not_found");
    }

    #[test]
    fn a_directory_lists_names_and_says_when_it_was_cut() {
        let root = scratch("listing");
        for index in 0..(MAX_DIR_ENTRIES + 3) {
            std::fs::write(root.join(format!("src/f{index}.rs")), "x").expect("write");
        }
        let steps = plan(
            &root.to_string_lossy(),
            &[],
            &[ContextRef {
                kind: ContextKind::Dir,
                path: "src".into(),
                label: None,
            }],
        )
        .expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert!(prepared.resolved.truncated);
                assert!(prepared.block.contains("more entries not listed"));
            }
            Step::Awaiting { .. } => panic!("inside the project"),
        }
    }

    #[test]
    fn a_skill_outside_the_configured_roots_is_refused() {
        let root = scratch("skill");
        let skill = ContextRef {
            kind: ContextKind::Skill,
            path: "README.md".into(),
            label: Some("demo".into()),
        };
        let error =
            plan(&root.to_string_lossy(), &[], std::slice::from_ref(&skill)).expect_err("no roots");
        assert_eq!(error.code(), "rejected");
        let steps = plan(
            &root.to_string_lossy(),
            std::slice::from_ref(&root),
            &[skill],
        )
        .expect("with a root");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert_eq!(prepared.resolved.kind, ContextKind::Skill);
                assert!(prepared.block.starts_with("--- skill demo ("));
            }
            Step::Awaiting { .. } => panic!("a skill root covers it"),
        }
    }

    #[test]
    fn folding_keeps_the_user_words_last() {
        let root = scratch("fold");
        let steps = plan(&root.to_string_lossy(), &[], &[file("README.md")]).expect("plan");
        let prepared: Vec<Prepared> = steps
            .into_iter()
            .filter_map(|step| match step {
                Step::Ready(prepared) => Some(*prepared),
                Step::Awaiting { .. } => None,
            })
            .collect();
        let prompt = fold("what is this?", &prepared);
        assert!(prompt.starts_with("The user attached the following context."));
        assert!(prompt.ends_with("what is this?"));
    }

    #[test]
    fn the_at_list_is_bounded_and_skips_vendor_directories() {
        let root = scratch("entries");
        std::fs::create_dir_all(root.join("node_modules/pkg")).expect("create");
        std::fs::write(root.join("node_modules/pkg/index.js"), "x").expect("write");
        let listed = entries(&root.to_string_lossy(), "", 20).expect("entries");
        assert!(
            listed
                .iter()
                .all(|entry| !entry.relative.contains("node_modules"))
        );
        assert!(listed.iter().any(|entry| entry.relative == "src/main.rs"));
        let narrowed = entries(&root.to_string_lossy(), "main", 5).expect("entries");
        assert!(narrowed.iter().all(|entry| entry.relative.contains("main")));
    }
}
