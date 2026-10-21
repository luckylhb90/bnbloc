package wire

// Notification DTOs (F5). realtime serves the member preferences, posts
// NotificationPayload to every notifier component visible to the event's
// tenant, and hands a member's notification to their desktop plugin
// (PluginNotification, notification.v1 hook) when they run one. The JSON tags are the public
// contract (docs/notifications.md).

// Notification types carried in NotificationPayload.Type.
const (
	NotificationMention       = "mention"
	NotificationAgentFinished = "agent_finished"
	NotificationAgentFailed   = "agent_failed"
	NotificationBorrowRequest = "borrow_request"
	// NotificationTest is only sent by a "send test notification" action
	// (super-admin or member); it is never produced by room activity.
	NotificationTest = "test"
	// NotificationAdminAlert is an operational alert a super-admin (or an
	// unattended job such as the nightly smoke) sends to platform admins
	// (POST /v1/platform/alerts). It has no room and is delivered to exactly
	// the listed recipients: member preferences, muted rooms and burst
	// collapsing do not apply.
	NotificationAdminAlert = "admin_alert"
	// NotificationBudgetWarning tells a member (member budget) or the org
	// admins (tenant budget) that this month's usage budget crossed its
	// warning threshold or was reached (R12). Room-less and operational,
	// like an admin alert: sent once per threshold per month.
	NotificationBudgetWarning = "budget_warning"
	// NotificationContentShared tells a member that an author shared an
	// expert, skill pack or prompt pack with them (a copy in their Library).
	// Room-less; it links to the Library.
	NotificationContentShared = "content_shared"
	// NotificationContentReview tells an organization's admins that a
	// content component waits for their approval, and its author how they
	// decided. Room-less; it links to the Organization console or the
	// Library.
	NotificationContentReview = "content_review"
	// NotificationAppNotice is a notice an interactive app sent to the
	// room's members or to named people of the organization (app.notify):
	// its title is the app's, and it links to the app.
	NotificationAppNotice = "app_notice"
	// NotificationRoomReport tells an organization's admins what a member
	// could not get done in a room (POST /v1/rooms/{roomId}/report): the
	// member's reviewed text, linked to the reply it is about.
	NotificationRoomReport = "room_report"
	// NotificationScheduleNotice tells a room's owner about one of its
	// scheduled tasks' event trigger: events a rate limit dropped
	// (docs/prd/2026-10-21-room-events.md §2.4). Operational, like a budget
	// warning: sent once per task until it is edited, never muted.
	NotificationScheduleNotice = "schedule_notice"
)

// AdminAlertRequest is the body of POST /v1/platform/alerts. Recipients
// default to every configured platform super-admin.
type AdminAlertRequest struct {
	Title      string   `json:"title"`
	Text       string   `json:"text,omitempty"`
	URL        string   `json:"url,omitempty"`
	Recipients []string `json:"recipients,omitempty"`
}

// AdminAlertResponse answers an accepted alert: the event id (the
// notifier's idempotency key), the recipients, and the notifier components
// it was queued for. Delivery is asynchronous through the outbox.
type AdminAlertResponse struct {
	ID         string   `json:"id"`
	Recipients []string `json:"recipients"`
	Notifiers  []string `json:"notifiers"`
}

// Notification delivery headers.
const (
	// NotificationSignatureHeader carries "t=<unix seconds>,v1=<hex>" where
	// v1 = HMAC-SHA256(secret, t + "." + body).
	NotificationSignatureHeader = "X-Bnbloc-Signature"
	// NotificationEventIDHeader repeats NotificationPayload.ID so a receiver
	// can de-duplicate retries without parsing the body.
	NotificationEventIDHeader = "X-Bnbloc-Event-Id"
)

// NotificationPrefs is a member's notification settings
// (GET/PUT /v1/my/notification-prefs). Defaults: every type on, no quiet rooms.
type NotificationPrefs struct {
	Mentions       bool     `json:"mentions"`
	AgentFinished  bool     `json:"agentFinished"`
	AgentFailed    bool     `json:"agentFailed"`
	BorrowRequests bool     `json:"borrowRequests"`
	ContentShared  bool     `json:"contentShared"`
	ContentReview  bool     `json:"contentReview"`
	AppNotices     bool     `json:"appNotices"`
	RoomReports    bool     `json:"roomReports"`
	QuietRooms     []string `json:"quietRooms"`
}

// RoomReportRequest is POST /v1/rooms/{roomId}/report: a member's note to
// the organization's admins of what the room could not do
// (docs/prd/2026-10-20-never-a-dead-end.md §2.2, the "report" fix), the
// text the member reviewed, and the reply it is about (its room sequence
// and channel; 0 links the room).
type RoomReportRequest struct {
	Text      string `json:"text"`
	Seq       int64  `json:"seq,omitempty"`
	ChannelID string `json:"channelId,omitempty"`
}

// MaxRoomReportText bounds a room report's text.
const MaxRoomReportText = 1000

// RoomReportReply says whether a note to the organization's admins (a room
// report, or a room's ask for a source's consent) was queued for one
// (false: no notifier, push subscription or desktop reaches one, or every
// admin turned room reports off).
type RoomReportReply struct {
	Queued bool `json:"queued"`
}

// NotificationTestResult answers a test send (POST
// /v1/platform/components/{name}/test and POST /v1/my/notifiers/{name}/test).
// Delivered is true when the notifier answered 2xx and reported no failed
// recipient; Results is the notifier's per-recipient report.
type NotificationTestResult struct {
	Delivered bool                          `json:"delivered"`
	Status    int                           `json:"status,omitempty"`
	Error     string                        `json:"error,omitempty"`
	EventID   string                        `json:"eventId"`
	Results   []NotificationRecipientResult `json:"results,omitempty"`
}

