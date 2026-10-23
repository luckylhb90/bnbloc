package component

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
)

// Service components (docs/architecture/service-components.md): an
// endpoint component that acts for members and for the turns their agents
// run, through primitives it declares in its package. The platform attests
// who a call is for and carries it; what a call may do is the component's
// decision. Every call in either direction is signed with the component's
// credential over SignedRequest.

// ServiceContractV1 pins the service component contract.
const ServiceContractV1 = "bnbloc.service.contract/v1"

// The primitives a service component declares (package capabilities). It
// gets none it does not declare.
const (
	// ServiceTurnProxy: a bridge may call the component for a turn it
	// drives (TurnProxyPath), the turn attested in TurnHeader.
	ServiceTurnProxy = "turnProxy"
	// ServiceEgress: the turn's HTTPS to the hosts the component claims
	// (ServiceEgressHostsPath) goes through it (ServiceEgressRequestPath).
	// Needs ServiceTurnProxy.
	ServiceEgress = "egress"
	// ServiceRoomFacts: the component adds a line and files to the room's
	// records for the turn (ServiceFactsPath). Needs ServiceTurnProxy.
	ServiceRoomFacts = "roomFacts"
	// ServicePages: members open the component's pages (ServicePagesPath).
	ServicePages = "pages"
	// ServiceCallbacks: another system redirects a browser to the
	// component's stable callback address (ComponentCallbackPath).
	ServiceCallbacks = "callbacks"
	// ServiceStore: the component keeps sealed values in the platform
	// (ComponentStorePath).
	ServiceStore = "store"
)

// ServiceCapabilities is every primitive a service component may declare.
var ServiceCapabilities = []string{ServiceTurnProxy, ServiceEgress, ServiceRoomFacts, ServicePages, ServiceCallbacks, ServiceStore}

// ServiceNeeds is the primitive another one needs.
var ServiceNeeds = map[string]string{ServiceEgress: ServiceTurnProxy, ServiceRoomFacts: ServiceTurnProxy}

// Paths the component serves, appended to its endpoint base URL. Each
// primitive reaches only its own prefix: a turn's call never reaches a
// page, a callback never reaches a turn's route.
const (
	// ServiceDiscoverPath answers {name, contract}.
	ServiceDiscoverPath = "/discover"
	// ServiceTurnPrefix: TurnProxyPath/{component}/{path} reaches
	// ServiceTurnPrefix + path.
	ServiceTurnPrefix = "/turn/"
	// ServiceEgressHostsPath answers EgressHosts for the turn.
	ServiceEgressHostsPath = ServiceTurnPrefix + "egress/hosts"
	// ServiceEgressRequestPath takes an EgressRequest and answers an
	// EgressResponse.
	ServiceEgressRequestPath = ServiceTurnPrefix + "egress/request"
	// ServiceFactsPath answers Facts for the turn.
	ServiceFactsPath = ServiceTurnPrefix + "facts"
	// ServicePagesPath answers PageList; ServicePagesPath + "/{page}[/...]"
	// serves a page to a member (MemberHeader).
	ServicePagesPath = "/pages"
	// ServiceCallbackPrefix: ComponentCallbackPath/{component}/{path}
	// reaches ServiceCallbackPrefix + path.
	ServiceCallbackPrefix = "/callbacks/"
)

// Platform routes, on the gateway.
const (
	// TurnProxyPath + "{component}/{path}?room=&job=": a bridge's call for
	// a job it drives.
	TurnProxyPath = "/v1/turn-proxy/"
	// ComponentStorePath + "{scope}/{key}": a component's sealed values
	// (StoreScope), signed by the component.
	ComponentStorePath = "/v1/component-store/"
	// ComponentCallbackPath + "{component}/{path}": open; forwarded signed.
	ComponentCallbackPath = "/v1/component-callbacks/"
	// ComponentPagePath + "{ticket}/{path}": a page a member opened.
	ComponentPagePath = "/v1/component-pages/"
)

// Headers of the contract.
const (
	// TurnHeader carries the attested Turn (EncodeHeader) on a turn call.
	TurnHeader = "X-Bnbloc-Turn"
	// MemberHeader carries the Member a page is served to.
	MemberHeader = "X-Bnbloc-Member"
	// CallbackHeader carries the Callback facts on a callback.
	CallbackHeader = "X-Bnbloc-Callback"
	// ComponentHeader names the endpoint component that signed a call to
	// the platform (SignatureHeader with that component's credential):
	// "<kind>/<name>" ("service/<name>" for a service; a notifier's reply
	// routes take its name alone).
	ComponentHeader = "X-Bnbloc-Component"
	// AuditHeader, on a component's answer to a forwarded call, is one
	// Audit row the platform records in the organization's audit log,
	// attributed to the component.
	AuditHeader = "X-Bnbloc-Audit"
)

