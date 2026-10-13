package realtime

import (
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
)

// SSE connection kinds: a presence-counting subscriber (a member's web or
// desktop client), a non-presence observer (?presence=0: a bridge, or the
// smoke check) or an interactive app's change feed (the gateway's).
const (
	SSEKindMember = "member"
	SSEKindBridge = "bridge"
	SSEKindApp    = "app"
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
	m.Describe(metricSSEOverflows, "Room streams closed because the client fell a whole buffer behind; the client reconnects and catches up from history.")
	m.Describe(metricSSEConnections, "Open room event streams by kind (member: presence-counting client; bridge: ?presence=0 observer; app: an interactive app's change feed).")
	for _, kind := range []string{SSEKindMember, SSEKindBridge, SSEKindApp} {
		m.GaugeSet(metricSSEConnections, 0, obs.L("kind", kind))
	}
}

// TrackSSE counts one open room event stream of kind until the returned
// func is called (defer it).
func TrackSSE(m *obs.Metrics, kind string) (done func()) {
	if kind != SSEKindBridge && kind != SSEKindApp {
		kind = SSEKindMember
	}
	m.GaugeAdd(metricSSEConnections, 1, obs.L("kind", kind))
	return func() { m.GaugeAdd(metricSSEConnections, -1, obs.L("kind", kind)) }
}
