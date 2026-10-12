package tarbundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
)

// IsArchive reports a zip, gzip or tar upload by its magic bytes.
func IsArchive(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte("PK\x03\x04")) || bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) ||
		(len(raw) >= 262 && string(raw[257:262]) == "ustar")
}

// ReadArchive reads an uploaded tar, tar.gz or zip archive (whatever tool
// made it: PAX/GNU headers are fine here, unlike in a bundle) into files,
// dropping Junk entries and directories. A link or other non-regular
// entry, a name that is not a bundle path ("../x" and "/abs" are refused,
// never rewritten), a duplicate, or the caps of l fail the read; an archive
// bomb stops at the cap and never expands fully. The files are then built
// into a bundle (PackFiles), which is what is stored and served.
func ReadArchive(raw []byte, l Limits) (map[string][]byte, error) {
	if int64(len(raw)) > l.MaxArchiveBytes() {
		return nil, fmt.Errorf("archive exceeds %d bytes", l.MaxArchiveBytes())
	}
	files := map[string][]byte{}
	total := 0
	add := func(name string, r io.Reader, size int64) error {
		name = uploadName(name)
		if Junk(name) {
			return nil
		}
		if !core.ValidBundlePath(name) {
			return fmt.Errorf("%q: file names may use letters, digits, '.', '_' and '-' only, inside the archive", name)
		}
		if len(files) >= l.MaxFiles {
			return fmt.Errorf("archive holds more than %d files", l.MaxFiles)
		}
		if size > int64(l.MaxBytes-total) {
			return fmt.Errorf("archive content exceeds %d bytes", l.MaxBytes)
		}
		data, err := io.ReadAll(io.LimitReader(r, int64(l.MaxBytes-total)+1))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if total += len(data); total > l.MaxBytes {
			return fmt.Errorf("archive content exceeds %d bytes", l.MaxBytes)
		}
		if _, dup := files[name]; dup {
			return fmt.Errorf("%q appears twice", name)
		}
		files[name] = data
		return nil
	}
	switch {
	case bytes.HasPrefix(raw, []byte("PK\x03\x04")):
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return nil, fmt.Errorf("not a valid zip archive: %w", err)
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if !f.Mode().IsRegular() {
				if Junk(uploadName(f.Name)) {
					continue
				}
				return nil, fmt.Errorf("%q is not a regular file", f.Name)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			err = add(f.Name, rc, int64(f.UncompressedSize64))
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
	default:
		var r io.Reader = bytes.NewReader(raw)
		if bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) {
			gz, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				return nil, fmt.Errorf("not a valid gzip archive: %w", err)
			}
			r = gz
		}
		tr := tar.NewReader(r)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("not a valid tar archive: %w", err)
			}
			switch hdr.Typeflag {
			case tar.TypeDir, tar.TypeXGlobalHeader:
				continue
			case tar.TypeReg, '\x00':
			default:
				if Junk(uploadName(hdr.Name)) {
					continue
				}
				return nil, fmt.Errorf("%q is not a regular file", hdr.Name)
			}
			if err := add(hdr.Name, tr, hdr.Size); err != nil {
				return nil, err
			}
		}
	}
	if len(files) == 0 {
		return nil, errors.New("the archive holds no files")
	}
	return files, nil
}

// uploadName is an upload entry's name as a bundle path: a Windows "\" read
// as "/" and a leading "./" dropped. Nothing else is rewritten: a name that
// is still not a bundle path is refused.
func uploadName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	return name
}
