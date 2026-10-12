//! Previewer bundles (`bnbloc.previewer.package/v1`): the pure, I/O-free half
//! of serving an installed previewer — path rules, sha256 verification, and a
//! deliberately small POSIX ustar reader.
//!
//! A previewer bundle is an uncompressed ustar archive of regular files (PRD
//! 2026-10-09 "Previewer contract details"): ≤ 16 MiB, ≤ 512 files, every
//! path made of `[A-Za-z0-9._-]` segments with no `.`/`..`. The archive is
//! parsed into memory once (after its sha256 matched the catalog pin) and
//! files are only ever served out of that map — the file system is never
//! consulted by path, so there is nothing to traverse.

use std::collections::HashMap;

/// Upper bound on the summed file bytes of one bundle.
pub(crate) const MAX_BUNDLE_BYTES: usize = 16 * 1024 * 1024;
/// Upper bound on regular files in one bundle.
pub(crate) const MAX_BUNDLE_FILES: usize = 512;
/// Upper bound on one path inside a bundle (ustar prefix + name).
const MAX_PATH_LEN: usize = 255;
const BLOCK: usize = 512;

/// A parsed, verified previewer bundle: path → file bytes.
#[derive(Debug, Default)]
pub(crate) struct Bundle {
    files: HashMap<String, Vec<u8>>,
}

impl Bundle {
    pub(crate) fn file(&self, path: &str) -> Option<&[u8]> {
        self.files.get(path).map(Vec::as_slice)
    }

    #[cfg(test)]
    pub(crate) fn len(&self) -> usize {
        self.files.len()
    }
}

/// One path segment of a bundle file: `[A-Za-z0-9._-]+`, not `.` or `..`.
fn valid_segment(segment: &str) -> bool {
    !segment.is_empty()
        && segment != "."
        && segment != ".."
        && segment
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'_' | b'-'))
}

/// A relative bundle path: valid segments joined by `/`, no leading or
/// trailing slash, no empty segment, bounded length. Percent-escapes are
/// refused by the segment alphabet (`%` is not in it).
pub(crate) fn valid_bundle_path(path: &str) -> bool {
    !path.is_empty() && path.len() <= MAX_PATH_LEN && path.split('/').all(valid_segment)
}

/// A package version (`pkgmeta` semverRE):
/// `^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`.
pub(crate) fn valid_package_version(version: &str) -> bool {
    if version.len() > 128 {
        return false;
    }
    let (core, suffix) = match version.find(['-', '+']) {
        Some(i) => (&version[..i], Some(&version[i + 1..])),
        None => (version, None),
    };
    let numeric = core.split('.').collect::<Vec<_>>();
    numeric.len() == 3
        && numeric
            .iter()
            .all(|part| !part.is_empty() && part.bytes().all(|b| b.is_ascii_digit()))
        && match suffix {
            None => true,
            Some(s) => {
                !s.is_empty()
                    && s.bytes()
                        .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-'))
            }
        }
}

/// NUL-terminated ASCII field (name / prefix / magic).
fn field_str(field: &[u8]) -> Result<&str, String> {
    let end = field.iter().position(|&b| b == 0).unwrap_or(field.len());
    std::str::from_utf8(&field[..end]).map_err(|_| "tar header field is not utf-8".to_string())
}

/// Octal numeric field, NUL/space terminated. Base-256 (GNU) is refused.
fn field_octal(field: &[u8]) -> Result<usize, String> {
    if field.first().is_some_and(|b| b & 0x80 != 0) {
        return Err("tar base-256 numeric field is not supported".into());
    }
    let text = field_str(field)?.trim_matches(|c| c == ' ' || c == '\0');
    if text.is_empty() {
        return Ok(0);
    }
    usize::from_str_radix(text, 8).map_err(|_| format!("tar numeric field {text:?} is not octal"))
}

fn header_checksum_ok(header: &[u8]) -> Result<bool, String> {
    let want = field_octal(&header[148..156])?;
    let got: usize = header
        .iter()
        .enumerate()
        .map(|(i, &b)| {
            if (148..156).contains(&i) {
                b' ' as usize
            } else {
                b as usize
            }
        })
        .sum();
    Ok(got == want)
}

/// Parses an uncompressed POSIX ustar archive into a bundle. Accepts regular
/// files (typeflag `0` / NUL) and directory entries (`5`, skipped); refuses
/// links, devices, pax/GNU extension headers, duplicate or invalid paths,
/// truncated archives, and anything over the size/count caps.
pub(crate) fn parse_ustar(raw: &[u8]) -> Result<Bundle, String> {
    let mut files: HashMap<String, Vec<u8>> = HashMap::new();
    let mut total = 0_usize;
    let mut offset = 0_usize;
    loop {
        let Some(header) = raw.get(offset..offset + BLOCK) else {
            return Err("tar archive is truncated (no end-of-archive block)".into());
        };
        if header.iter().all(|&b| b == 0) {
            break;
        }
        if !header_checksum_ok(header)? {
            return Err(format!("tar header checksum mismatch at offset {offset}"));
        }
        let name = field_str(&header[0..100])?;
        let size = field_octal(&header[124..136])?;
        let typeflag = header[156];
        let magic = &header[257..263];
        let prefix = if magic == b"ustar\0" || magic == b"ustar " {
            field_str(&header[345..500])?
        } else {
            ""
        };
        let path = if prefix.is_empty() {
            name.to_string()
        } else {
            format!("{prefix}/{name}")
        };
        offset += BLOCK;
        let data_blocks = size.div_ceil(BLOCK);
        match typeflag {
            b'0' | 0 => {
                if !valid_bundle_path(&path) {
                    return Err(format!("bundle path {path:?} is not allowed"));
                }
                if files.len() >= MAX_BUNDLE_FILES {
                    return Err(format!("bundle has more than {MAX_BUNDLE_FILES} files"));
                }
                total = total.saturating_add(size);
                if total > MAX_BUNDLE_BYTES {
                    return Err(format!("bundle exceeds {MAX_BUNDLE_BYTES} bytes"));
                }
                let Some(data) = raw.get(offset..offset + size) else {
                    return Err(format!("tar archive is truncated in {path:?}"));
                };
                if files.insert(path.clone(), data.to_vec()).is_some() {
                    return Err(format!("bundle path {path:?} appears twice"));
                }
            }
            b'5' => {
                let dir = path.trim_end_matches('/');
                if !valid_bundle_path(dir) {
                    return Err(format!("bundle directory {path:?} is not allowed"));
                }
            }
            other => {
                return Err(format!(
                    "tar entry {path:?} has unsupported type {:?} (regular files only)",
                    other as char
                ))
            }
        }
        offset = offset
            .checked_add(data_blocks * BLOCK)
            .filter(|&end| end <= raw.len())
            .ok_or_else(|| format!("tar archive is truncated in {path:?}"))?;
    }
    Ok(Bundle { files })
}

#[cfg(test)]
#[path = "previewer_bundle_tests.rs"]
pub(crate) mod previewer_bundle_tests;
