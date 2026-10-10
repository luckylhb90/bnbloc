use super::*;

/// One test tar entry: (name field, prefix field, typeflag, data).
pub(crate) struct Entry<'a> {
    pub(crate) name: &'a str,
    pub(crate) prefix: &'a str,
    pub(crate) typeflag: u8,
    pub(crate) data: &'a [u8],
}

/// A ustar header block for `e`, with a valid checksum.
fn header(e: &Entry<'_>) -> [u8; 512] {
    let mut h = [0_u8; 512];
    h[..e.name.len()].copy_from_slice(e.name.as_bytes());
    h[100..107].copy_from_slice(b"0000644");
    h[108..115].copy_from_slice(b"0000000");
    h[116..123].copy_from_slice(b"0000000");
    let size = format!("{:011o}", e.data.len());
    h[124..135].copy_from_slice(size.as_bytes());
    h[136..147].copy_from_slice(b"00000000000");
    h[156] = e.typeflag;
    h[257..263].copy_from_slice(b"ustar\0");
    h[263..265].copy_from_slice(b"00");
    h[345..345 + e.prefix.len()].copy_from_slice(e.prefix.as_bytes());
    h[148..156].copy_from_slice(b"        ");
    let sum: usize = h.iter().map(|&b| b as usize).sum();
    h[148..156].copy_from_slice(format!("{sum:06o}\0 ").as_bytes());
    h
}

/// A ustar archive of `entries` plus the two end-of-archive blocks.
pub(crate) fn tar(entries: &[Entry<'_>]) -> Vec<u8> {
    let mut out = Vec::new();
    for e in entries {
        out.extend_from_slice(&header(e));
        out.extend_from_slice(e.data);
        out.resize(out.len().div_ceil(512) * 512, 0);
    }
    out.extend_from_slice(&[0_u8; 1024]);
    out
}

/// A tar of regular files.
pub(crate) fn tar_files(files: &[(&str, &[u8])]) -> Vec<u8> {
    let entries: Vec<Entry<'_>> = files
        .iter()
        .map(|(name, data)| Entry {
            name,
            prefix: "",
            typeflag: b'0',
            data,
        })
        .collect();
    tar(&entries)
}

#[test]
fn parses_regular_files_directories_and_the_prefix_field() {
    let raw = tar(&[
        Entry {
            name: "assets/",
            prefix: "",
            typeflag: b'5',
            data: b"",
        },
        Entry {
            name: "index.html",
            prefix: "",
            typeflag: b'0',
            data: b"<!doctype html>",
        },
        Entry {
            name: "main.js",
            prefix: "assets",
            typeflag: 0,
            data: &[7_u8; 700],
        },
    ]);
    let bundle = parse_ustar(&raw).expect("valid bundle");
    assert_eq!(bundle.len(), 2);
    assert_eq!(bundle.file("index.html"), Some(&b"<!doctype html>"[..]));
    assert_eq!(bundle.file("assets/main.js").map(<[u8]>::len), Some(700));
    assert_eq!(bundle.file("assets"), None);
}

#[test]
fn refuses_links_devices_and_extension_headers() {
    for typeflag in [b'1', b'2', b'3', b'4', b'6', b'x', b'g', b'L', b'K'] {
        let raw = tar(&[Entry {
            name: "index.html",
            prefix: "",
            typeflag,
            data: b"",
        }]);
        assert!(
            parse_ustar(&raw).is_err(),
            "typeflag {:?} must be refused",
            typeflag as char
        );
    }
}

#[test]
fn refuses_invalid_and_duplicate_paths() {
    for name in [
        "../x.html",
        "/etc/passwd",
        "a//b.js",
        "./index.html",
        "a/../b.js",
        "sp ace.js",
        "%2e%2e/x",
    ] {
        let raw = tar_files(&[(name, b"x")]);
        assert!(parse_ustar(&raw).is_err(), "{name} must be refused");
    }
    let raw = tar(&[Entry {
        name: "x.js",
        prefix: "..",
        typeflag: b'0',
        data: b"x",
    }]);
    assert!(parse_ustar(&raw).is_err(), "a .. prefix must be refused");
    let raw = tar_files(&[("a.js", b"1"), ("a.js", b"2")]);
    assert!(
        parse_ustar(&raw).is_err(),
        "duplicate paths must be refused"
    );
}

#[test]
fn refuses_truncated_archives_and_bad_checksums() {
    let raw = tar_files(&[("index.html", &[1_u8; 1000])]);
    for cut in [100, 512, 900, 1536] {
        assert!(
            parse_ustar(&raw[..cut]).is_err(),
            "cut at {cut} must be refused"
        );
    }
    let mut bad = raw.clone();
    bad[0] = b'j';
    assert!(
        parse_ustar(&bad).is_err(),
        "a corrupted header must fail its checksum"
    );
}

#[test]
fn enforces_the_file_count_and_size_caps() {
    let names: Vec<String> = (0..=MAX_BUNDLE_FILES).map(|i| format!("f{i}.js")).collect();
    let files: Vec<(&str, &[u8])> = names.iter().map(|n| (n.as_str(), &b""[..])).collect();
    assert!(
        parse_ustar(&tar_files(&files)).is_err(),
        "more than {MAX_BUNDLE_FILES} files"
    );
    assert!(parse_ustar(&tar_files(&files[..MAX_BUNDLE_FILES])).is_ok());

    let big = vec![0_u8; MAX_BUNDLE_BYTES / 2 + 1];
    assert!(parse_ustar(&tar_files(&[("a.bin", &big), ("b.bin", &big)])).is_err());
}

#[test]
fn sha256_pin_must_match() {
    let raw = tar_files(&[("index.html", b"hi")]);
    let pin = crate::internal_ext::sidecar_util::hex_lower(&Sha256::digest(&raw));
    assert!(verify_sha256(&raw, &pin).is_ok());
    assert!(verify_sha256(&raw, &pin.to_ascii_uppercase()).is_ok());
    let mut tampered = raw.clone();
    tampered[600] ^= 1;
    assert!(verify_sha256(&tampered, &pin).is_err());
    assert!(verify_sha256(&raw, "").is_err());
}

#[test]
fn package_name_version_and_path_rules() {
    assert!(valid_package_name("sprite-sheet") && valid_package_name("a.b_c-1"));
    for bad in ["", "Sprite", "-x", ".x", "a/b", "a%2f"] {
        assert!(!valid_package_name(bad), "{bad}");
    }
    assert!(
        valid_package_version("1.2.3")
            && valid_package_version("1.2.3-rc.1")
            && valid_package_version("0.0.1+build-7")
    );
    for bad in [
        "",
        "1.2",
        "1.2.3.4",
        "v1.2.3",
        "1.2.3-",
        "1..3",
        "1.2.3-a b",
    ] {
        assert!(!valid_package_version(bad), "{bad}");
    }
    assert!(valid_bundle_path("index.html") && valid_bundle_path("assets/app.min.js"));
    for bad in [
        "", "/a", "a/", "a//b", ".", "..", "a/./b", "a/../b", "%2e%2e/a", "a\\b", "a?b",
    ] {
        assert!(!valid_bundle_path(bad), "{bad}");
    }
}
