package gateway

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// notifyGW is the app gateway with a notifier installed and the emitter's
// outbox in memory.
func notifyGW(t *testing.T) (*appGW, *store.MemoryNotifications, *recordedAudit) {
	t.Helper()
	e := newAppGW(t)
	if e.gw.PlatformSettings == nil {
		e.gw.PlatformSettings = store.NewMemoryPlatformSettings()
	}
	outbox := store.NewMemoryNotifications()
	installTestNotifier(t, e.gw.PlatformSettings, "n1")
	e.gw.Alerts = notify.NewEmitter(notify.Stores{Prefs: outbox, Outbox: outbox, Settings: e.gw.PlatformSettings, Installs: store.NewMemoryComponentInstall()}, "https://bnbloc.example.com")
	e.gw.PublicURL = "https://bnbloc.example.com"
	sink := &recordedAudit{}
	e.gw.ExtAudit = sink
	return e, outbox, sink
}

// queued is every notification payload the outbox holds for notifier n1.
func queued(t *testing.T, outbox *store.MemoryNotifications) []wire.NotificationPayload {
	t.Helper()
	var out []wire.NotificationPayload
	for _, it := range outbox.Items() {
		if it.Notifier != "n1" {
			continue
		}
		var p wire.NotificationPayload
		if err := json.Unmarshal(it.EventJSON, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func recipientsOf(p wire.NotificationPayload) []string {
	var out []string
	for _, r := range p.Recipients {
		out = append(out, r.UserID)
	}
	slices.Sort(out)
	return out
}

// A page's notice reaches the room's members (not the sender) or the
// logins it names in the organization, signed with the app's title and
// linked to it; a visitor notifies no one; only the owners name logins;
// a link off the deployment is refused; the same notice twice is one.
func TestAppNotify(t *testing.T) {
	e, outbox, sink := notifyGW(t)
	jack := e.ticketBase("jack", e.appA)
	r := e.call(http.MethodPost, jack+"~app/notify", map[string]any{"to": "room", "title": "Fill in your weekly report", "text": "Due Friday"})
	if r.status != http.StatusOK || r.json()["recipients"] != float64(2) {
		t.Fatalf("notify the room = %d %s", r.status, r.body)
	}
	got := queued(t, outbox)
	if len(got) != 1 {
		t.Fatalf("queued %d notices", len(got))
	}
	p := got[0]
	if p.Type != wire.NotificationAppNotice || !strings.Contains(p.Title, "Fill in your weekly report") || p.Text != "Due Friday" ||
		!strings.HasPrefix(p.URL, "https://bnbloc.example.com/#/room/"+e.roomID+"?artifact="+e.appA) || strings.Join(recipientsOf(p), ",") != "maya" {
		t.Fatalf("notice = %+v (recipients %v): the sender is never notified", p, recipientsOf(p))
	}
	if recs := sink.take(); len(recs) != 1 || recs[0].Kind != wire.AuditKindApp || recs[0].Action != "notify" || strings.Contains(recs[0].Detail, "Friday") {
		t.Fatalf("audit = %+v", recs)
	}
	// The same notice again: one notice.
	e.call(http.MethodPost, jack+"~app/notify", map[string]any{"to": "room", "title": "Fill in your weekly report", "text": "Due Friday"})
	if n := len(queued(t, outbox)); n != 1 {
		t.Fatalf("a repeated notice queued %d", n)
	}

	// Logins: the organization's only (olaf is another's, ghost nobody's).
	r = e.call(http.MethodPost, jack+"~app/notify", map[string]any{"to": []string{"nora", "olaf", "ghost", "maya"}, "title": "Your turn"})
	if r.status != http.StatusOK || r.json()["recipients"] != float64(2) {
		t.Fatalf("notify logins = %d %s", r.status, r.body)
	}
	for _, p := range queued(t, outbox) {
		if strings.Contains(p.Title, "Your turn") && strings.Join(recipientsOf(p), ",") != "maya,nora" {
			t.Fatalf("logins reached %v", recipientsOf(p))
		}
	}

	if r := e.call(http.MethodPost, jack+"~app/notify", map[string]any{"to": "room", "title": "x", "url": "https://evil.example/phish"}); r.status != http.StatusBadRequest {
		t.Fatalf("a link elsewhere = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, jack+"~app/notify", map[string]any{"to": "everyone", "title": "x"}); r.status != http.StatusBadRequest {
		t.Fatalf("an unknown to = %d %s", r.status, r.body)
	}

	// A signed-in link opener who is not a room member may notify the
	// room, not name logins.
	tok, _ := e.share(e.appA)
	nora := shareBase(e.ticket("nora", tok))
	if r := e.call(http.MethodPost, nora+"~app/notify", map[string]any{"to": []string{"maya"}, "title": "x"}); r.status != http.StatusForbidden {
		t.Fatalf("a non-owner names logins = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, nora+"~app/notify", map[string]any{"to": "room", "title": "Signed up"}); r.status != http.StatusOK || r.json()["recipients"] != float64(2) {
		t.Fatalf("a link opener notifies the room = %d %s", r.status, r.body)
	}

	anyone, _ := e.shareWith(e.appA, "anyone")
	vis := visitorHeader(e.visitor(anyone, ""))
	if r := e.call(http.MethodPost, shareBase(anyone)+"~app/notify", map[string]any{"to": "room", "title": "x"}, vis); r.status != http.StatusForbidden || r.json()["code"] != wire.AppSignInRequired {
		t.Fatalf("a visitor's notice = %d %s", r.status, r.body)
	}
}

// A member who turned app notices off, or muted the room, gets none.
func TestAppNotifyHonoursPrefs(t *testing.T) {
	e, outbox, _ := notifyGW(t)
	prefs := store.DefaultNotificationPrefs()
	prefs.AppNotices = false
	if err := outbox.PutNotificationPrefs(e.ctx(), "maya", prefs); err != nil {
		t.Fatal(err)
	}
	r := e.call(http.MethodPost, e.ticketBase("jack", e.appA)+"~app/notify", map[string]any{"to": []string{"maya"}, "title": "x"})
	if r.status != http.StatusOK || len(queued(t, outbox)) != 0 {
		t.Fatalf("a member who turned app notices off got one: %d %s, %d queued", r.status, r.body, len(queued(t, outbox)))
	}
}
