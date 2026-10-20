package gateway

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
)

// apiPath is a proxied request path parsed ONCE into decoded segments. Every
// edge decision — backend routing, room scope, org RBAC, rate limits — reads
// this structure, so they all agree on what the request addresses. Matching
// substrings of the raw path instead (the previous design) routed a room named
// "chat-team" or "events2026" to the wrong backend.
type apiPath struct {
	// segs are the decoded path segments, e.g. ["v1","rooms","r1","chat"].
	segs []string
	// RoomID is the room a /v1/rooms/{id}[/...] path addresses ("" otherwise).
	RoomID string
	// RoomSub is the room sub-resource (segs[3], e.g. "chat"); "" when the path
	// addresses the room itself or is not room-scoped.
	RoomSub string
}

var (
	errBadPath   = errors.New("malformed request path")
	errBadRoomID = errors.New("invalid room id")
)

// parseAPIPath splits the request's escaped path on "/" and unescapes each
// segment separately, so an encoded slash inside a segment (%2F) can never
// change the path's structure. A /v1/rooms/{id} room id must satisfy
// roomctx.ValidateID.
func parseAPIPath(r *http.Request) (apiPath, error) {
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	parts := strings.Split(raw, "/")
	segs := make([]string, 0, len(parts))
	for i, p := range parts {
		if p == "" {
			// Tolerate exactly one trailing slash; any other empty segment is
			// not a route.
			if i == len(parts)-1 && i > 0 {
				break
			}
			return apiPath{}, errBadPath
		}
		seg, err := url.PathUnescape(p)
		if err != nil {
			return apiPath{}, errBadPath
		}
		segs = append(segs, seg)
	}
	p := apiPath{segs: segs}
	if len(segs) >= 3 && segs[0] == "v1" && segs[1] == "rooms" {
		if err := roomctx.ValidateID(segs[2]); err != nil {
			return apiPath{}, errBadRoomID
		}
		p.RoomID = segs[2]
		if len(segs) >= 4 {
			p.RoomSub = segs[3]
		}
	}
	return p, nil
}

// is reports whether the path is exactly the given segments.
func (p apiPath) is(segs ...string) bool {
	if len(p.segs) != len(segs) {
		return false
	}
	for i := range segs {
		if p.segs[i] != segs[i] {
			return false
		}
	}
	return true
}

// isMyNotifiers matches the member notifier routes: /v1/my/notifiers and
// /v1/my/notifiers/{name}/test.
func (p apiPath) isMyNotifiers() bool {
	if len(p.segs) < 3 || p.segs[0] != "v1" || p.segs[1] != "my" || p.segs[2] != "notifiers" {
		return false
	}
	return len(p.segs) == 3 || (len(p.segs) == 5 && p.segs[4] == "test")
}

// isMyPluginEvents matches the member plugin hook routes:
// /v1/my/plugin-events (SSE) and /v1/my/plugin-events/{id}/ack.
func (p apiPath) isMyPluginEvents() bool {
	if len(p.segs) < 3 || p.segs[0] != "v1" || p.segs[1] != "my" || p.segs[2] != "plugin-events" {
		return false
	}
	return len(p.segs) == 3 || (len(p.segs) == 5 && p.segs[4] == "ack")
}

// section returns the top-level API section (segs[1] of /v1/<section>/...).
func (p apiPath) section() string {
	if len(p.segs) < 2 || p.segs[0] != "v1" {
		return ""
	}
	return p.segs[1]
}

// roomScoped reports whether the path addresses a room sub-resource, which a
// token may reach only for rooms it is a member of.
func (p apiPath) roomScoped() bool { return p.RoomID != "" && p.RoomSub != "" }

// namesRoom reports whether the path names one room: the room itself or any
// of its sub-resources (not the /v1/rooms/import collection route). Such a
// request acts in the room's own tenant (tenant_scope.go).
func (p apiPath) namesRoom() bool { return p.RoomID != "" && !p.isRoomImport() }

// searchRoom is the one room a global search is narrowed to
// (GET /v1/search?rooms=<id>): the in-room search, which acts in that room's
// tenant like the room's own paths. "" for a search over several rooms or
// all of the caller's (the session tenant's).
func (p apiPath) searchRoom(r *http.Request) string {
	if !p.is("v1", "search") {
		return ""
	}
	rooms := strings.Split(r.URL.Query().Get("rooms"), ",")
	if len(rooms) != 1 {
		return ""
	}
	room := strings.TrimSpace(rooms[0])
	if roomctx.ValidateID(room) != nil {
		return ""
	}
	return room
}

