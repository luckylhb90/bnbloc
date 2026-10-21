package wire

// Share targets (docs/prd/2026-10-21-share-target-components.md): a member
// shares one item, as a card, to a conversation of another app through an
// installed share-target component.

// Share item kinds: what a member may share.
//
//wire:union ShareItemKind
const (
	ShareItemRoom        = "room"
	ShareItemMessage     = "message"
	ShareItemDeliverable = "deliverable"
)

// Share destination types: who a card goes to in the other app.
//
//wire:union ShareDestType
const (
	ShareDestUser  = "user"
	ShareDestGroup = "group"
)

const (
	// MaxShareDest bounds the conversations of one share.
	MaxShareDest = 20
	// MaxShareIDBytes bounds a delivery id, a destination id and an item id.
	MaxShareIDBytes = 128
	// MaxShareHostTokenBytes bounds the other app's credential for the member.
	MaxShareHostTokenBytes = 8 << 10
	// MaxShareSummaryRunes bounds a card's summary.
	MaxShareSummaryRunes = 100
	// MaxShareTitleRunes bounds a card's title.
	MaxShareTitleRunes = 100
)

// ShareTargetInfo is one share target a member may use
// (GET /v1/my/share-targets).
type ShareTargetInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ShareItem names the item a member shares. Kind picks the fields that
// matter: a room needs RoomID; a message RoomID, ChannelID and Seq; a
// deliverable RoomID, ArtifactID and optionally Version.
type ShareItem struct {
	Kind       string `json:"kind" tstype:"ShareItemKind"`
	RoomID     string `json:"roomId"`
	ChannelID  string `json:"channelId,omitempty"`
	Seq        int64  `json:"seq,omitempty"`
	ArtifactID string `json:"artifactId,omitempty"`
	Version    int    `json:"version,omitempty"`
}

// ShareDest is one conversation of the other app: its id there and whether
// it is a person or a group.
type ShareDest struct {
	ID   string `json:"id"`
	Type string `json:"type" tstype:"ShareDestType"`
}

// ShareCard is a shared item as a neutral card: what the gateway built from
// the item as the member (internal/sharecard) and the target formats for its
// app. Kind is a ShareItem* value; Summary is plain text.
type ShareCard struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	URL     string `json:"url"`
}

// ShareSessionRequest starts a share (POST /v1/my/share-targets/{name}/sessions):
// the item the member shares.
type ShareSessionRequest struct {
	Item ShareItem `json:"item"`
}

// ShareSession answers a started share: URL is the share page, with the
// session's ticket in its path, which the web navigates to.
type ShareSession struct {
	URL string `json:"url"`
}

// ShareSessionInfo is what the share page reads of its session
// (GET <page URL>session): the card, the address to send it to and the link
// back to the shared item.
type ShareSessionInfo struct {
	Card    ShareCard `json:"card"`
	SendURL string    `json:"sendUrl"`
	BackURL string    `json:"backUrl"`
}

// ShareSend is the share page's request to send the session's card
// (POST <page URL>send): DeliveryID is the client half's idempotency key;
// HostToken is the other app's credential for the member, used for this one
// call and never stored or logged.
type ShareSend struct {
	DeliveryID string      `json:"deliveryId"`
	Dest       []ShareDest `json:"dest"`
	HostToken  string      `json:"hostToken"`
}

// ShareResult answers a share the target accepted.
type ShareResult struct {
	Status string `json:"status"`
}

// ShareSignatureHeader carries a share's signature to the target: the
// notifier's scheme ("t=<unix seconds>,v1=<hex>", notify.Sign) over the
// request body with the component's credential.
const ShareSignatureHeader = NotificationSignatureHeader

// ShareTargetList answers GET /v1/my/share-targets.
type ShareTargetList struct {
	Targets []ShareTargetInfo `json:"targets"`
}
