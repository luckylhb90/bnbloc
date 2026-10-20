package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Payload text bounds: a notification is a pointer back into the room, not a
// copy of the conversation.
const (
	maxTitleRunes = 200
	maxTextRunes  = 500
)

// collapseWindow is the per-(member, room, type) burst window: within it a
// member gets at most one notification of that type for that room. Bursts
// (an agent finishing several turns back to back, a chatty thread of
// mentions, a borrower retrying) collapse into the first notification.
var collapseWindow = map[string]time.Duration{
	wire.NotificationMention:       30 * time.Second,
	wire.NotificationAgentFinished: 30 * time.Second,
	wire.NotificationAgentFailed:   30 * time.Second,
	wire.NotificationBorrowRequest: 5 * time.Minute,
}

// Event is one notification-worthy happening, before preference filtering.
type Event struct {
	Type string // wire.Notification*
	// RoomID is the room the event happened in.
	RoomID string
	// Recipients are the candidate members to notify; the actor, members who
	// turned the type off or muted the room, and collapsed duplicates are
	// filtered out.
	Recipients []string
	// Actor is who caused the event (a member id, or owner/provider for an
	// agent). Never notified about their own action.
	Actor string
	// Title is the headline without the room ("alice mentioned you"); the
	// emitter appends " in <room name>" (not for a room-less event).
	Title string
	Text  string
	// URL, when set, replaces the room deep link (a room-less admin alert
	// links wherever its sender points).
	URL string
	// DedupKey identifies the logical event (e.g. "job:<id>"): the same key
	// enqueues once, however often the event is observed.
	DedupKey string
	// Seq (and ChannelID) anchor the deep link at one room message: the
	// mentioning message, the agent's reply, or the request a failed job
	// answered. 0 links to the room.
	Seq       int64
	ChannelID string
	// Reply, when set, makes the notification answerable (N1): where a
	// member's reply from the notifier is posted (agentReplyTo/roomReplyTo).
	Reply *wire.NotificationReply
	// RoomName, when set, is the room's display name, read by the producer
	// already (else RoomName resolves it).
	RoomName string
	// Group, when set, addresses the event to a connected group chat
	// instead of members (EmitGroup): its name, the notifier it was
	// connected with and that notifier's address of it. Recipients is
	// ignored.
	Group, GroupNotifier, GroupAddress string
}

// ErrNoGroupNotifier: the group chat's notifier is not distributed to the
// tenant, or does not declare the groups capability (any more).
var ErrNoGroupNotifier = errors.New("the group chat's notifier does not deliver to groups here")

// EmitGroup queues ev for its group chat through the notifier it was
// connected with, when that notifier is distributed to the tenant and
// declares the groups capability (an opt-in notifier included: a group is
// not a member who could opt in). Members' preferences do not apply (the
// group is no member); the producer's rate limits and dedup key do.
func (e *Emitter) EmitGroup(ctx context.Context, ev Event) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(ev.Group) == "" || ev.GroupNotifier == "" || ev.GroupAddress == "" {
		return false, errors.New("a group event needs its group, notifier and address")
	}
	targets, err := e.Notifiers.Targets(ctx, tid)
	if err != nil {
		return false, err
	}
	now := e.now()
	payload := e.payload(ctx, tid, ev, now)
	payload.Group, payload.GroupAddress, payload.Recipients = ev.Group, ev.GroupAddress, []wire.NotificationRecipient{}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	any, queued := false, false
	for _, t := range targets {
		if !t.Groups || t.Name != ev.GroupNotifier {
			continue
		}
		any = true
		inserted, err := e.Outbox.EnqueueNotification(ctx, store.NotificationOutboxItem{
			ID: payload.ID, Notifier: t.Name, EventJSON: body, NextAttemptAtMs: now.UnixMilli(), CreatedAtMs: now.UnixMilli(),
		})
		if err != nil {
			return queued, err
		}
		queued = queued || inserted
	}
	if !any {
		return false, ErrNoGroupNotifier
	}
	if queued && e.OnEnqueue != nil {
		e.OnEnqueue()
	}
	return queued, nil
}

// Emitter turns Events into outbox items for the context tenant. It is the
// one producer seam: realtime (mentions, agent outcomes), shareagent (borrow
// requests) and the gateway (admin alerts) all build one over the same
// stores (NewEmitter) and only enqueue.
//
// Routing (after the preference/mute/collapse filter): while a notification
// plugin (notification.v1) is distributed to the tenant, the event is queued
// once on DesktopChannel with every recipient, and the outbox worker — in
// realtime, which holds the members' plugin event streams — routes each
// recipient to their desktop plugin or to the server notifiers, with a
// durable fallback (desktop.go). Otherwise it is queued directly as one item
// per visible notifier component. Browser push is a separate device channel
// and is queued for every subscribed recipient either way.
type Emitter struct {
	Prefs     store.NotificationPrefsStore
	Outbox    store.NotificationOutboxStore
	Notifiers *Directory
	// Installs gates opt-in notifiers: only members who installed the
	// component receive through it.
	Installs store.ComponentInstallStore
	// Push is the built-in Web Push channel's subscription store (nil = no
	// push): recipients with a browser subscription also get an item for
	// WebPushChannel.
	Push store.WebPushSubscriptionStore
	// Plugins reads the plugin install records: an event goes through
	// DesktopChannel while a notification plugin is distributed to its
	// tenant (nil = never).
	Plugins *Directory
	// RoomName resolves a room's display name ("" falls back to the id).
	RoomName func(ctx context.Context, roomID string) string
	// PublicURL is the public web origin deep links are built on ("" = no url).
	PublicURL string
	// OnEnqueue is called after a new item is queued (wakes the worker).
	OnEnqueue func()
	Now       func() time.Time

	mu       sync.Mutex
	lastSent map[string]time.Time
}

