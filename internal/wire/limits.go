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
)

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
	// MaxAppCoverBytes bounds an app's cover image (cover.png, .jpg or
	// .webp at the site's root, docs/prd/2026-10-13-app-gallery.md §4): a
	// larger one is not served, and the card draws the generated cover.
	MaxAppCoverBytes = 1 << 20
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
)
