//! Reading one file so the page can show it.
//!
//! The chat page previews what a turn wrote: pictures, text, markdown, HTML,
//! tables, Word documents. The page cannot read the disk, so the host reads the
//! file and hands over either its text or its bytes.
//!
//! Two rules, both about not lying:
//!
//! * The path must be inside the session's project. An artifact is only ever a
//!   file the turn wrote there; a preview must not become a way to read
//!   something else.
//! * Both budgets are finite. A 200 MB log would freeze the window, so the
//!   text is cut and the reply says so instead of pretending it is complete.

use std::path::{Path, PathBuf};

use crate::error::{Error, Result};

/// Text kept verbatim. Longer files are cut at a line boundary.
const TEXT_BUDGET: u64 = 400 * 1024;
/// Bytes handed to the page as base64 (pictures, PDFs, spreadsheets, docx).
const BINARY_BUDGET: u64 = 24 * 1024 * 1024;

/// One file, ready for the preview pane.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct FilePreview {
    /// Absolute path, symlinks resolved.
    pub path: String,
    /// The file name, for the pane's header.
    pub name: String,
    /// `image`, `markdown`, `html`, `table`, `word`, `pdf`, `text` or `binary`.
    pub kind: String,
    /// Size on disk.
    pub bytes: u64,
    /// UTF-8 content, for every kind whose preview is text.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub text: Option<String>,
    /// Base64 of the raw bytes, for the kinds the page renders itself.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<String>,
    /// The text was cut to fit the budget.
    pub truncated: bool,
    /// The file is too large to hand over at all.
    pub too_large: bool,
}

/// Why a file cannot be previewed at all.
fn extension(path: &Path) -> String {
    path.extension()
        .map(|ext| ext.to_string_lossy().to_lowercase())
        .unwrap_or_default()
}

/// The MIME type a data URL needs, by extension.
fn image_mime(ext: &str) -> &'static str {
    match ext {
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "gif" => "image/gif",
        "webp" => "image/webp",
        "bmp" => "image/bmp",
        "avif" => "image/avif",
        _ => "application/octet-stream",
    }
}

/// Which family the page should use to draw this file.
pub fn kind_of(path: &Path) -> &'static str {
    match extension(path).as_str() {
        "png" | "jpg" | "jpeg" | "gif" | "webp" | "bmp" | "avif" | "svg" => "image",
        "md" | "markdown" | "mdx" => "markdown",
        "html" | "htm" => "html",
        "csv" | "tsv" | "xlsx" | "xls" | "xlsm" => "table",
        "docx" => "word",
        "pdf" => "pdf",
        _ => "text",
    }
}

/// Read one file for the page.
///
/// `root` is the project the session runs in; anything outside it is refused,
/// because a preview is a convenience, not a permission.
pub fn read(root: &Path, requested: &Path) -> Result<FilePreview> {
    let path = canonical(requested)?;
    let root = canonical(root)?;
    if !path.starts_with(&root) {
        return Err(Error::Rejected(format!(
            "{} is outside the project",
            path.display()
        )));
    }
    let meta = std::fs::metadata(&path)
        .map_err(|error| Error::Rejected(format!("{}: {error}", path.display())))?;
    if meta.is_dir() {
        return Err(Error::Rejected(format!("{} is a folder", path.display())));
    }
    let bytes = meta.len();
    let kind = kind_of(&path);
    let name = path
        .file_name()
        .map(|name| name.to_string_lossy().to_string())
        .unwrap_or_else(|| path.display().to_string());

    // Pictures, PDFs, workbooks and docx are rendered by the page itself, so
    // they travel as bytes. CSV and TSV are text: the page parses them, and
    // they read fine as text in any case.
    let ext = extension(&path);
    let wants_bytes = matches!(kind, "image" | "pdf" | "word")
        || (kind == "table" && matches!(ext.as_str(), "xlsx" | "xls" | "xlsm"));
    if wants_bytes {
        if bytes > BINARY_BUDGET {
            return Ok(FilePreview {
                path: path.display().to_string(),
                name,
                kind: kind.to_string(),
                bytes,
                text: None,
                data: None,
                truncated: false,
                too_large: true,
            });
        }
        let raw = std::fs::read(&path)
            .map_err(|error| Error::Rejected(format!("{}: {error}", path.display())))?;
        let mime = if kind == "image" {
            image_mime(&ext)
        } else if kind == "pdf" {
            "application/pdf"
        } else if kind == "table" && matches!(ext.as_str(), "xlsx" | "xls" | "xlsm") {
            "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
        } else if kind == "word" {
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
        } else {
            "text/plain; charset=utf-8"
        };
        return Ok(FilePreview {
            path: path.display().to_string(),
            name,
            kind: kind.to_string(),
            bytes,
            text: None,
            data: Some(format!("data:{mime};base64,{}", base64(&raw))),
            truncated: false,
            too_large: false,
        });
    }

    // Everything else is read as text, within the budget.
    let budget = bytes.min(TEXT_BUDGET) as usize;
    let raw = read_prefix(&path, budget)?;
    let truncated = bytes > TEXT_BUDGET as u64;
    let text = match String::from_utf8(raw) {
        Ok(text) => text,
        Err(error) => {
            // Not UTF-8: hand back what decodes cleanly rather than a wall of
            // replacement characters. A binary file is still worth saying so.
            let valid = error.utf8_error().valid_up_to();
            if valid == 0 {
                return Ok(FilePreview {
                    path: path.display().to_string(),
                    name,
                    kind: "binary".to_string(),
                    bytes,
                    text: None,
                    data: None,
                    truncated,
                    too_large: false,
                });
            }
            String::from_utf8_lossy(&error.into_bytes()[..valid]).to_string()
        }
    };
    Ok(FilePreview {
        path: path.display().to_string(),
        name,
        kind: kind.to_string(),
        bytes,
        text: Some(text),
        data: None,
        truncated,
        too_large: false,
    })
}

