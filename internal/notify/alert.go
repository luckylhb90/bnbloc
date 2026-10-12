package notify

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Operational reports whether typ is an operational notification: it goes to
// exactly its explicit recipients — the actor is not skipped, and member
// preferences, muted rooms and burst collapsing do not apply (an operator
// must not be able to mute "the nightly smoke failed", nor a member the
// warning that their agents are about to stop).
func Operational(typ string) bool {
	return typ == wire.NotificationAdminAlert || typ == wire.NotificationBudgetWarning
}

// Admin alert bounds. Title/text are also truncated by the emitter; a
// request beyond these limits is refused rather than silently cut.
const (
	MaxAlertTitleRunes = maxTitleRunes
	MaxAlertTextRunes  = maxTextRunes
	MaxAlertRecipients = 100
	maxAlertURLBytes   = 2048
)

// Alert errors (the handler maps them onto 400 / 409).
var (
	ErrAlertInvalid     = errors.New("invalid alert")
	ErrAlertNoNotifier  = errors.New("nothing reaches the alert's recipients (no notifier component, notification plugin or browser push subscription)")
	ErrAlertNoRecipient = errors.New("alert has no recipients")
)

// Alert is one operational alert to platform admins.
type Alert struct {
	Title      string
	Text       string
	URL        string
	Recipients []string
	// Actor is the sender (a super-admin or a service subject).
	Actor string
	// DedupKey makes the event id unique per alert (the caller generates it).
	DedupKey string
}

// ValidateAlert checks an alert request and returns the cleaned recipients.
func ValidateAlert(a Alert) (Alert, error) {
	a.Title = strings.TrimSpace(a.Title)
	a.Text = strings.TrimSpace(a.Text)
	a.URL = strings.TrimSpace(a.URL)
	switch {
	case a.Title == "":
		return a, errors.Join(ErrAlertInvalid, errors.New("title required"))
	case utf8.RuneCountInString(a.Title) > MaxAlertTitleRunes:
		return a, errors.Join(ErrAlertInvalid, errors.New("title longer than 200 characters"))
	case utf8.RuneCountInString(a.Text) > MaxAlertTextRunes:
		return a, errors.Join(ErrAlertInvalid, errors.New("text longer than 500 characters"))
	case len(a.URL) > maxAlertURLBytes:
		return a, errors.Join(ErrAlertInvalid, errors.New("url too long"))
	}
	if a.URL != "" {
		u, err := url.Parse(a.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return a, errors.Join(ErrAlertInvalid, errors.New("url must be an absolute http(s) URL"))
		}
	}
	seen := map[string]bool{}
	var rs []string
	for _, r := range a.Recipients {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		if len(r) > 255 {
			return a, errors.Join(ErrAlertInvalid, errors.New("recipient id longer than 255 bytes"))
		}
		seen[r] = true
		rs = append(rs, r)
	}
	if len(rs) > MaxAlertRecipients {
		return a, errors.Join(ErrAlertInvalid, errors.New("more than 100 recipients"))
	}
	if len(rs) == 0 {
		return a, ErrAlertNoRecipient
	}
	a.Recipients = rs
	return a, nil
}

// SendAlert validates a and queues it as an admin_alert for the context
// tenant through the one emitter path (Emit): every notifier component that
// reaches at least one of its recipients (tenant scope and rollout apply as
// for every notification; an opt-in notifier reaches only recipients who
// opted in), the desktop channel while a notification plugin is distributed
// to the tenant, and browser push. Delivery is the outbox worker's job. The
// response lists those channels; ErrAlertNoNotifier when none would carry it.
func (e *Emitter) SendAlert(ctx context.Context, a Alert) (wire.AdminAlertResponse, error) {
	a, err := ValidateAlert(a)
	if err != nil {
		return wire.AdminAlertResponse{}, err
	}
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.AdminAlertResponse{}, err
	}
	targets, err := e.Notifiers.Targets(ctx, tid)
	if err != nil {
		return wire.AdminAlertResponse{}, err
	}
	var notifiers []string
	for _, t := range targets {
		if !t.OptIn || len(e.optedIn(ctx, t.Name, a.Recipients)) > 0 {
			notifiers = append(notifiers, t.Name)
		}
	}
	if e.desktopRoutable(ctx, tid) {
		notifiers = append(notifiers, DesktopChannel)
	}
	if e.Push != nil && len(pushSubscribed(ctx, e.Push, a.Recipients)) > 0 {
		notifiers = append(notifiers, WebPushChannel)
	}
	if len(notifiers) == 0 {
		return wire.AdminAlertResponse{}, ErrAlertNoNotifier
	}
	ev := Event{
		Type: wire.NotificationAdminAlert, Recipients: a.Recipients, Actor: a.Actor,
		Title: a.Title, Text: a.Text, URL: a.URL, DedupKey: "alert:" + a.DedupKey,
	}
	if _, err := e.Emit(ctx, ev); err != nil {
		return wire.AdminAlertResponse{}, err
	}
	return wire.AdminAlertResponse{
		ID: EventID(tid, ev.Type, ev.DedupKey), Recipients: a.Recipients, Notifiers: notifiers,
	}, nil
}