func (e *Emitter) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Emit filters ev's recipients and queues the event: on DesktopChannel
// while a notification plugin is distributed to the tenant (the worker
// routes each recipient), else through every notifier component the
// tenant's event goes to (opt-in components only for members who installed
// them); plus browser push for subscribed recipients. It is a no-op (false,
// nil) when nothing could carry the event or nobody is left to notify.
func (e *Emitter) Emit(ctx context.Context, ev Event) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	targets, err := e.Notifiers.Targets(ctx, tid)
	if err != nil {
		return false, err
	}
	desktop := e.desktopRoutable(ctx, tid)
	if len(targets) == 0 && e.Push == nil && !desktop {
		return false, nil
	}
	now := e.now()
	recipients := e.filter(ctx, tid, ev, now)
	if len(recipients) == 0 {
		return false, nil
	}
	payload := e.payload(ctx, tid, ev, now)
	var channels []channel
	if desktop {
		channels = []channel{{name: DesktopChannel, rs: recipients}}
	} else {
		// One delivery channel per visible notifier component.
		channels = e.notifierChannels(ctx, targets, recipients)
	}
	// The built-in Web Push channel for recipients with a browser subscription.
	if e.Push != nil {
		channels = append(channels, channel{name: WebPushChannel, rs: pushSubscribed(ctx, e.Push, recipients)})
	}
	return e.enqueue(ctx, payload, payload.ID, channels, now, deepLink{channelID: ev.ChannelID, seq: ev.Seq})
}

// channel is one outbox target of an event: a notifier component (or the
// built-in push channel) and the recipients it carries.
type channel struct {
	name string
	rs   []string
}

// notifierChannels assigns recipients to the notifier components targets
// (opt-in ones keep only the members who installed them).
func (e *Emitter) notifierChannels(ctx context.Context, targets []Target, recipients []string) []channel {
	if len(recipients) == 0 {
		return nil
	}
	channels := make([]channel, 0, len(targets)+1)
	for _, t := range targets {
		rs := recipients
		if t.OptIn {
			rs = e.optedIn(ctx, t.Name, recipients)
		}
		channels = append(channels, channel{name: t.Name, rs: rs})
	}
	return channels
}

// enqueue writes one outbox item per non-empty channel under rowID (the
// event id, or a per-member fallback id) and wakes the worker. link is the
// message anchor a DesktopChannel item keeps for the plugin payload.
func (e *Emitter) enqueue(ctx context.Context, payload wire.NotificationPayload, rowID string, channels []channel, now time.Time, link deepLink) (bool, error) {
	queued := false
	for _, t := range channels {
		if len(t.rs) == 0 {
			continue
		}
		// The outbox stores recipient ids only; reply tokens are minted by
		// the worker at send time.
		payload.Recipients = recipientIDs(t.rs)
		var body []byte
		var err error
		if t.name == DesktopChannel {
			body, err = json.Marshal(desktopItem{Payload: payload, ChannelID: link.channelID, Seq: link.seq})
		} else {
			body, err = json.Marshal(payload)
		}
		if err != nil {
			return queued, err
		}
		inserted, err := e.Outbox.EnqueueNotification(ctx, store.NotificationOutboxItem{
			ID: rowID, Notifier: t.name, EventJSON: body, NextAttemptAtMs: now.UnixMilli(), CreatedAtMs: now.UnixMilli(),
		})
		if err != nil {
			return queued, err
		}
		queued = queued || inserted
	}
	if queued && e.OnEnqueue != nil {
		e.OnEnqueue()
	}
	return queued, nil
}

// FallbackRowID is the outbox row id of one member's server delivery after
// their desktop plugin did not take the notification eventID. The payload
// keeps the event id, so a notifier still de-duplicates on (id, userId).
func FallbackRowID(eventID, member string) string {
	sum := sha256.Sum256([]byte(member))
	return eventID + "-" + hex.EncodeToString(sum[:8])
}