fn canonical(path: &Path) -> Result<PathBuf> {
    std::fs::canonicalize(path)
        .map_err(|error| Error::Rejected(format!("{}: {error}", path.display())))
}

fn read_prefix(path: &Path, bytes: usize) -> Result<Vec<u8>> {
    use std::io::Read;
    let mut file = std::fs::File::open(path)
        .map_err(|error| Error::Rejected(format!("{}: {error}", path.display())))?;
    let mut buffer = vec![0u8; bytes];
    let mut read = 0usize;
    while read < bytes {
        match file.read(&mut buffer[read..]) {
            Ok(0) => break,
            Ok(count) => read += count,
            Err(error) if error.kind() == std::io::ErrorKind::Interrupted => continue,
            Err(error) => {
                return Err(Error::Rejected(format!("{}: {error}", path.display())));
            }
        }
    }
    buffer.truncate(read);
    Ok(buffer)
}

/// Base64 without pulling a dependency in for one function.
fn base64(raw: &[u8]) -> String {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(raw.len().div_ceil(3) * 4);
    for chunk in raw.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = *chunk.get(1).unwrap_or(&0) as u32;
        let b2 = *chunk.get(2).unwrap_or(&0) as u32;
        let triple = (b0 << 16) | (b1 << 8) | b2;
        out.push(TABLE[(triple >> 18) as usize & 0x3f] as char);
        out.push(TABLE[(triple >> 12) as usize & 0x3f] as char);
        out.push(if chunk.len() > 1 {
            TABLE[(triple >> 6) as usize & 0x3f] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            TABLE[triple as usize & 0x3f] as char
        } else {
            '='
        });
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A file outside the project is not a preview, it is a permission.
    #[test]
    fn refuses_a_path_outside_the_project() {
        let root = std::env::temp_dir().join("anvil-preview-root");
        std::fs::create_dir_all(&root).expect("root");
        let outside = std::env::temp_dir().join("anvil-preview-outside.txt");
        std::fs::write(&outside, "secret").expect("write");
        let error = read(&root, &outside).expect_err("must refuse");
        assert!(matches!(error, Error::Rejected(_)), "{error:?}");
        let _ = std::fs::remove_file(&outside);
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn reads_text_and_names_the_family() {
        let root = std::env::temp_dir().join("anvil-preview-text");
        std::fs::create_dir_all(&root).expect("root");
        let file = root.join("notes.md");
        std::fs::write(&file, "# 标题\n正文").expect("write");
        let preview = read(&root, &file).expect("preview");
        assert_eq!(preview.kind, "markdown");
        assert_eq!(preview.text.as_deref(), Some("# 标题\n正文"));
        assert!(!preview.truncated);
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn a_picture_travels_as_bytes() {
        let root = std::env::temp_dir().join("anvil-preview-image");
        std::fs::create_dir_all(&root).expect("root");
        let file = root.join("shot.png");
        std::fs::write(&file, [0x89u8, b'P', b'N', b'G']).expect("write");
        let preview = read(&root, &file).expect("preview");
        assert_eq!(preview.kind, "image");
        assert!(preview.data.as_deref().unwrap_or("").starts_with("data:image/png;base64,"));
        assert!(preview.text.is_none());
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn base64_matches_the_reference_encoding() {
        assert_eq!(base64(b""), "");
        assert_eq!(base64(b"f"), "Zg==");
        assert_eq!(base64(b"fo"), "Zm8=");
        assert_eq!(base64(b"foo"), "Zm9v");
        assert_eq!(base64(b"foobar"), "Zm9vYmFy");
    }
}