// NotificationRecipient is one recipient of a notification.
type NotificationRecipient struct {
	UserID string `json:"userId"`
	// ReplyToken (N1) is present when the notification accepts a reply
	// (NotificationPayload.Reply set): an opaque per-recipient token the
	// notifier sends back with POST /v1/notifier/replies to post the
	// member's answer into the room. Minted per delivery attempt, valid for
	// 7 days (notify.ReplyTokenTTL); never logged.
	ReplyToken string `json:"replyToken,omitempty"`
}

// NotificationReply (N1) says where a reply to a notification goes: the room
// channel, and — for an agent outcome — the agent the reply is addressed to
// (the same {ownerId, provider, core} recipient the member's request used;
// borrow rules apply when the member drives another member's agent). A nil
// Recipient posts human chat to the room (a mention).
type NotificationReply struct {
	ChannelID string                      `json:"channelId,omitempty"`
	Recipient *NotificationReplyRecipient `json:"recipient,omitempty"`
}

// NotificationReplyRecipient is the agent a reply is addressed to.
type NotificationReplyRecipient struct {
	OwnerID  string `json:"ownerId"`
	Provider string `json:"provider"`
	Core     string `json:"core,omitempty"`
}

// Notifier reply seam (N1): a notifier component posts a member's reply to a
// notification back into bnbloc.
const (
	// NotifierRepliesPath is the gateway route a notifier posts replies to.
	NotifierRepliesPath = "/v1/notifier/replies"
	// ComponentHeader names the endpoint component that signed a call to
	// the gateway (X-Bnbloc-Signature uses that component's credential): a
	// notifier's reply routes take its name, GET /v1/component-settings
	// "<kind>/<name>".
	ComponentHeader = "X-Bnbloc-Component"
)

// NotifierReplyRequest is the signed body of POST /v1/notifier/replies.
// IdempotencyKey is the receiving system's id of the reply message: a retry
// with the same key never posts twice. The reply names its token either
// directly (ReplyToken) or (R9) by a reference the notifier registered with
// POST /v1/notifier/reply-refs: Ref is the message the member answered and
// Sender the receiving system's account that answered — it must be the
// account the reference was registered for. Exactly one of the two forms.
type NotifierReplyRequest struct {
	ReplyToken     string `json:"replyToken,omitempty"`
	Ref            string `json:"ref,omitempty"`
	Sender         string `json:"sender,omitempty"`
	Text           string `json:"text"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// NotifierReplyRefsPath (R9) is the gateway route a notifier registers reply
// references at.
const NotifierReplyRefsPath = "/v1/notifier/reply-refs"

// NotifierReplyRefRequest is the signed body of POST /v1/notifier/reply-refs:
// the notifier sent the notification carrying ReplyToken as the receiving
// system's message Ref to the account Recipient (both opaque, at most 128
// characters). A reply quoting Ref from Recipient then resolves to the token
// in bnbloc, so the notifier keeps no state of its own.
type NotifierReplyRefRequest struct {
	ReplyToken string `json:"replyToken"`
	Ref        string `json:"ref"`
	Recipient  string `json:"recipient"`
}

// NotifierReplyResponse answers an accepted reply with the posted message's
// room sequence (the original sequence for an idempotent retry).
type NotifierReplyResponse struct {
	RoomID   string `json:"roomId"`
	Sequence int64  `json:"sequence"`
}

// NotificationRecipientResult is a notifier's per-recipient outcome. A failed
// recipient with Retryable=true is re-sent later (same event id, recipients
// narrowed to the retryable failures); a non-retryable failure is final.
type NotificationRecipientResult struct {
	UserID    string `json:"userId"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// NotificationDeliveryResponse is the optional 2xx body of a notifier.
type NotificationDeliveryResponse struct {
	Results []NotificationRecipientResult `json:"results"`
}

// MemberNotifier is one notifier component as a member sees it
// (GET /v1/my/notifiers).
type MemberNotifier struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// OptIn is true for an opt-in component; Installed then says whether the
	// member opted in (only opted-in members receive its notifications).
	OptIn     bool `json:"optIn"`
	Installed bool `json:"installed"`
}

// NotificationPayload is the signed JSON body posted to a notifier
// component (POST {baseUrl}/v1/notifications).
type NotificationPayload struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	TenantID   string                  `json:"tenantId"`
	RoomID     string                  `json:"roomId"`
	RoomName   string                  `json:"roomName"`
	Recipients []NotificationRecipient `json:"recipients"`
	Actor      string                  `json:"actor"`
	Title      string                  `json:"title"`
	Text       string                  `json:"text"`
	URL        string                  `json:"url,omitempty"`
	AtMs       int64                   `json:"atMs"`
	// Reply (N1) is set when the notification accepts a reply (mention,
	// agent_finished, agent_failed): each recipient then carries a
	// ReplyToken.
	Reply *NotificationReply `json:"reply,omitempty"`
	// Group, when set, addresses the notification to a group chat a room
	// connected through this notifier instead of members: Recipients is
	// empty, GroupAddress is the notifier's own address of the chat (what
	// it reported when the chat was connected). Only a notifier declaring
	// the groups capability receives one.
	Group        string `json:"group,omitempty"`
	GroupAddress string `json:"groupAddress,omitempty"`
}