// payload builds the notifier payload of ev (recipients are set per channel).
func (e *Emitter) payload(ctx context.Context, tid string, ev Event, now time.Time) wire.NotificationPayload {
	roomName := ev.RoomID
	if ev.RoomName != "" {
		roomName = ev.RoomName
	} else if e.RoomName != nil {
		if n := strings.TrimSpace(e.RoomName(ctx, ev.RoomID)); n != "" {
			roomName = n
		}
	}
	payload := wire.NotificationPayload{
		ID:       EventID(tid, ev.Type, ev.DedupKey),
		Type:     ev.Type,
		TenantID: tid,
		RoomID:   ev.RoomID,
		RoomName: roomName,
		Actor:    ev.Actor,
		Title:    truncateRunes(ev.Title+" in "+roomName, maxTitleRunes),
		Text:     truncateRunes(ev.Text, maxTextRunes),
		URL:      MessageURL(e.PublicURL, ev.RoomID, ev.ChannelID, ev.Seq),
		AtMs:     now.UnixMilli(),
		Reply:    ev.Reply,
	}
	if ev.RoomID == "" {
		// A room-less event (an admin alert) links only where its sender
		// points (ev.URL), never at a default page.
		payload.Title = truncateRunes(ev.Title, maxTitleRunes)
		payload.URL = ""
	}
	if ev.URL != "" {
		payload.URL = ev.URL
	}
	return payload
}

func recipientIDs(ids []string) []wire.NotificationRecipient {
	out := make([]wire.NotificationRecipient, len(ids))
	for i, id := range ids {
		out[i] = wire.NotificationRecipient{UserID: id}
	}
	return out
}

// optedIn keeps the members who installed the opt-in notifier name. A read
// error drops the member (fail closed: never deliver through a component the
// member did not choose).
func (e *Emitter) optedIn(ctx context.Context, name string, members []string) []string {
	if e.Installs == nil {
		return nil
	}
	var out []string
	for _, m := range members {
		if MemberInstalledAt(ctx, e.Installs, m, name) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// MemberInstalledAt is when member opted in to the notifier name, 0 when
// they did not (or the read failed: fail closed, never deliver through a
// component the member did not choose).
func MemberInstalledAt(ctx context.Context, installs store.ComponentInstallStore, member, name string) int64 {
	if installs == nil {
		return 0
	}
	rows, err := installs.ListComponentInstalls(ctx, member)
	if err != nil {
		return 0
	}
	for _, in := range rows {
		if in.Kind == string(extreg.KindNotifier) && in.Name == name {
			return in.InstalledAtMs
		}
	}
	return 0
}

// filter drops the actor, duplicates, members whose prefs exclude the type or
// the room, and members inside the type's collapse window.
func (e *Emitter) filter(ctx context.Context, tid string, ev Event, now time.Time) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range ev.Recipients {
		r = strings.TrimSpace(r)
		operational := Operational(ev.Type)
		if r == "" || seen[r] || (!operational && r == ev.Actor) {
			continue
		}
		seen[r] = true
		if operational {
			// Explicit recipients only: no prefs, mutes, or collapsing.
			out = append(out, r)
			continue
		}
		prefs, ok, err := e.Prefs.GetNotificationPrefs(ctx, r)
		if err != nil {
			continue // fail closed for this member: a read error never spams
		}
		if !ok {
			prefs = store.DefaultNotificationPrefs()
		}
		if !Allows(prefs, ev.Type, ev.RoomID) {
			continue
		}
		if !e.admit(tid, r, ev.RoomID, ev.Type, now) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// admit applies the collapse window; it records the send when admitted.
func (e *Emitter) admit(tid, user, roomID, typ string, now time.Time) bool {
	window, ok := collapseWindow[typ]
	if !ok {
		return true
	}
	key := tid + "\x00" + user + "\x00" + roomID + "\x00" + typ
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastSent == nil {
		e.lastSent = map[string]time.Time{}
	}
	if last, ok := e.lastSent[key]; ok && now.Sub(last) < window {
		return false
	}
	// Bound the map: drop entries older than the longest window.
	if len(e.lastSent) > 10000 {
		for k, t := range e.lastSent {
			if now.Sub(t) > 5*time.Minute {
				delete(e.lastSent, k)
			}
		}
	}
	e.lastSent[key] = now
	return true
}

// Allows reports whether prefs let a notification of typ through for roomID.
func Allows(p store.NotificationPrefs, typ, roomID string) bool {
	for _, q := range p.QuietRooms {
		if q == roomID {
			return false
		}
	}
	switch typ {
	case wire.NotificationMention:
		return p.Mentions
	case wire.NotificationAgentFinished:
		return p.AgentFinished
	case wire.NotificationAgentFailed:
		return p.AgentFailed
	case wire.NotificationBorrowRequest:
		return p.BorrowRequests
	case wire.NotificationContentShared:
		return p.ContentShared
	case wire.NotificationContentReview:
		return p.ContentReview
	case wire.NotificationAppNotice:
		return p.AppNotices
	case wire.NotificationRoomReport:
		return p.RoomReports
	}
	return false
}

// EventID derives the delivery id (the receiver's idempotency key) from the
// logical event, so re-observing an event can never enqueue it twice.
func EventID(tenantID, typ, dedupKey string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + typ + "\x00" + dedupKey))
	return "ntf_" + hex.EncodeToString(sum[:16])
}

// RoomURL is the web deep link to a room ("" when no public URL is set).
func RoomURL(publicURL, roomID string) string {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		return ""
	}
	if roomID == "" {
		return publicURL + "/"
	}
	return publicURL + "/#/room/" + url.PathEscape(roomID)
}

func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
