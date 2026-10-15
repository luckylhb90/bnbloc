// Package attachment is the one owner of what an uploaded file may be and
// how it is served: a chat attachment (realtime) and an interactive app's
// file (the room service's app data) both go through it. A file's type
// comes from wire.AttachmentTypes by its name (the declared Content-Type
// only when the name has no extension), its first bytes must pass the
// type's content check, and only sniff-verified raster images are served
// inline: every other file is an opaque, nosniff, sandboxed download.
package attachment

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MaxNameLen bounds a stored file name (its tail is kept).
const MaxNameLen = 512

// headLen is how much of a file its content check reads.
const headLen = 512

// Type resolves a file's type from wire.AttachmentTypes: by the name's
// extension, else (a name without one) by its declared Content-Type. The
// declared type never overrides the name, and neither is trusted without
// ContentMatches.
func Type(filename, declared string) (wire.AttachmentType, bool) {
	ext := strings.ToLower(filepath.Ext(filename))
	for _, t := range wire.AttachmentTypes {
		if ext != "" && slices.Contains(t.Extensions, ext) {
			return t, true
		}
		if ext == "" && t.MIME == MediaType(declared) {
			return t, true
		}
	}
	return wire.AttachmentType{}, false
}

// oleMagic opens an OLE compound file (legacy Office documents).
var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// ContentMatches reports whether a file's first bytes pass its type's
// content check (wire.AttachmentContent*). Text must be text, not binary:
// markup is text too (a README opening with <!-- toc -->), and a text file
// is only ever served as an opaque, nosniff, sandboxed download, so its
// markup never renders.
func ContentMatches(content string, head []byte) bool {
	sniffed := MediaType(http.DetectContentType(head))
	switch content {
	case wire.AttachmentContentPNG:
		return sniffed == "image/png"
	case wire.AttachmentContentJPEG:
		return sniffed == "image/jpeg"
	case wire.AttachmentContentGIF:
		return sniffed == "image/gif"
	case wire.AttachmentContentWebP:
		return sniffed == "image/webp"
	case wire.AttachmentContentPDF:
		return sniffed == "application/pdf"
	case wire.AttachmentContentZip:
		return bytes.HasPrefix(head, []byte("PK\x03\x04")) || bytes.HasPrefix(head, []byte("PK\x05\x06"))
	case wire.AttachmentContentOLE:
		return bytes.HasPrefix(head, oleMagic)
	case wire.AttachmentContentText:
		return strings.HasPrefix(sniffed, "text/")
	case wire.AttachmentContentISOMedia:
		return len(head) >= 12 && string(head[4:8]) == "ftyp"
	case wire.AttachmentContentWebM:
		return bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3})
	case wire.AttachmentContentMP3:
		return bytes.HasPrefix(head, []byte("ID3")) || (len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0)
	case wire.AttachmentContentWAV:
		return len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WAVE"
	case wire.AttachmentContentOgg:
		return bytes.HasPrefix(head, []byte("OggS"))
	}
	return false
}

// The refusals of Check: a type not in the list, or content that is not
// its type. Their messages are sentences a person reads.
var (
	ErrType    = errors.New("file type not accepted")
	ErrContent = errors.New("file content does not match its type")
)

// Checked is a file that passed Check: its sanitized name, its type and its
// whole content (the head Check read, then the rest).
type Checked struct {
	Name string
	Type wire.AttachmentType
	Body io.Reader
}

// Check resolves a file's type and checks its first bytes. The error wraps
// ErrType or ErrContent with a sentence naming the file, or is a read
// failure.
func Check(filename, declared string, r io.Reader) (Checked, error) {
	name := SanitizeName(filename)
	t, ok := Type(name, declared)
	if !ok {
		return Checked{}, fmt.Errorf("%w: %s can't be attached: images, PDF, Office and OpenDocument files, text, audio, video and zip can", ErrType, name)
	}
	head := make([]byte, headLen)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Checked{}, err
	}
	head = head[:n]
	if !ContentMatches(t.Content, head) {
		return Checked{}, fmt.Errorf("%w: %s can't be attached: its content is not a %s file", ErrContent, name, t.Extensions[0])
	}
	return Checked{Name: name, Type: t, Body: io.MultiReader(bytes.NewReader(head), r)}, nil
}

// Refusal reports err as a Check refusal and its sentence (without the
// sentinel's prefix).
func Refusal(err error) (string, bool) {
	for _, s := range []error{ErrType, ErrContent} {
		if errors.Is(err, s) {
			return strings.TrimPrefix(err.Error(), s.Error()+": "), true
		}
	}
	return "", false
}

// inlineTypes are the only types served inline (rendered by the browser):
// raster images, whose bytes were sniff-verified at upload.
var inlineTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// CSP sandboxes any file response a browser does render, so even a
// mis-typed file cannot run script with the serving origin's privileges.
const CSP = "default-src 'none'; sandbox"

// ServeHeaders sets a stored file's download headers. Only verified raster
// images render inline with their type; everything else (rows stored
// before upload sniffing existed included) is an opaque
// application/octet-stream attachment. nosniff and the sandboxing CSP apply
// to every response, and the name is RFC 2231-encoded so it cannot break
// out of the header value.
func ServeHeaders(h http.Header, name, contentType string) {
	ct, disposition := "application/octet-stream", "attachment"
	if t := MediaType(contentType); inlineTypes[t] {
		ct, disposition = t, "inline"
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", httpx.ContentDisposition(disposition, name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", CSP)
}

// MediaType lowercases a content type and strips its parameters.
func MediaType(ct string) string {
	ct = strings.TrimSpace(strings.ToLower(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// SanitizeName keeps a file name's last path element, its tail when over
// MaxNameLen, and "file" for none.
func SanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > MaxNameLen {
		name = name[len(name)-MaxNameLen:]
	}
	if name == "" {
		return "file"
	}
	return name
}