// endsMembership reports a request that ends a room membership: removing a
// member (DELETE /v1/rooms/{id}/members/{user}), leaving (POST
// /v1/rooms/{id}/leave, the caller), or deleting the room (every member,
// user ""). The edge forgets its cached answers for it.
func (p apiPath) endsMembership(method, caller string) (user string, ok bool) {
	switch {
	case p.RoomID == "" || p.isRoomImport():
		return "", false
	case method == http.MethodDelete && len(p.segs) == 3:
		return "", true
	case method == http.MethodDelete && len(p.segs) == 5 && p.RoomSub == "members":
		return p.segs[4], true
	case method == http.MethodPost && len(p.segs) == 4 && p.RoomSub == "leave":
		return caller, true
	}
	return "", false
}

// isArtifactShareRevoke reports DELETE
// /v1/rooms/{id}/artifacts/{artifactId}/shares/{shareId}. An org admin may
// revoke any public link of their organization without being a member of
// its room (the room service re-checks the org role in the room's tenant);
// it is the only room route with that exemption.
// isArtifactShareCreate is POST /v1/rooms/{id}/artifacts/{artifactId}/shares:
// a link's create, whose answer the proxy completes with the link's URL
// (withShareURL).
func (p apiPath) isArtifactShareCreate(method string) bool {
	return method == http.MethodPost && len(p.segs) == 6 && p.RoomSub == "artifacts" && p.segs[5] == "shares"
}

// isArtifactShareLink is GET /v1/rooms/{id}/artifacts/{artifactId}/shares/{shareId}/link:
// its creator's copy of the link again, whose answer the proxy turns into the
// link's URL (withShareLinkURL).
func (p apiPath) isArtifactShareLink(method string) bool {
	return method == http.MethodGet && len(p.segs) == 8 && p.RoomSub == "artifacts" && p.segs[5] == "shares" && p.segs[7] == "link"
}

func (p apiPath) isArtifactShareRevoke(method string) bool {
	return method == http.MethodDelete && len(p.segs) == 7 && p.RoomSub == "artifacts" && p.segs[5] == "shares"
}

