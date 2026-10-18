package wire

// A job's live view (docs/prd/2026-10-17-browser-component.md §3.2, §3.3):
// a tool with something live to show (a browser tab, a terminal, a canvas)
// posts frames while its turn runs, and the people in the room watch it and,
// with control, drive it.
//
//   - The tool posts a frame through the room relay (POST /v1/live, a job
//     grant). The gateway hands it to the room's realtime owner, which keeps
//     only the latest frame of each job, in memory.
//   - Realtime tells the room a new frame exists (EventJobLiveFrame, no
//     image): viewers fetch the image themselves
//     (GET /v1/rooms/{roomId}/jobs/{jobId}/live/frame), so a slow viewer
//     skips frames instead of falling behind, and the room's event stream
//     never carries images.
//   - A viewer with control queues input (POST .../live/input); the tool
//     takes it in the reply to its next post. Input never goes out to the
//     room.
//
// All of it is transient: nothing is stored, and a job's live state ends
// with its turn (posts are refused once the job is no longer running).

// EventJobLiveFrame carries JobLiveFrame (realtime), scoped to the job's
// channel: a job has a new frame.
const EventJobLiveFrame = "job_live_frame"

// Bounds of a live frame post.
const (
	// MaxLiveFrameBytes bounds one frame's image (decoded).
	MaxLiveFrameBytes = 1 << 20
	// MaxLiveFrameSide bounds a frame's width and height in pixels.
	MaxLiveFrameSide = 4096
	// MaxLiveTitle bounds a frame's title and URL (runes).
	MaxLiveTitle = 300
	// MaxLiveInput bounds the input events queued for a job; the oldest
	// are dropped past it.
	MaxLiveInput = 256
)

// LiveFramePost is POST /v1/live: one frame of the job's live view, or an
// empty post (no Data) that only collects the reply. Run names what is
// shown (a browser run); Width and Height are the image's pixels; ViewWidth
// and ViewHeight the page's own size, which input coordinates map to.
type LiveFramePost struct {
	Run        string `json:"run,omitempty"`
	Seq        int64  `json:"seq,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Data       []byte `json:"data,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Controller string `json:"-"`
}

// LiveFrameHeader carries, on the frame image's answer
// (GET .../live/frame), the frame's JobLiveFrame as base64url JSON: a
// viewer opened after the last job_live_frame event knows the view at once.
const LiveFrameHeader = "X-Bnbloc-Live-Frame"

// LiveFrameReply answers a post: whether someone watches the large view
// (the tool posts faster then), who holds control ("" nobody), and the
// input queued since the last post, oldest first.
type LiveFrameReply struct {
	Watching   bool        `json:"watching"`
	Controller string      `json:"controller,omitempty"`
	Input      []LiveInput `json:"input,omitempty"`
}

// LiveFrameIngest is the gateway's hand-off of a post to realtime
// (POST /internal/v1/rooms/{roomId}/live/{jobId}): the frame, and the job's
// facts realtime checks viewers and controllers against.
type LiveFrameIngest struct {
	ChannelID string        `json:"channelId,omitempty"`
	Owner     string        `json:"owner"`
	Requester string        `json:"requester"`
	Frame     LiveFramePost `json:"frame"`
}

// JobLiveFrame is EventJobLiveFrame: a job's newest frame, without the
// image. Owner is whose machine shows it (the only member who may take
// control, until control grants exist); Controller who holds control now.
type JobLiveFrame struct {
	JobID      string `json:"jobId"`
	Run        string `json:"run,omitempty"`
	Seq        int64  `json:"seq"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Owner      string `json:"owner"`
	Controller string `json:"controller,omitempty"`
	AtMs       int64  `json:"atMs"`
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

// LiveInputPost is POST /v1/rooms/{roomId}/jobs/{jobId}/live/input: the
// controller's input, in order.
type LiveInputPost struct {
	Events []LiveInput `json:"events"`
}

// LiveControlPost is POST /v1/rooms/{roomId}/jobs/{jobId}/live/control:
// take control (Take) or hand it back. A controller repeats it while in
// control: control lapses LiveControlLapse after the last one, so a closed
// view hands it back by itself.
type LiveControlPost struct {
	Take bool `json:"take"`
}

// LiveControlReply answers a control post: who holds control now.
type LiveControlReply struct {
	Controller string `json:"controller,omitempty"`
}

// LiveControlLapseMs is how long control holds without a renewal.
const LiveControlLapseMs = 15_000
