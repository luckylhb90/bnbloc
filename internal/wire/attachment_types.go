package wire

// The files a chat attachment may be (realtime's upload owns the check; the
// composer's file picker offers the same list, from wire.gen.ts). A file's
// type comes from its extension — the declared Content-Type only when the
// name has none — and its first bytes must pass the type's content check:
// nothing is trusted by name alone. What keeps an attachment from running
// script is how it is served, not its type: only sniff-verified raster
// images are served inline; every other attachment, markup included (HTML,
// SVG, XML), is an opaque, nosniff, sandboxed download. Where the app renders
// one, it renders it as it renders an agent's file: HTML in the sandboxed
// runner, SVG as an image (docs/bugs/2026-10-15-html-attachment-refused.md).

// Content checks of an attachment type (AttachmentType.Content): what its
// first bytes must look like.
//
//wire:union AttachmentContent
const (
	AttachmentContentPNG  = "png"
	AttachmentContentJPEG = "jpeg"
	AttachmentContentGIF  = "gif"
	AttachmentContentWebP = "webp"
	AttachmentContentPDF  = "pdf"
	// AttachmentContentZip: a zip archive (zip, and the Office Open XML and
	// OpenDocument formats, which are zip archives).
	AttachmentContentZip = "zip"
	// AttachmentContentOLE: an OLE compound file (legacy .doc, .xls, .ppt).
	AttachmentContentOLE = "ole"
	// AttachmentContentText: text, not binary (markup included: a text
	// attachment is only served as an opaque download).
	AttachmentContentText = "text"
	// AttachmentContentISOMedia: an ISO base media file (mp4, mov, m4a).
	AttachmentContentISOMedia = "isomedia"
	AttachmentContentWebM     = "webm"
	AttachmentContentMP3      = "mp3"
	AttachmentContentWAV      = "wav"
	AttachmentContentOgg      = "ogg"
)

// AttachmentType is one kind of file a chat attachment may be: its canonical
// media type (what the attachment is stored and served as), the extensions
// that name it (lower case, with the dot) and its content check.
type AttachmentType struct {
	MIME       string   `json:"mime"`
	Extensions []string `json:"extensions"`
	Content    string   `json:"content" tstype:"AttachmentContent"`
}

// AttachmentTypes are the files a chat attachment may be.
var AttachmentTypes = []AttachmentType{
	{MIME: "image/png", Extensions: []string{".png"}, Content: AttachmentContentPNG},
	{MIME: "image/jpeg", Extensions: []string{".jpg", ".jpeg"}, Content: AttachmentContentJPEG},
	{MIME: "image/gif", Extensions: []string{".gif"}, Content: AttachmentContentGIF},
	{MIME: "image/webp", Extensions: []string{".webp"}, Content: AttachmentContentWebP},
	{MIME: "application/pdf", Extensions: []string{".pdf"}, Content: AttachmentContentPDF},
	{MIME: "application/zip", Extensions: []string{".zip"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Extensions: []string{".docx"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.template", Extensions: []string{".dotx"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Extensions: []string{".xlsx"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.template", Extensions: []string{".xltx"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Extensions: []string{".pptx"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.openxmlformats-officedocument.presentationml.template", Extensions: []string{".potx"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.openxmlformats-officedocument.presentationml.slideshow", Extensions: []string{".ppsx"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.oasis.opendocument.text", Extensions: []string{".odt"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.oasis.opendocument.spreadsheet", Extensions: []string{".ods"}, Content: AttachmentContentZip},
	{MIME: "application/vnd.oasis.opendocument.presentation", Extensions: []string{".odp"}, Content: AttachmentContentZip},
	{MIME: "application/msword", Extensions: []string{".doc"}, Content: AttachmentContentOLE},
	{MIME: "application/vnd.ms-excel", Extensions: []string{".xls"}, Content: AttachmentContentOLE},
	{MIME: "application/vnd.ms-powerpoint", Extensions: []string{".ppt", ".pot", ".pps"}, Content: AttachmentContentOLE},
	{MIME: "text/plain", Extensions: []string{".txt", ".log"}, Content: AttachmentContentText},
	{MIME: "text/markdown", Extensions: []string{".md", ".markdown"}, Content: AttachmentContentText},
	{MIME: "text/csv", Extensions: []string{".csv"}, Content: AttachmentContentText},
	{MIME: "text/tab-separated-values", Extensions: []string{".tsv"}, Content: AttachmentContentText},
	{MIME: "application/json", Extensions: []string{".json"}, Content: AttachmentContentText},
	{MIME: "application/yaml", Extensions: []string{".yaml", ".yml"}, Content: AttachmentContentText},
	{MIME: "text/html", Extensions: []string{".html", ".htm"}, Content: AttachmentContentText},
	{MIME: "image/svg+xml", Extensions: []string{".svg"}, Content: AttachmentContentText},
	{MIME: "application/xml", Extensions: []string{".xml"}, Content: AttachmentContentText},
	{MIME: "video/mp4", Extensions: []string{".mp4", ".m4v"}, Content: AttachmentContentISOMedia},
	{MIME: "video/quicktime", Extensions: []string{".mov"}, Content: AttachmentContentISOMedia},
	{MIME: "video/webm", Extensions: []string{".webm"}, Content: AttachmentContentWebM},
	{MIME: "audio/mpeg", Extensions: []string{".mp3"}, Content: AttachmentContentMP3},
	{MIME: "audio/mp4", Extensions: []string{".m4a"}, Content: AttachmentContentISOMedia},
	{MIME: "audio/wav", Extensions: []string{".wav"}, Content: AttachmentContentWAV},
	{MIME: "audio/ogg", Extensions: []string{".ogg", ".oga"}, Content: AttachmentContentOgg},
}