// Drives: whose agent runs the turn, as the platform attests it.
const (
	// DriveOwn: the requester's own agent (or a schedule they created).
	DriveOwn = "own"
	// DriveBorrowed: another member's agent the requester borrowed.
	DriveBorrowed = "borrowed"
	// DrivePlatform: a platform agent.
	DrivePlatform = "platform"
)

// Turn is the turn a turn call is for: the organization, room, channel and
// job, the person whose request it runs (Requester), the agent's owner
// (AgentOwner), the drive, whether its commands run in a sandbox, and the
// platform's public origin (for links the component hands out).
type Turn struct {
	Org        string `json:"org"`
	Room       string `json:"room"`
	Channel    string `json:"channel,omitempty"`
	Job        string `json:"job"`
	Requester  string `json:"requester"`
	AgentOwner string `json:"agentOwner"`
	Drive      string `json:"drive"`
	Origin     string `json:"origin"`
}

// Member is who a page is served to: their subject, organization and role
// there (Admin: they manage the organization), the page, the page's own
// address (PageURL: what its forms and scripts call, ending in "/") and the
// platform's public origin.
type Member struct {
	Subject string `json:"subject"`
	Org     string `json:"org"`
	Role    string `json:"role"`
	Admin   bool   `json:"admin,omitempty"`
	Page    string `json:"page"`
	PageURL string `json:"pageUrl"`
	Origin  string `json:"origin"`
}

// Callback is what a callback carries: the platform's public origin and
// the callback's own address.
type Callback struct {
	Origin string `json:"origin"`
	URL    string `json:"url"`
}

// Audit is one audit row a component records with its answer.
type Audit struct {
	Action  string `json:"action"`
	Target  string `json:"target,omitempty"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

// Facts are a component's part of the room's records for a turn: Line
// joins the records' list in the session guidance (at most MaxFactsLine
// characters, one line), Files are written under .room/<component>/ (paths
// relative to it, at most MaxFactsFiles and MaxFactsBytes in all).
type Facts struct {
	Line  string            `json:"line"`
	Files map[string]string `json:"files,omitempty"`
}

// Facts bounds.
const (
	MaxFactsLine  = 200
	MaxFactsFiles = 32
	MaxFactsBytes = 64 << 10
)

// CheckFacts reports the first thing wrong with f, nil when it is within
// the contract.
func CheckFacts(f Facts) error {
	if utf8.RuneCountInString(f.Line) > MaxFactsLine || strings.ContainsAny(f.Line, "\r\n") {
		return fmt.Errorf("the facts line must be one line of at most %d characters", MaxFactsLine)
	}
	if len(f.Files) > MaxFactsFiles {
		return fmt.Errorf("at most %d facts files", MaxFactsFiles)
	}
	total := 0
	for name, content := range f.Files {
		if !validFactsPath(name) {
			return fmt.Errorf("facts file %q: a relative path of letters, digits, '-', '_', '.' and '/'", name)
		}
		total += len(content)
	}
	if total > MaxFactsBytes {
		return fmt.Errorf("facts files exceed %d bytes", MaxFactsBytes)
	}
	return nil
}

func validFactsPath(p string) bool {
	clean, ok := core.CleanRelPath(p)
	if !ok || clean != p || len(p) > 128 || strings.HasPrefix(p, ".") || strings.Contains(p, "/.") {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./", r)) {
			return false
		}
	}
	return true
}

// EgressHosts are the hosts a component handles for the turn (exact names,
// lower case; none for a turn it will not serve), and the environment the
// session's commands get beside them (placeholders a CLI needs before it
// runs; never a credential, since every credential sent to a claimed host
// is replaced).
type EgressHosts struct {
	Hosts []string          `json:"hosts"`
	Env   map[string]string `json:"env,omitempty"`
}

// MaxEgressHosts bounds the hosts one component claims.
const MaxEgressHosts = 256

// ValidEgressHost reports an exact DNS name a component may claim: lower
// case, at least two labels, no port, no address.
func ValidEgressHost(h string) bool {
	if h == "" || len(h) > 253 || h != strings.ToLower(h) || !strings.Contains(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	last := h[strings.LastIndexByte(h, '.')+1:]
	return strings.Trim(last, "0123456789") != ""
}

// EgressRequest is one HTTPS request a turn's command sent to a claimed
// host, as the bridge read it: URL is absolute (https://host/path?query).
type EgressRequest struct {
	Method string              `json:"method"`
	URL    string              `json:"url"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

// EgressResponse is the answer the command gets, and a card for the
// requester when the component needs them (a connection to make, a change
// to allow): the bridge posts it in the room, shown only to them.
type EgressResponse struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
	Card   *Card               `json:"card,omitempty"`
}

