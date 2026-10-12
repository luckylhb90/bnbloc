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
	// MaxStarterPrompts and MaxStarterPromptBytes bound a room template's
	// starter prompts (count, and bytes of one prompt).
	MaxStarterPrompts     = 20
	MaxStarterPromptBytes = 4 << 10
	// MaxDisplayNameRunes is the longest display name accepted, in
	// characters after trimming (Unicode code points, which is also how
	// MySQL sizes the VARCHAR(40) column).
	MaxDisplayNameRunes = 40
)