// route maps a parsed path to the owning backend base URL ("" = no route).
// Room sub-resources route on the exact sub-resource segment.
func (s *Service) route(p apiPath) string {
	switch p.section() {
	case "bridge":
		return s.Backends.Activity
	case "my":
		if len(p.segs) >= 3 && p.segs[2] == "agents" {
			return s.Backends.Activity
		}
		// Agent run records (R10) of the caller's agents and requests.
		if len(p.segs) >= 3 && p.segs[2] == "jobs" {
			return s.Backends.Activity
		}
		// The member's own monthly usage budget and burn (R12).
		if p.is("v1", "my", "budget") {
			return s.Backends.Activity
		}
		// Member notification preferences and the notifier list/test send
		// live with the notification producer (realtime).
		if p.is("v1", "my", "notification-prefs") || p.isMyNotifiers() || p.is("v1", "my", "push-subscriptions") {
			return s.Backends.Realtime
		}
		// The member's plugin hook stream and its acks: realtime is the
		// hook authority and auditor.
		if p.isMyPluginEvents() {
			return s.Backends.Realtime
		}
		// The tenant policy switches (read-only for members; the owner's
		// bridge enforces the sandbox switch, the web hides sharing while
		// public links are off): shareagent owns the tenant registry.
		if p.is("v1", "my", "tenant-policy") {
			return s.Backends.ShareAgent
		}
		// The member's own display name: the room service owns profiles
		// (beside /v1/users/profiles, the batch lookup).
		if p.is("v1", "my", "profile") {
			return s.Backends.Room
		}
		// The member's interactive apps across their rooms: the room
		// service owns rooms, artifacts, app data and share links.
		if p.is("v1", "my", "apps") {
			return s.Backends.Room
		}
		return ""
	case "rooms-unread":
		return s.Backends.Realtime
	case "push":
		// The browser push channel's public VAPID key (N4).
		return s.Backends.Realtime
	case "search":
		// Global search (F7) lives beside the room feed it builds on.
		return s.Backends.Realtime
	case "rooms":
		switch p.RoomSub {
		case "jobs":
			// A job's live view (frame, control, input) is held by the
			// room's realtime owner; the run records are activity's.
			if len(p.segs) >= 6 && p.segs[5] == "live" {
				return s.Backends.Realtime
			}
			return s.Backends.Activity
		case "agent-jobs", "agent-activity", "agent-catalogs", "team-plan":
			// "jobs" are the room's agent run records (R10); "agent-catalogs"
			// the model catalogs of the agents addressable in the room;
			// "team-plan" staffs an expert team from those agents and
			// estimates it from their runs.
			return s.Backends.Activity
		case "shared-agents", "shared-agents-usage", "platform-agents":
			// A room's platform agents are platform grants (shareagent).
			return s.Backends.ShareAgent
		case "events", "agents", "chat", "attachments", "history", "typing", "read", "messages", "tasks":
			return s.Backends.Realtime
		case "live":
			// The room's live views now (wire.JobLiveList), beside each
			// job's own.
			return s.Backends.Realtime
		case "dispatch":
			// Agent dispatch (room tools): realtime's internal route, see
			// isRoomDispatch.
			return s.Backends.Realtime
		case "schedules":
			// Scheduled prompts (N2) run beside the chat publish they use.
			return s.Backends.Realtime
		default:
			// The collection, the room itself, and every other room
			// sub-resource (members, invites, channels, artifacts, ...).
			return s.Backends.Room
		}
	case "team":
		// Team agent dashboard (F1), the caller's own agents: activity owns
		// the bridge/job/reply data it aggregates.
		return s.Backends.Activity
	case "borrow":
		// Borrow billing (F3): usage reports and quota requests over the
		// shareagent grants/audit.
		return s.Backends.ShareAgent
	case "platform-agents":
		// The platform agents published for the caller's organization; the
		// super-admin policy is /v1/platform/platform-agents.
		return s.Backends.ShareAgent
	case "platform", "tenants", "tenant":
		// The tenant-wide team dashboard (F1) lives with the rest of the team
		// dashboard on activity; manage_org still gates it (requiredPermission).
		if p.section() == "tenant" && len(p.segs) >= 3 && p.segs[2] == "team" {
			return s.Backends.Activity
		}
		// Tenant-wide agent run records (R10), beside the dashboard.
		if p.section() == "tenant" && len(p.segs) >= 3 && p.segs[2] == "jobs" {
			return s.Backends.Activity
		}
		// Monthly usage budgets (R12, manage_org) live with the usage they
		// are measured against and the job claim that enforces them.
		if p.is("v1", "tenant", "budgets") {
			return s.Backends.Activity
		}
		// Search reindex and status (super-admin) run where the room feed
		// and its search backend live.
		if p.is("v1", "platform", "search", "reindex") || p.is("v1", "platform", "search", "status") {
			return s.Backends.Realtime
		}
		// The org-admin list of public artifact share links: the room
		// service owns artifacts and their links.
		if p.is("v1", "tenant", "shares") {
			return s.Backends.Room
		}
		// Platform (super-admin), self-service join-request, and org-admin
		// endpoints live on shareagent, which owns the tenant registry; RBAC
		// gates each (see requiredPermission).
		return s.Backends.ShareAgent
	case "invites", "users":
		return s.Backends.Room
	default:
		return ""
	}
}