// MaxEgressBody bounds an egress request's and answer's body.
const MaxEgressBody = 8 << 20

// Card is a component's card for one member: plain text the platform
// renders, and at most MaxCardActions actions.
type Card struct {
	Title   string       `json:"title"`
	Text    string       `json:"text,omitempty"`
	Actions []CardAction `json:"actions,omitempty"`
}

// Card actions.
const (
	// CardOpen opens one of the component's pages (Page, with Query).
	CardOpen = "open"
	// CardAskAgain sends the turn's request to the same agent again.
	CardAskAgain = "ask-again"
)

// CardAction is one button of a card.
type CardAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Page  string `json:"page,omitempty"`
	Query string `json:"query,omitempty"`
}

// Card bounds.
const (
	MaxCardTitle   = 120
	MaxCardText    = 1000
	MaxCardActions = 3
)

// CheckCard reports the first thing wrong with c, nil when it is within
// the contract.
func CheckCard(c Card) error {
	if t := strings.TrimSpace(c.Title); t == "" || utf8.RuneCountInString(t) > MaxCardTitle {
		return fmt.Errorf("a card needs a title of at most %d characters", MaxCardTitle)
	}
	if utf8.RuneCountInString(c.Text) > MaxCardText {
		return fmt.Errorf("a card's text is at most %d characters", MaxCardText)
	}
	if len(c.Actions) > MaxCardActions {
		return fmt.Errorf("a card has at most %d actions", MaxCardActions)
	}
	for _, a := range c.Actions {
		if l := strings.TrimSpace(a.Label); l == "" || utf8.RuneCountInString(l) > 40 {
			return errors.New("a card action needs a label of at most 40 characters")
		}
		switch a.Kind {
		case CardOpen:
			if !ValidPageName(a.Page) {
				return fmt.Errorf("card action %q opens no valid page", a.Label)
			}
			if len(a.Query) > 1024 {
				return errors.New("a card action's query is at most 1024 bytes")
			}
			if _, err := url.ParseQuery(a.Query); err != nil {
				return fmt.Errorf("card action %q: query: %w", a.Label, err)
			}
		case CardAskAgain:
		default:
			return fmt.Errorf("unknown card action kind %q", a.Kind)
		}
	}
	return nil
}

// Page audiences.
const (
	AudienceMember = "member"
	AudienceAdmin  = "admin"
)

// Page is one page a component serves: its name (ValidPageName), the label
// members see and who it is for. An admin page is listed only to an
// organization admin; the component still checks Member.Admin itself.
type Page struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Audience string `json:"audience"`
}

// PageList is the answer of ServicePagesPath.
type PageList struct {
	Pages []Page `json:"pages"`
}

// ValidPageName reports a page name: 1-32 of a-z, 0-9 and '-'.
func ValidPageName(n string) bool {
	if n == "" || len(n) > 32 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Store scopes: whose a value is. A member's values go when the member
// leaves the organization; every value goes when the component is
// uninstalled.
const (
	// StoreMembers + "/<subject>": one member's.
	StoreMembers = "members"
	// StoreOrgs + "/<org>": one organization's.
	StoreOrgs = "orgs"
	// StoreShared: the component's own, for every organization.
	StoreShared = "shared"
)

// MaxStoreValue bounds one stored value; MaxStoreKey one key.
const (
	MaxStoreValue = 64 << 10
	MaxStoreKey   = 128
)

// ValidStoreKey reports a key: 1-128 of letters, digits and "-_.:".
func ValidStoreKey(k string) bool {
	if k == "" || len(k) > MaxStoreKey {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:", r)) {
			return false
		}
	}
	return true
}

// StorePath is the platform path of a stored value: scope is StoreShared,
// or StoreMembers/StoreOrgs with the subject or organization.
func StorePath(scope, owner, key string) string {
	if scope == StoreShared {
		return ComponentStorePath + StoreShared + "/" + url.PathEscape(key)
	}
	return ComponentStorePath + scope + "/" + url.PathEscape(owner) + "/" + url.PathEscape(key)
}

// EncodeHeader encodes an identity (Turn, Member, Callback, Audit) as a
// header value: base64url of its JSON.
func EncodeHeader(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeHeader decodes a header value EncodeHeader made.
func DecodeHeader(s string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return errors.New("header is not base64url")
	}
	return json.Unmarshal(raw, v)
}
