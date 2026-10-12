// Package tarbundle is the one reader/writer of bnbloc file bundles: an
// uncompressed POSIX ustar archive of regular files whose every path passes
// core.ValidBundlePath. Content component bundles (experts, skill packs:
// internal/contentpkg, ContentLimits) use it too. Two more formats share it,
// differing only in caps:
// previewer component bundles (bnbloc.previewer.package/v1 artifact
// "bundle"; the gateway parses them to serve and verify, the CLI packs a
// directory) and site snapshots (a "site" artifact version: the bridge packs
// a static web app from session files, the gateway serves it). The desktop
// shell has its own reader of the previewer format (apps/desktop/src-tauri),
// and the reference previewer's build script its own writer.
//
// It is also the only Go code that reads archives
// (TestOnlyTarbundleReadsArchives): a stored bundle through
// Cache.LoadPinned, an upload of a foreign format through ReadArchive; and
// the one directory packer (PackEntries, PackFS, Pack) with one exclusion
// rule (Entry.Packed: junk, hidden files, core.ProtectedWorkspacePath).
package tarbundle

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/digest"
)

// Limits caps one bundle's files.
type Limits struct {
	MaxBytes int
	MaxFiles int
}

// PreviewerLimits are the previewer bundle caps (PRD "Previewer contract
// details").
var PreviewerLimits = Limits{MaxBytes: 16 << 20, MaxFiles: 512}

// SiteLimits are the site snapshot caps (PRD 2026-10-09 "Site snapshots").
var SiteLimits = Limits{MaxBytes: 64 << 20, MaxFiles: 2000}

// ContentLimits are the content component bundle caps (experts and skill
// packs, docs/prd/2026-10-12-experts-and-teams.md): mostly text, with room
// for a skill's scripts and reference files.
var ContentLimits = Limits{MaxBytes: 8 << 20, MaxFiles: 512}

// SiteMIME is the media type of a site snapshot version's blob (a bundle
// within SiteLimits).
const SiteMIME = "application/vnd.bnbloc.site+tar"

// MaxArchiveBytes bounds the archive size of a bundle within l: the files
// plus one header block each and the trailer.
func (l Limits) MaxArchiveBytes() int64 {
	return int64(l.MaxBytes) + int64(l.MaxFiles)*1024 + 10240
}

// Bundle is a parsed, verified archive.
type Bundle struct {
	SHA256 string
	files  map[string][]byte
}

// File returns the bytes of one bundle file.
func (b *Bundle) File(p string) ([]byte, bool) {
	data, ok := b.files[p]
	return data, ok
}

