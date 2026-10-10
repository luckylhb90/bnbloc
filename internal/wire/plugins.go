package wire

import "encoding/json"

// Plugin hook delivery DTOs. realtime is the hook authority: it decides which
// plugin sees which room event and audits the outcome, but a plugin sidecar
// runs on a member's machine, which the server never dials. So each delivery
// travels over the member's own authenticated stream
// (GET /v1/my/plugin-events, SSE) to their desktop, which posts the envelope
// to its local sidecar's POST /events and acknowledges the outcome
// (POST /v1/my/plugin-events/{id}/ack). The JSON tags are the public contract.

// PluginEventKind is the SSE event name of one PluginDelivery frame.
const PluginEventKind = "plugin_event"

// Plugin delivery outcomes a desktop acknowledges (PluginDeliveryAck.Status).
const (
	PluginDeliveryDelivered = "delivered"
	PluginDeliveryFailed    = "failed"
)

// PluginEnvelope is what a plugin sidecar receives at POST /events: the hook's
// versioned event name and an immutable copy of the source event payload.
type PluginEnvelope struct {
	Event    string `json:"event"`
	Version  string `json:"version"`
	TenantID string `json:"tenantId"`
	RoomID   string `json:"roomId"`
	Payload  any    `json:"payload"`
}

// PluginDelivery is one frame of a member's plugin event stream: the delivery
// id to acknowledge, the plugin it targets, and the envelope to post verbatim.
type PluginDelivery struct {
	ID       string          `json:"id"`
	Plugin   string          `json:"plugin"`
	Envelope json.RawMessage `json:"envelope"`
}

// PluginDeliveryAck reports how the desktop's POST /events went.
type PluginDeliveryAck struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// PluginNotification is the payload of a notification.v1 delivery: ONE
// member's notification, sent to that member's own desktop plugin (e.g. a
// personal bot), which delivers it in place of the server notifiers. The
// member's preferences, muted rooms and burst collapsing were already applied.
type PluginNotification struct {
	// ID is the logical event id (the server notifiers' X-Bnbloc-Event-Id):
	// stable across retries, so a plugin can de-duplicate on (id, userId).
	ID   string `json:"id"`
	Type string `json:"type"`
	// UserID is the member this notification is for (their bnbloc subject,
	// typically the IdP email).
	UserID    string `json:"userId"`
	RoomID    string `json:"roomId,omitempty"`
	RoomName  string `json:"roomName,omitempty"`
	ChannelID string `json:"channelId,omitempty"`
	// Seq is the room message the notification is about (0 = none).
	Seq   int64  `json:"seq,omitempty"`
	Actor string `json:"actor"`
	Title string `json:"title"`
	Text  string `json:"text"`
	// URL is the web deep link (absent without BNBLOC_PUBLIC_URL).
	URL  string `json:"url,omitempty"`
	AtMs int64  `json:"atMs"`
	// Reply is set when the member may answer: the plugin posts the answer
	// as a normal chat message through the desktop host API
	// (POST {BNBLOC_PLUGIN_HOST_URL}/v1/chat) addressed as here.
	Reply *PluginNotificationReply `json:"reply,omitempty"`
}

// Reply targets of a PluginNotificationReply.
const (
	PluginReplyToRoom  = "room"
	PluginReplyToAgent = "agent"
)

// PluginNotificationReply says where an answer goes: the room channel as
// human chat (To "room", a mention), or the agent the member's request
// addressed (To "agent": the chat recipient {ownerId, provider, core};
// borrow rules apply as for any typed message).
type PluginNotificationReply struct {
	RoomID    string                      `json:"roomId"`
	ChannelID string                      `json:"channelId,omitempty"`
	To        string                      `json:"to"`
	Agent     *NotificationReplyRecipient `json:"agent,omitempty"`
}
