package realtime

import (
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Hub metrics (docs/monitoring.md).
const (
	metricEventsPublished  = "realtime_events_published_total"
	metricSSEConnections   = "realtime_sse_connections"
	metricFeedCommits      = "realtime_feed_commits_total"
	metricFeedCommitEvents = "realtime_feed_commit_events_total"
	metricSSEOverflows     = "realtime_sse_overflows_total"
	metricSSEGaps          = "realtime_sse_gaps_total"
	metricLaneEvents       = "realtime_stream_lane_events_total"
	metricChatStep         = "realtime_chat_step_seconds"
	metricFeedCommitTime   = "realtime_feed_commit_seconds"
	metricFeedDeliverTime  = "realtime_feed_deliver_seconds"
)

// The steps of a chat post (ObserveChatStep), in order.
const (
	ChatStepAccess = "access" // membership, channel and read access
	ChatStepAdmit  = "admit"  // the channel's setup: rules, who posts, slow mode
	ChatStepCommit = "commit" // waiting for the room's batch, its commit and delivery
	ChatStepAfter  = "after"  // mentions, thread counts, the agent job
)

// SSE connection kinds: a presence-counting subscriber (a member's web or
// desktop client), a non-presence observer (?presence=0: a bridge, or the
// smoke check), an interactive app's change feed (the gateway's) or a
// person reading a shared room they have not joined (a preview).
const (
	SSEKindMember  = "member"
	SSEKindBridge  = "bridge"
	SSEKindApp     = "app"
	SSEKindPreview = "preview"
)

// EventKindLabel maps an event kind onto the bounded metric label set: the
// registered kinds (wire.RoomEventKinds). Any other kind string (the
// internal ingest accepts one) is counted as "other", so a caller can never
// grow the label space.
func EventKindLabel(kind string) string {
	if _, ok := wire.LookupEventKind(kind); ok {
		return kind
	}
	return "other"
}

// RegisterMetrics describes the hub/SSE series and registers the SSE gauges
// at zero so a connections-dropped-to-zero alert has a series to read.
func RegisterMetrics(m *obs.Metrics) {
	if m == nil {
		return
	}
	m.Describe(metricEventsPublished, "Events published to room streams by kind (unknown kinds count as other).")
	m.Describe(metricFeedCommits, "Room feed group commits (one multi-row insert each) by result.")
	m.Describe(metricFeedCommitEvents, "Durable events stored by successful group commits; divided by the ok commits it is the mean batch size.")
	m.Describe(metricSSEGaps, "Room streams that fell a whole buffer behind once and were told to fetch the difference (stream_gap) instead of being closed.")
	m.Describe(metricSSEOverflows, "Room streams closed because the client fell a whole buffer behind; the client reconnects and catches up from history.")
	m.Describe(metricSSEConnections, "Open room event streams by kind (member: presence-counting client; bridge: ?presence=0 observer; app: an interactive app's change feed; preview: a person reading a shared room they have not joined).")
	m.Describe(metricChatStep, "A chat post's time by step (access, admit, commit, after), seconds.")
	m.Describe(metricFeedCommitTime, "One room batch's database commit, seconds.")
	m.Describe(metricFeedDeliverTime, "One room batch's delivery to the room's streams, seconds.")
	m.Describe(metricLaneEvents, "Room events on lanes streams by lane (full: delivered; activity: folded into a channel_activity; activity_sent: channel_activity events written; none: dropped, a closed channel's typing or progress).")
	for _, lane := range []string{wire.LaneFull, wire.LaneActivity, "activity_sent", wire.LaneNone} {
		m.CounterAdd(metricLaneEvents, 0, obs.L("lane", lane))
	}
	for _, kind := range []string{SSEKindMember, SSEKindBridge, SSEKindApp, SSEKindPreview} {
		m.GaugeSet(metricSSEConnections, 0, obs.L("kind", kind))
	}
}

// TrackSSE counts one open room event stream of kind until the returned
// func is called (defer it).
func TrackSSE(m *obs.Metrics, kind string) (done func()) {
	if kind != SSEKindBridge && kind != SSEKindApp && kind != SSEKindPreview {
		kind = SSEKindMember
	}
	m.GaugeAdd(metricSSEConnections, 1, obs.L("kind", kind))
	return func() { m.GaugeAdd(metricSSEConnections, -1, obs.L("kind", kind)) }
}

// CountLaneEvents adds a lanes stream's counts since its last report
// (per lane: wire.LaneFull, wire.LaneActivity, "activity_sent",
// wire.LaneNone); streams report once a second, not per event.
func CountLaneEvents(m *obs.Metrics, counts map[string]int) {
	for lane, n := range counts {
		if n > 0 {
			m.CounterAdd(metricLaneEvents, float64(n), obs.L("lane", lane))
		}
	}
}

// ObserveChatStep records how long one step of a chat post took.
func ObserveChatStep(m *obs.Metrics, step string, d time.Duration) {
	m.HistogramObserve(metricChatStep, d.Seconds(), obs.L("step", step))
}
