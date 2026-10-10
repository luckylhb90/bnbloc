package realtime

import (
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Hub metrics (docs/monitoring.md).
const (
	metricEventsPublished = "realtime_events_published_total"
	metricSSEConnections  = "realtime_sse_connections"
)

// SSE connection kinds: a presence-counting subscriber (a member's web or
// desktop client) or a non-presence observer (?presence=0: a bridge, or the
// smoke check).
const (
	SSEKindMember = "member"
	SSEKindBridge = "bridge"
)

// publishedKinds is the bounded label set of published event kinds. A kind
// outside it (the internal ingest accepts any kind string) is counted as
// "other", so a caller can never grow the label space.
var publishedKinds = map[string]bool{
	wire.KindChatMessage: true, wire.KindAgentMessages: true,
	"agent_job_created": true, "agent_job_updated": true, "agent_session_state": true, "agent_roster": true,
	"presence_join": true, "presence_leave": true, "presence_snapshot": true, "presence_typing": true,
	"message_deleted": true, "message_edited": true, wire.EventTaskUpdated: true,
	"artifact_updated": true, "artifact_state": true,
	wire.EventMemberRemoved: true, wire.EventRoomDeleted: true,
}

// EventKindLabel maps an event kind onto the bounded metric label set.
func EventKindLabel(kind string) string {
	if publishedKinds[kind] {
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
	m.Describe(metricSSEConnections, "Open room event streams by kind (member: presence-counting client; bridge: ?presence=0 observer).")
	m.GaugeSet(metricSSEConnections, 0, obs.L("kind", SSEKindMember))
	m.GaugeSet(metricSSEConnections, 0, obs.L("kind", SSEKindBridge))
}

// TrackSSE counts one open room event stream of kind until the returned
// func is called (defer it).
func TrackSSE(m *obs.Metrics, kind string) (done func()) {
	if kind != SSEKindBridge {
		kind = SSEKindMember
	}
	m.GaugeAdd(metricSSEConnections, 1, obs.L("kind", kind))
	return func() { m.GaugeAdd(metricSSEConnections, -1, obs.L("kind", kind)) }
}
