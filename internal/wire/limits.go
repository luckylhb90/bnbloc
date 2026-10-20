package wire

// Limits a service enforces and a client checks before it sends, so the
// client can say why instead of meeting a 4xx. Each is the one value the
// service enforces (and the web reads it from wire.gen.ts): a limit is
// changed here, never in a second copy.
const (
	// MaxUnreadRooms bounds one GET /v1/rooms-unread?rooms= query: each
	// room costs a membership lookup and a count per channel. A longer list
	// is refused (400), never cut, so a client never reads "no unread" into
	// a truncated answer.
	MaxUnreadRooms = 200
	// MaxAttachmentBytes bounds one chat attachment upload.
	MaxAttachmentBytes = 25 << 20
	// MaxBlobBytes bounds one object in the blob store: realtime's blob put,
	// so also an artifact version file (BNBLOC_ARTIFACT_MAX_BYTES may only
	// lower it) and a super-admin artifact upload at the gateway.
	MaxBlobBytes = 256 << 20
	// MaxArtifactStateBytes bounds one live collaboration-state payload. The
	// state is transient but fans out to every room subscriber on each
	// change, so it stays gesture-sized.
	MaxArtifactStateBytes = 16 << 10
	// MaxDisplayNameRunes is the longest display name accepted, in
	// characters after trimming (Unicode code points, which is also how
	// MySQL sizes the VARCHAR(40) column).
	MaxDisplayNameRunes = 40
	// MaxContentTagRunes bounds one content component tag, in characters.
	MaxContentTagRunes = 32
	// Media bounds (docs/prd/2026-10-16-voice-and-image-generation.md):
	// what one media model call may carry. The gateway enforces them; the
	// web reads them here to say why before it sends.
	MaxMediaAudioBytes      = 20 << 20 // one transcription's uploaded audio
	MaxMediaTranscriptChars = 32_768   // a transcript's characters
	MaxMediaPrompt          = 4000     // a generation's prompt (an image's or a video's), in characters
	MaxMediaImageReplyBytes = 16 << 20 // read of one generation's reply body
	MaxMediaImageBytes      = 12 << 20 // one generated image, decoded
	MaxMediaImages          = 4        // images one generation call may draw
	MaxMediaImageInputs     = 4        // room files and attachments one generation may use as references
)

// ContentTagPattern is the one rule for a content component tag: lower-case
// letters of any script (CJK and other caseless letters included), digits,
// spaces, '_' and '-', starting with a letter or digit. It is valid both as
// Go RE2 and as a JavaScript regular expression with the "u" flag.
const ContentTagPattern = `^[\p{Ll}\p{Lo}\p{Nd}][\p{Ll}\p{Lo}\p{Nd} _-]*$`

// The safety caps of an interactive app's data (docs/prd/2026-10-12-
// interactive-apps.md §4.5): cost and abuse bounds, not product rules. The
// room service (services/room/appdata.go) enforces them; the app SDK checks
// them before it sends.
const (
	// MaxAppDocBytes bounds one document: its JSON body without the server
	// stamps.
	MaxAppDocBytes = 64 << 10
	// MaxAppDocs bounds the documents of one app, all collections together.
	MaxAppDocs = 100_000
	// MaxAppBytes bounds the stored bytes of one app's documents.
	MaxAppBytes = 256 << 20
	// MaxAppCollections bounds the named collections of one app.
	MaxAppCollections = 64
	// MaxAppCollectionNameLen bounds a collection name (letters, digits,
	// '_' and '-').
	MaxAppCollectionNameLen = 64
	// MaxAppDocIDLen bounds a document id (letters, digits, '_', '-', '.'
	// and ':').
	MaxAppDocIDLen = 128
	// MaxAppFindLimit bounds one page of a find.
	MaxAppFindLimit = 500
	// MaxAppRequestBytes bounds one app data request body: a document plus
	// its envelope (AppDocWrite, AppDocPatch).
	MaxAppRequestBytes = MaxAppDocBytes + 4<<10
	// MaxAppFiles and MaxAppFileBytes bound the files one app's pages
	// uploaded (app.files), apart from its documents: a form collecting
	// photos must not crowd out its own answers. One file is at most
	// MaxAttachmentBytes, a chat attachment's cap.
	MaxAppFiles     = 10_000
	MaxAppFileBytes = 2 << 30
	// MaxAppUploadBytes bounds an upload's request body: one file and its
	// multipart envelope.
	MaxAppUploadBytes = MaxAttachmentBytes + 16<<10
	// MaxAppAIPrompt and MaxAppAIInput bound an app's ai call, in
	// characters: its instruction, and the data it works on (a week of a
	// team's logs).
	MaxAppAIPrompt = 8000
	MaxAppAIInput  = 60000
	// MaxAppNotifyLogins bounds the logins one app notice names, and
	// MaxAppNotifyRecipients the people it reaches (a room's members
	// included).
	MaxAppNotifyLogins     = 100
	MaxAppNotifyRecipients = 500
)

// The caps of a work (docs/prd/2026-10-13-works-gallery-and-make-similar.md):
// contentpkg's work kind enforces them.
const (
	// MaxWorkCoverBytes bounds a work's cover image (cover.png, .jpg or
	// .webp at the bundle's root, or an image work's image): a larger one
	// is not served, and the card draws the generated cover.
	MaxWorkCoverBytes = 1 << 20
	// MaxWorkReferences bounds the reference files a work's recipe keeps
	// (the request's attachments, in order; the rest are left out).
	MaxWorkReferences = 8
	// MaxWorkReferenceBytes bounds the reference files of a work together.
	MaxWorkReferenceBytes = 25 << 20
)

// The bounds of a component's settings (docs/prd/2026-10-19-component-
// settings.md): core/pkgmeta holds a declaration and a value to them; the
// web reads them to say why before it sends.
const (
	// MaxComponentSettings bounds the settings one component declares.
	MaxComponentSettings = 24
	// MaxComponentSettingValueBytes bounds one value (a key, a URL, a
	// path: never a document).
	MaxComponentSettingValueBytes = 4 << 10
	// MaxComponentSettingLines and MaxComponentSettingLineBytes bound a
	// lines value (a list of folders, patterns or names): at most this
	// many lines, each trimmed, non-empty and at most this long. A lines
	// value is held to these instead of MaxComponentSettingValueBytes.
	MaxComponentSettingLines     = 64
	MaxComponentSettingLineBytes = 512
)
