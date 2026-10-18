package wire

import "github.com/luckylhb90/bnbloc/component"

// A job's live view (docs/prd/2026-10-17-browser-component.md §3.2, §3.3):
// a tool with something live to show (a browser tab, a terminal, a canvas)
// posts frames while its turn runs, and the people in the room watch it and,
// with control, drive it.
//
//   - The tool posts a frame through the room relay (POST /v1/live, a job
//     grant; the tool's side is component/live.go). The gateway hands it to
//     the room's realtime owner, which keeps only the latest frame of each
//     job, in memory.
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

// MaxLiveInput bounds the input events queued for a job; the oldest are
// dropped past it. The post's own bounds are the tool's contract
// (component.MaxLiveFrameBytes and the rest).
const MaxLiveInput = 256

// LiveFrameHeader carries, on the frame image's answer
// (GET .../live/frame), the frame's JobLiveFrame as base64url JSON: a
// viewer opened after the last job_live_frame event knows the view at once.
const LiveFrameHeader = "X-Bnbloc-Live-Frame"

// LiveFrameIngest is the gateway's hand-off of a post to realtime
// (POST /internal/v1/rooms/{roomId}/live/{jobId}): the frame, and the job's
// facts realtime checks viewers and controllers against.
type LiveFrameIngest struct {
	ChannelID string                  `json:"channelId,omitempty"`
	Owner     string                  `json:"owner"`
	Requester string                  `json:"requester"`
	Frame     component.LiveFramePost `json:"frame"`
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

// LiveInputPost is POST /v1/rooms/{roomId}/jobs/{jobId}/live/input: the
// controller's input, in order.
type LiveInputPost struct {
	Events []component.LiveInput `json:"events"`
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
