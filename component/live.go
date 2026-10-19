package component

// A job's live view, the tool's side (docs/prd/2026-10-17-browser-component.md
// §3.2, §3.3, §3.7): a tool with something live to show (a browser tab, a
// terminal, a canvas) posts frames through the room relay (POST /v1/live)
// while its turn runs; the reply says whether anyone watches, who holds
// control, and brings the controller's input. The room's side (the event,
// the viewer's routes) is internal/wire/job_live.go.
//
// A view that waits for a person (Waiting: a sign-in, a second factor)
// outlives its turn: the tool keeps posting for that job (?job= on the
// relay, a job its session ran) and the room keeps the view while the
// posts carry Waiting. A post without it after the turn ends the view.

// Bounds of a live frame post.
const (
	// MaxLiveFrameBytes bounds one frame's image (decoded).
	MaxLiveFrameBytes = 1 << 20
	// MaxLiveFrameSide bounds a frame's width and height in pixels.
	MaxLiveFrameSide = 4096
	// MaxLiveTitle bounds a frame's title, URL and Waiting (runes).
	MaxLiveTitle = 300
	// MaxLiveDialogText bounds a dialog's message and default (runes).
	MaxLiveDialogText = 1000
	// LiveKeepEverySec is how often a tool posts, at the least, to keep a
	// view no frame changes (a waiting page standing still); the room drops
	// a view a few of these after its posts stop.
	LiveKeepEverySec = 20
)

// LiveFramePost is POST /v1/live: one frame of the job's live view, or an
// empty post (no Data) that only collects the reply. Run names what is
// shown (a browser run); Width and Height are the image's pixels. Waiting
// says, in a sentence, what a person must do in the view before the work
// can go on ("" nothing): while it is set the view stays after the turn,
// as long as the tool keeps posting (an empty post every LiveKeepEverySec
// at least).
type LiveFramePost struct {
	Run      string `json:"run,omitempty"`
	Seq      int64  `json:"seq,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     []byte `json:"data,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	Waiting  string `json:"waiting,omitempty"`
	// Ready says what the view waited for looks done (the page moved on):
	// the room offers the person to let the work go on.
	Ready bool `json:"ready,omitempty"`
	// End closes the view now (what it showed is gone, or work resumed
	// elsewhere); the post carries no frame.
	End bool `json:"end,omitempty"`
	// WaitInput: an empty post while someone controls the view waits a
	// moment for their input before it answers (a long poll), so input
	// reaches the tool as it is sent rather than at the next post.
	WaitInput bool `json:"waitInput,omitempty"`
	// LeaveInput: the post takes no input; it stays queued for the tool's
	// WaitInput post. A tool that waits for input beside its frame posts
	// sets it on the frames, so input runs as it arrives, in order, never
	// behind a capture and its upload.
	LeaveInput bool `json:"leaveInput,omitempty"`
	// Dialog is the dialog the page has open (nil none): browser UI the
	// picture cannot show, which the page waits on. Every post says it
	// while it is open; the controller answers it (LiveInputDialog).
	Dialog *LiveDialog `json:"dialog,omitempty"`
}

// Kinds of a page's dialog (LiveDialog.Kind), as the browser names them.
//
//wire:union LiveDialogKind
const (
	LiveDialogAlert        = "alert"
	LiveDialogConfirm      = "confirm"
	LiveDialogPrompt       = "prompt"
	LiveDialogBeforeUnload = "beforeunload"
)

// LiveDialog is a dialog a page opened: its kind, its message, and a
// prompt's default text.
type LiveDialog struct {
	Kind    string `json:"kind" tstype:"LiveDialogKind"`
	Message string `json:"message"`
	Default string `json:"default,omitempty"`
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
	// LiveInputClose: the owner closes a view that waits for them (they
	// will not do it, or did it elsewhere); the tool ends it.
	LiveInputClose = "close"
	// LiveInputDialog: answers the page's open dialog (LiveDialog): Action
	// "accept" or "dismiss", Text a prompt's answer.
	LiveInputDialog = "dialog"
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
