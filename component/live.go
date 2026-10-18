package component

// A job's live view, the tool's side (docs/prd/2026-10-17-browser-component.md
// §3.2, §3.3): a tool with something live to show (a browser tab, a
// terminal, a canvas) posts frames through the room relay (POST /v1/live)
// while its turn runs; the reply says whether anyone watches, who holds
// control, and brings the controller's input. The room's side (the event,
// the viewer's routes) is internal/wire/job_live.go.

// Bounds of a live frame post.
const (
	// MaxLiveFrameBytes bounds one frame's image (decoded).
	MaxLiveFrameBytes = 1 << 20
	// MaxLiveFrameSide bounds a frame's width and height in pixels.
	MaxLiveFrameSide = 4096
	// MaxLiveTitle bounds a frame's title and URL (runes).
	MaxLiveTitle = 300
)

// LiveFramePost is POST /v1/live: one frame of the job's live view, or an
// empty post (no Data) that only collects the reply. Run names what is
// shown (a browser run); Width and Height are the image's pixels.
type LiveFramePost struct {
	Run      string `json:"run,omitempty"`
	Seq      int64  `json:"seq,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     []byte `json:"data,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
}

// LiveFrameReply answers a post: whether someone watches the large view
// (the tool posts faster then), who holds control ("" nobody), and the
// input queued since the last post, oldest first.
type LiveFrameReply struct {
	Watching   bool        `json:"watching"`
	Controller string      `json:"controller,omitempty"`
	Input      []LiveInput `json:"input,omitempty"`
}

// Live input kinds (LiveInput.Type).
//
//wire:union LiveInputType
const (
	// LiveInputMouse: a pointer press, release or move at X, Y.
	LiveInputMouse = "mouse"
	// LiveInputWheel: a scroll by DeltaX, DeltaY at X, Y.
	LiveInputWheel = "wheel"
	// LiveInputKey: a key pressed or released (Key, Code, Text).
	LiveInputKey = "key"
	// LiveInputText: text inserted as typed (a paste, an IME's result).
	LiveInputText = "text"
	// LiveInputFocus: bring the window showing the view to the front of
	// its machine (for what remote input cannot carry: a security key, a
	// system dialog).
	LiveInputFocus = "focus"
)

// LiveInput is one input event. X and Y are fractions of the frame (0..1),
// so they hold at any display size; Action is "down", "up" or "move" for
// the pointer and "down" or "up" for keys; Modifiers is a bit set (1 Alt,
// 2 Ctrl, 4 Meta, 8 Shift, CDP's).
type LiveInput struct {
	Type      string  `json:"type" tstype:"LiveInputType"`
	Action    string  `json:"action,omitempty"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Button    string  `json:"button,omitempty"`
	Clicks    int     `json:"clicks,omitempty"`
	DeltaX    float64 `json:"deltaX,omitempty"`
	DeltaY    float64 `json:"deltaY,omitempty"`
	Key       string  `json:"key,omitempty"`
	Code      string  `json:"code,omitempty"`
	Text      string  `json:"text,omitempty"`
	Modifiers int     `json:"modifiers,omitempty"`
}