// Paths lists the bundle's files (sorted).
func (b *Bundle) Paths() []string {
	out := make([]string, 0, len(b.files))
	for p := range b.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Size is the total byte size of the bundle's files.
func (b *Bundle) Size() int {
	n := 0
	for _, data := range b.files {
		n += len(data)
	}
	return n
}

// ErrDigest reports archive bytes that do not match the pinned sha256.
var ErrDigest = errors.New("bundle does not match its sha256")

// Parse verifies raw against wantSHA256 (when non-empty: a bundle this
// process just packed or received for validation has no pin yet; a stored
// bundle is read only through Cache.LoadPinned) and decodes it.
// Only regular files and directories are accepted; every file path must be a
// valid bundle path, unique, and the caps hold.
func Parse(raw []byte, wantSHA256 string, l Limits) (*Bundle, error) {
	if int64(len(raw)) > l.MaxArchiveBytes() {
		return nil, fmt.Errorf("bundle exceeds %d bytes", l.MaxBytes)
	}
	sum := digest.Of(raw)
	if wantSHA256 != "" && sum != wantSHA256 {
		return nil, ErrDigest
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	b := &Bundle{SHA256: sum, files: map[string][]byte{}}
	total := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bundle is not a valid tar archive: %w", err)
		}
		// Plain ustar only: the desktop's reader (and the contract) accept no
		// PAX/GNU extension headers, so a bundle installs only if every
		// client can read it.
		if hdr.Format != tar.FormatUSTAR {
			return nil, fmt.Errorf("bundle: %q is not a plain ustar entry (no PAX/GNU extensions)", hdr.Name)
		}
		name := hdr.Name
		switch hdr.Typeflag {
		case tar.TypeDir:
			if dir := strings.TrimSuffix(name, "/"); dir != "" && !core.ValidBundlePath(dir) {
				return nil, fmt.Errorf("bundle: invalid directory %q", hdr.Name)
			}
			continue
		case tar.TypeReg, '\x00': // '\x00': pre-POSIX regular file
		default:
			return nil, fmt.Errorf("bundle: %q is not a regular file", hdr.Name)
		}
		if !core.ValidBundlePath(name) {
			return nil, fmt.Errorf("bundle: invalid path %q", hdr.Name)
		}
		if _, dup := b.files[name]; dup {
			return nil, fmt.Errorf("bundle: duplicate path %q", name)
		}
		if len(b.files) >= l.MaxFiles {
			return nil, fmt.Errorf("bundle holds more than %d files", l.MaxFiles)
		}
		if hdr.Size < 0 || int64(total)+hdr.Size > int64(l.MaxBytes) {
			return nil, fmt.Errorf("bundle exceeds %d bytes", l.MaxBytes)
		}
		data, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil || int64(len(data)) != hdr.Size {
			return nil, fmt.Errorf("bundle: truncated %q", name)
		}
		total += len(data)
		b.files[name] = data
	}
	if len(b.files) == 0 {
		return nil, fmt.Errorf("bundle is empty")
	}
	return b, nil
}

// RequireEntry checks that the bundle holds the package's entry document.
func (b *Bundle) RequireEntry(entry string) error {
	if _, ok := b.files[entry]; !ok {
		return fmt.Errorf("bundle has no entry %q", entry)
	}
	return nil
}

// PackFiles archives files (bundle path -> bytes) into a deterministic
// bundle: sorted paths, mode 0644, zero times and owners. A path the bundle
// rules refuse, no files, or the caps fail the pack.
func PackFiles(files map[string][]byte, l Limits) ([]byte, error) {
	if len(files) == 0 {
		return nil, errors.New("bundle holds no files")
	}
	if len(files) > l.MaxFiles {
		return nil, fmt.Errorf("bundle holds more than %d files", l.MaxFiles)
	}
	paths := make([]string, 0, len(files))
	total := 0
	for p, data := range files {
		if !core.ValidBundlePath(p) {
			return nil, fmt.Errorf("%s is not a valid bundle path ([A-Za-z0-9._-] segments)", p)
		}
		if total += len(data); total > l.MaxBytes {
			return nil, fmt.Errorf("bundle exceeds %d bytes", l.MaxBytes)
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, rel := range paths {
		data := files[rel]
		hdr := &tar.Header{Name: rel, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// contentTypes is the code-owned extension -> Content-Type map for served
// bundle files; anything else is application/octet-stream.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".map":   "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".wasm":  "application/wasm",
	".txt":   "text/plain; charset=utf-8",
	// Common static web app types (site snapshots).
	".htm":         "text/html; charset=utf-8",
	".cjs":         "text/javascript; charset=utf-8",
	".webmanifest": "application/manifest+json",
	".xml":         "application/xml",
	".csv":         "text/csv; charset=utf-8",
	".md":          "text/markdown; charset=utf-8",
	".pdf":         "application/pdf",
	".ico":         "image/x-icon",
	".avif":        "image/avif",
	".bmp":         "image/bmp",
	".otf":         "font/otf",
	".mp4":         "video/mp4",
	".webm":        "video/webm",
	".mp3":         "audio/mpeg",
	".m4a":         "audio/mp4",
	".wav":         "audio/wav",
	".ogg":         "audio/ogg",
	".glb":         "model/gltf-binary",
	".gltf":        "model/gltf+json",
}

// ContentType is the Content-Type a bundle file is served with.
func ContentType(p string) string {
	if ct, ok := contentTypes[strings.ToLower(path.Ext(p))]; ok {
		return ct
	}
	return "application/octet-stream"
}
