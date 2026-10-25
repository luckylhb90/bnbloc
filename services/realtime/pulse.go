package realtimehttp

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// pulsePresenceEvery is how often an open presence stream refreshes its
// presence.seen. The consumer expires a member whose last refresh is older than
// its TTL, so a long-open stream must keep refreshing (docs/prd/
// 2026-10-24-executive-pulse-backend.md, sections 2 and 5).
var pulsePresenceEvery = 30 * time.Second

// pulseMessageSent: a room message was durably stored. channelType is "agent"
// when the message went to an agent (a job was queued for it), else "room".
// The sender is named by member digest, so active people can be counted without
// a raw id; with no key, or no author, no member is named.
func (s *Service) pulseMessageSent(ctx context.Context, roomID string, seq int64, agent bool, author string) {
	channelType := "room"
	if agent {
		channelType = "agent"
	}
	ev := pulse.Event{
		ID:     "msg:" + roomID + ":" + strconv.FormatInt(seq, 10),
		Tenant: tenant.FromContext(ctx),
		Kind:   pulse.KindMessageSent,
	}.With(pulse.Enum("channelType", channelType))
	if member, err := pulse.MemberHash(s.PulseKey, author); err == nil && author != "" {
		ev = ev.With(pulse.Hash("member", member))
	}
	s.Pulse.Emit(ev)
}

// pulsePresenceStart reports one human presence stream: presence.seen now and
// every pulsePresenceEvery while it stays open. The returned stop emits
// presence.left. A person is never sent as a raw id.
//
// The zone is the client's time zone only when the organization has member
// locations on (its pulse_geography setting, read through PulseGeo) and the zone
// is one the table knows; otherwise it is "other", and no zone is stored. The
// setting is read on every refresh, so turning it off takes effect within a few
// seconds.
func (s *Service) pulsePresenceStart(ctx context.Context, roomID, userID, tz string) (stop func()) {
	if s.Pulse == nil {
		return func() {}
	}
	member, err := pulse.MemberHash(s.PulseKey, userID)
	if err != nil {
		return func() {}
	}
	tnt := tenant.FromContext(ctx)
	var mu sync.Mutex
	country := ""
	// countryFor turns the client's zone into a country code: the zone itself never
	// leaves this process. "" when the switch is off or the zone is unknown.
	countryFor := func() string {
		if tz == "" || !s.PulseGeo.On(ctx, tnt) {
			return ""
		}
		cc, _, _, ok := pulse.ZoneCountry(tz)
		if !ok {
			return ""
		}
		return strings.ToLower(cc)
	}
	emit := func(kind, id string, cc string) {
		ev := pulse.Event{ID: id, Tenant: tnt, Kind: kind}.With(pulse.Hash("member", member))
		if cc != "" {
			ev = ev.With(pulse.Ident("country", cc))
		}
		s.Pulse.Emit(ev)
	}
	seen := func() {
		cc := countryFor()
		mu.Lock()
		country = cc
		mu.Unlock()
		emit(pulse.KindPresenceSeen, "presence:"+member+":"+strconv.FormatInt(time.Now().UnixNano(), 10), cc)
	}
	// The first seen is synchronous, so a stream that closes at once still orders
	// seen before left. stop waits for the refresher to exit before it emits left,
	// so no refresh can land after the member has left.
	seen()
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(pulsePresenceEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				seen()
			}
		}
	}()
	return func() {
		close(done)
		<-exited
		mu.Lock()
		cc := country
		mu.Unlock()
		emit(pulse.KindPresenceLeft, "left:"+member+":"+strconv.FormatInt(time.Now().UnixNano(), 10), cc)
	}
}

// pulseJobQueued: a chat message queued an agent job. The job waits here until a
// bridge claims it (job.claimed), finishes it, or the sweep takes it back.
func (s *Service) pulseJobQueued(job store.AgentJob) {
	s.Pulse.Emit(pulse.Event{
		ID:     "job:" + job.JobID + ":queued",
		Tenant: job.TenantID,
		Kind:   pulse.KindJobQueued,
	}.With(pulse.Enum("core", pulse.CoreFamily(job.Provider))).With(pulse.Hash("job", pulse.Digest(job.JobID))))
}
