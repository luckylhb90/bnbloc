package wire

// Service components (docs/architecture/service-components.md): the
// member-facing and bridge-facing shapes of the platform's routes. The
// component-facing shapes are the public contract's (component/service.go).

// TurnService is one service a bridge may call for its turns
// (GET /v1/my/turn-services): its name and the primitives it declared.
type TurnService struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// TurnServiceList answers GET /v1/my/turn-services.
type TurnServiceList struct {
	Services []TurnService `json:"services"`
}

// ComponentPageInfo is one page a member may open
// (GET /v1/my/component-pages): the component, the page's name, label and
// audience.
type ComponentPageInfo struct {
	Component string `json:"component"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	Audience  string `json:"audience"`
	// InRoom: the page is offered in a room too (opened with its roomId).
	InRoom bool `json:"inRoom,omitempty"`
	// Version is the installed package's; ServiceVersion the running
	// service's own build, when it says.
	Version        string `json:"version,omitempty"`
	ServiceVersion string `json:"serviceVersion,omitempty"`
}

// ComponentPageList answers GET /v1/my/component-pages. Unreadable names
// the services whose pages could not be read.
type ComponentPageList struct {
	Pages      []ComponentPageInfo `json:"pages"`
	Unreadable []string            `json:"unreadable,omitempty"`
}

// ComponentPageOpen opens a page (POST /v1/my/component-pages/{component}/{page}):
// Query is passed to the page.
type ComponentPageOpen struct {
	Query string `json:"query,omitempty"`
	// RoomID opens the page for a room the member is in: the page is told
	// the room and their role there.
	RoomID string `json:"roomId,omitempty"`
}

// ComponentPageSession is the opened page's address: it carries the
// member's ticket.
type ComponentPageSession struct {
	URL string `json:"url"`
}

// NoticeComponentCard is a service component's card for the turn's
// requester (payload.card: a ComponentCard), shown to them only.
const NoticeComponentCard = "component-card"

// ComponentCard is a service component's card as the feed shows it: the
// component, the person it is for, and the card itself (plain text and
// actions, component.Card's shape).
type ComponentCard struct {
	Component string                `json:"component"`
	For       string                `json:"for"`
	Title     string                `json:"title"`
	Text      string                `json:"text,omitempty"`
	Actions   []ComponentCardAction `json:"actions,omitempty"`
}

// ComponentCardCall is a card's call (component.CardCall): POST
// /v1/my/component-cards/{component}/{page} with the action's query and
// the card's room; the answer is the component's component.CardReply.
type ComponentCardCall struct {
	Query  string `json:"query,omitempty"`
	RoomID string `json:"roomId,omitempty"`
}

// ComponentCardAction is one button of a ComponentCard.
type ComponentCardAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Page  string `json:"page,omitempty"`
	Query string `json:"query,omitempty"`
}