// requiredPermission maps a request to the coarse org permission it needs.
// Reads (GET/HEAD) need only PermRead; room creation and invite redemption need
// PermCreateRoom; any other write defaults to PermDriveAgent (fail toward
// requiring a non-viewer). A viewer holds only PermRead.
func requiredPermission(method string, p apiPath) rbac.Permission {
	switch p.section() {
	case "platform":
		// Cross-tenant administration: the most privileged, even for GET.
		return rbac.PermManagePlatform
	case "tenant":
		// Org-admin endpoints (audit, usage) are privileged even for GET.
		return rbac.PermManageOrg
	case "tenants":
		// Self-service join requests: applying is what a non-member does.
		return rbac.PermRead
	}
	if method == http.MethodGet || method == http.MethodHead {
		return rbac.PermRead
	}
	// A member's own notification preferences, notifier test sends and
	// browser push subscriptions are not cost-bearing: a read-only viewer
	// may still mute rooms, turn notifications off, or check how they are
	// reached.
	if p.is("v1", "my", "notification-prefs") || p.isMyNotifiers() || p.is("v1", "my", "push-subscriptions") {
		return rbac.PermRead
	}
	// Acknowledging a plugin hook delivery only reports what the member's
	// own desktop did; it is not cost-bearing.
	if p.isMyPluginEvents() {
		return rbac.PermRead
	}
	// Choosing one's own display name is not cost-bearing: a viewer may set
	// theirs too.
	if p.is("v1", "my", "profile") {
		return rbac.PermRead
	}
	switch {
	case method == http.MethodPost && p.createsRoom():
		return rbac.PermCreateRoom
	case method == http.MethodPost && len(p.segs) == 4 && p.section() == "invites" && p.segs[3] == "redeem":
		return rbac.PermCreateRoom
	case method == http.MethodPost && len(p.segs) == 4 && readerRoomAction(p.RoomSub):
		// A viewer's own read-side room actions: marking read, the typing
		// hint, and joining/leaving a shared room are not cost-bearing.
		// Refusing them left a viewer's unread count uncleared forever.
		return rbac.PermRead
	default:
		return rbac.PermDriveAgent
	}
}

// readerRoomAction reports the room sub-resources a read-only member may POST.
func readerRoomAction(sub string) bool {
	switch sub {
	case "read", "typing", "join", "leave":
		return true
	}
	return false
}

// rateLimited reports whether a request is a cost-bearing write limited per
// subject: creating a room, sending chat (drives agents), and borrow actions
// (consume another owner's quota). Reads, SSE, and non-cost paths are exempt.
func rateLimited(method string, p apiPath) bool {
	if method != http.MethodPost && method != http.MethodPut {
		return false
	}
	if method == http.MethodPost && p.createsRoom() {
		return true
	}
	// Running a schedule now posts chat (and may drive an agent).
	if method == http.MethodPost && p.RoomSub == "schedules" && len(p.segs) == 6 && p.segs[5] == "run" {
		return true
	}
	// An agent dispatch posts chat on a member's behalf (and drives an
	// agent): the dispatching member's chat budget.
	return p.RoomSub == "chat" || p.RoomSub == "shared-agents" || p.isRoomDispatch(method)
}

// isRoomDispatch reports POST /v1/rooms/{id}/dispatch: a bridge's room tools
// server asking another agent for work on behalf of the job it runs. It is
// the public door of realtime's svcauth-only POST
// /internal/v1/rooms/{id}/dispatch: after the ordinary edge checks (room
// scope, drive_agent, the chat rate limit) the proxy rewrites the path and
// signs the caller (the bridge's owner) like every proxied request.
func (p apiPath) isRoomDispatch(method string) bool {
	return method == http.MethodPost && len(p.segs) == 4 && p.RoomSub == "dispatch"
}

// createsRoom reports the POST routes that create a room, which carry room
// creation's permission and rate limit: the collection, a handoff import
// and a local agent run (it adopts the agent's workspace as a room and
// drives the agent there; every role that may create a room may drive an
// agent).
func (p apiPath) createsRoom() bool {
	return p.is("v1", "rooms") || p.isRoomImport() || p.is("v1", "local-agent", "run")
}

// isRoomImport reports /v1/rooms/import: a handoff package import creates a
// room, so it carries room creation's permission and rate limit. (The segment
// parses as a room id; no room route answers POST on a bare room path.)
func (p apiPath) isRoomImport() bool { return p.is("v1", "rooms", "import") }

// isInviteRedeem reports POST /v1/invites/{code}/redeem, the per-IP
// brute-forceable invite acceptance endpoint.
func isInviteRedeem(method string, p apiPath) bool {
	return method == http.MethodPost && len(p.segs) == 4 && p.section() == "invites" && p.segs[3] == "redeem"
}

// isEventStream reports GET /v1/rooms/{id}/events (SSE).
func (p apiPath) isEventStream(method string) bool {
	return method == http.MethodGet && len(p.segs) == 4 && p.RoomSub == "events"
}

// isAttachmentDownload reports GET /v1/rooms/{id}/attachments/{attachmentId}.
func (p apiPath) isAttachmentDownload(method string) bool {
	return method == http.MethodGet && len(p.segs) == 5 && p.RoomSub == "attachments"
}
