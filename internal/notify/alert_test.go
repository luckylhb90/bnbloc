package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An admin alert reaches exactly its recipients: the sender is not skipped,
// and member preferences, muted rooms and the collapse window do not apply.
func TestSendAlertIgnoresPrefsAndKeepsSender(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	ctx := tctx()
	// root turned everything off and muted every room it could.
	_ = f.mem.PutNotificationPrefs(ctx, "root", store.NotificationPrefs{QuietRooms: []string{""}})

	res, err := f.emitter.SendAlert(ctx, Alert{
		Title: "bnbloc smoke failed", Text: "FAIL chat echo: timeout", URL: "https://gw.example.com",
		Recipients: []string{"root", " ops ", "ROOT"}, Actor: "root", DedupKey: "a1",
	})
	if err != nil {
		t.Fatalf("send alert: %v", err)
	}
	if !reflect.DeepEqual(res.Recipients, []string{"root", "ops"}) || !reflect.DeepEqual(res.Notifiers, []string{"n1"}) {
		t.Fatalf("response = %+v", res)
	}
	p, ok := f.payload(t, wire.NotificationAdminAlert, "alert:a1", "n1")
	if !ok {
		t.Fatal("no outbox item")
	}
	if res.ID != p.ID || !reflect.DeepEqual(userIDs(p), []string{"root", "ops"}) {
		t.Fatalf("payload recipients = %v id %s (response id %s)", userIDs(p), p.ID, res.ID)
	}
	// Room-less: no " in <room>" suffix, and the sender's link.
	if p.Title != "bnbloc smoke failed" || p.URL != "https://gw.example.com" || p.Type != wire.NotificationAdminAlert {
		t.Fatalf("payload = %+v", p)
	}
	// A second alert right away is not collapsed.
	if _, err := f.emitter.SendAlert(ctx, Alert{Title: "again", Recipients: []string{"root"}, Actor: "root", DedupKey: "a2"}); err != nil {
		t.Fatalf("second alert: %v", err)
	}
	if _, ok := f.payload(t, wire.NotificationAdminAlert, "alert:a2", "n1"); !ok {
		t.Fatal("second alert collapsed")
	}
}

func TestSendAlertValidationAndNoNotifier(t *testing.T) {
	f := newFixture(t)
	ctx := tctx()
	for name, a := range map[string]Alert{
		"no title":   {Recipients: []string{"r"}},
		"long title": {Title: strings.Repeat("t", 201), Recipients: []string{"r"}},
		"long text":  {Title: "t", Text: strings.Repeat("x", 501), Recipients: []string{"r"}},
		"bad url":    {Title: "t", URL: "javascript:alert(1)", Recipients: []string{"r"}},
	} {
		if _, err := f.emitter.SendAlert(ctx, a); !errors.Is(err, ErrAlertInvalid) {
			t.Errorf("%s: err = %v, want ErrAlertInvalid", name, err)
		}
	}
	if _, err := f.emitter.SendAlert(ctx, Alert{Title: "t", Recipients: []string{" "}}); !errors.Is(err, ErrAlertNoRecipient) {
		t.Fatalf("no recipients err = %v", err)
	}
	// Nothing installed: nothing would carry it.
	if _, err := f.emitter.SendAlert(ctx, Alert{Title: "t", Recipients: []string{"root"}, DedupKey: "x"}); !errors.Is(err, ErrAlertNoNotifier) {
		t.Fatalf("no notifier err = %v", err)
	}
	// An opt-in notifier nobody opted in to does not carry it either.
	f.install(t, "opt", fakeNotifier(t, nil).URL+"/hook", "optin")
	if _, err := f.emitter.SendAlert(ctx, Alert{Title: "t", Recipients: []string{"root"}, DedupKey: "y"}); !errors.Is(err, ErrAlertNoNotifier) {
		t.Fatalf("optin-only err = %v", err)
	}
}

// The worker counts outcomes and publishes the outbox snapshot.
func TestWorkerMetrics(t *testing.T) {
	f := newFixture(t)
	ok200 := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	f.install(t, "n1", fakeNotifier(t, ok200).URL+"/hook", "")
	ctx := tctx()
	m := obs.NewMetrics("svc")
	RegisterMetrics(m)
	w := f.worker()
	w.Metrics = m
	if ok, err := f.emitter.Emit(ctx, Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"bob"}, Actor: "alice", Title: "t", DedupKey: "m1"}); !ok || err != nil {
		t.Fatalf("emit = %v %v", ok, err)
	}
	w.sampleOutbox(context.Background())
	body := scrape(m)
	for _, want := range []string{
		`bnbloc_notification_outbox_items{service="svc",state="queued"} 1`,
		`bnbloc_notification_deliveries_total{service="svc",outcome="failed"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	if _, err := w.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	w.sampleOutbox(context.Background())
	body = scrape(m)
	for _, want := range []string{
		`bnbloc_notification_deliveries_total{service="svc",outcome="delivered"} 1`,
		`bnbloc_notification_outbox_items{service="svc",state="delivered"} 1`,
		`bnbloc_notification_outbox_items{service="svc",state="queued"} 0`,
		`bnbloc_notification_outbox_oldest_pending_age_seconds{service="svc"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}

func scrape(m *obs.Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}
