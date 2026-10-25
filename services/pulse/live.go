package pulsesvc

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Live-state windows. A member counts as online for presenceWindow after their
// last presence.seen; a job counts as working for jobsWindow after its claim
// unless its finish arrives first. A queued job is waiting until it is claimed
// or swept, and the sweep takes queued jobs back after the store's queued
// timeout, so that timeout bounds the waiting set too. A missed leave therefore
// heals itself.
const (
	presenceWindow = 90 * time.Second
	jobsWindow     = 20 * time.Minute
	queuedWindow   = time.Duration(store.DefaultQueuedTimeoutMs) * time.Millisecond
	activeKeyTTL   = 8 * 24 * time.Hour
)

func presenceKey(tenant string) string { return pulse.KeyPrefix + "presence:" + tenant }
func jobsKey(tenant string) string     { return pulse.KeyPrefix + "jobs:" + tenant }
func queuedKey(tenant string) string   { return pulse.KeyPrefix + "queued:" + tenant }
func tenantsKey() string               { return pulse.KeyPrefix + "tenants" }

// geoKey holds the members whose presence is in a country, per tenant and country.
func geoKey(tenant, cc string) string { return pulse.KeyPrefix + "geo:" + tenant + ":" + cc }

// geoOfKey maps each member to the country their presence is in (one country at a time).
func geoOfKey(tenant string) string { return pulse.KeyPrefix + "geoof:" + tenant }

// geoCountriesKey lists the countries a tenant has had presence in.
func geoCountriesKey(tenant string) string { return pulse.KeyPrefix + "geocountries:" + tenant }
func activeKey(tenant string, at time.Time) string {
	return pulse.KeyPrefix + "active:" + tenant + ":" + at.UTC().Format("20060102")
}

// Live keeps the Redis state the board reads now: who is online, which agents
// are working, which jobs wait for a bridge, and who was active this week. Every
// write is idempotent (ZADD, ZREM, PFADD), so a redelivered event does no harm.
type Live struct {
	rdb redis.UniversalClient
}

// NewLive returns the live state over rdb.
func NewLive(rdb redis.UniversalClient) *Live { return &Live{rdb: rdb} }

// Apply queues one event's live effects on pipe.
func (l *Live) Apply(pipe redis.Pipeliner, ctx context.Context, ev pulse.Event) {
	at := time.UnixMilli(ev.At)
	pipe.SAdd(ctx, tenantsKey(), ev.Tenant)
	job, hasJob := ev.Get("job")
	switch ev.Kind {
	case pulse.KindPresenceSeen:
		if m, ok := ev.Get("member"); ok {
			pipe.ZAdd(ctx, presenceKey(ev.Tenant), redis.Z{Score: float64(ev.At), Member: m.Text()})
			l.geoSeen(pipe, ctx, ev, m.Text())
		}
	case pulse.KindPresenceLeft:
		if m, ok := ev.Get("member"); ok {
			pipe.ZRem(ctx, presenceKey(ev.Tenant), m.Text())
			l.geoLeave(pipe, ctx, ev, m.Text())
		}
	case pulse.KindJobQueued:
		if hasJob {
			pipe.ZAdd(ctx, queuedKey(ev.Tenant), redis.Z{Score: float64(ev.At), Member: job.Text()})
		}
	case pulse.KindJobClaimed:
		if hasJob {
			pipe.ZRem(ctx, queuedKey(ev.Tenant), job.Text())
			pipe.ZAdd(ctx, jobsKey(ev.Tenant), redis.Z{Score: float64(ev.At), Member: job.Text()})
		}
	case pulse.KindJobFinished, pulse.KindJobSwept:
		if hasJob {
			pipe.ZRem(ctx, queuedKey(ev.Tenant), job.Text())
			pipe.ZRem(ctx, jobsKey(ev.Tenant), job.Text())
		}
	}
	// A member who did something this week counts as active. Chat, deliverables
	// and app documents all name the member by digest; an agent's own work names
	// none, so it does not count as a person.
	switch ev.Kind {
	case pulse.KindMessageSent, pulse.KindArtifactVersion, pulse.KindAppWritten:
		if m, ok := ev.Get("member"); ok {
			k := activeKey(ev.Tenant, at)
			pipe.PFAdd(ctx, k, m.Text())
			pipe.Expire(ctx, k, activeKeyTTL)
		}
	}
}

// PeopleOnline counts members whose presence is fresh at now.
func (l *Live) PeopleOnline(ctx context.Context, tenant string, now time.Time) (int64, error) {
	return l.rdb.ZCount(ctx, presenceKey(tenant), scoreFrom(now.Add(-presenceWindow)), "+inf").Result()
}

// AgentsWorking counts jobs claimed and not yet finished, within jobsWindow.
func (l *Live) AgentsWorking(ctx context.Context, tenant string, now time.Time) (int64, error) {
	return l.rdb.ZCount(ctx, jobsKey(tenant), scoreFrom(now.Add(-jobsWindow)), "+inf").Result()
}

// AgentsWaiting counts jobs queued for a bridge and not yet claimed, within the
// queued window.
func (l *Live) AgentsWaiting(ctx context.Context, tenant string, now time.Time) (int64, error) {
	return l.rdb.ZCount(ctx, queuedKey(tenant), scoreFrom(now.Add(-queuedWindow)), "+inf").Result()
}

// OldestWaiting returns how long the oldest waiting job has waited, and whether
// there is one.
func (l *Live) OldestWaiting(ctx context.Context, tenant string, now time.Time) (time.Duration, bool, error) {
	zs, err := l.rdb.ZRangeWithScores(ctx, queuedKey(tenant), 0, 0).Result()
	if err != nil || len(zs) == 0 {
		return 0, false, err
	}
	since := time.UnixMilli(int64(zs[0].Score))
	return now.Sub(since), true, nil
}

// ActivePeopleWeek counts distinct members active on any of the last seven UTC
// days (HyperLogLog: approximate, and it holds no member ids).
func (l *Live) ActivePeopleWeek(ctx context.Context, tenant string, now time.Time) (int64, error) {
	keys := make([]string, 0, 7)
	for d := 0; d < 7; d++ {
		keys = append(keys, activeKey(tenant, now.AddDate(0, 0, -d)))
	}
	return l.rdb.PFCount(ctx, keys...).Result()
}

// Prune drops entries that aged out of their windows, for every tenant seen.
func (l *Live) Prune(ctx context.Context, now time.Time) error {
	tenants, err := l.rdb.SMembers(ctx, tenantsKey()).Result()
	if err != nil {
		return err
	}
	pipe := l.rdb.Pipeline()
	for _, t := range tenants {
		pipe.ZRemRangeByScore(ctx, presenceKey(t), "-inf", scoreFrom(now.Add(-presenceWindow)))
		pipe.ZRemRangeByScore(ctx, jobsKey(t), "-inf", scoreFrom(now.Add(-jobsWindow)))
		pipe.ZRemRangeByScore(ctx, queuedKey(t), "-inf", scoreFrom(now.Add(-queuedWindow)))
		if cc, err := l.rdb.SMembers(ctx, geoCountriesKey(t)).Result(); err == nil {
			for _, c := range cc {
				pipe.ZRemRangeByScore(ctx, geoKey(t, c), "-inf", scoreFrom(now.Add(-presenceWindow)))
			}
		}
	}
	_, err = pipe.Exec(ctx)
	return err
}

// scoreFrom is the lower bound of a window, in the same unit as event times.
func scoreFrom(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}

// geoSeen places a member in the country of their zone, moving them out of the
// country they were in before. A zone the table does not know (or a switch that
// is off, which sends "other") leaves the member in no country.
func (l *Live) geoSeen(pipe redis.Pipeliner, ctx context.Context, ev pulse.Event, member string) {
	cc := countryOf(ev)
	_, _, known := pulse.CountryCentreOf(cc)
	prev := l.prevCountry(ctx, ev.Tenant, member)
	if prev != "" && (!known || prev != cc) {
		pipe.ZRem(ctx, geoKey(ev.Tenant, prev), member)
	}
	if !known {
		pipe.HDel(ctx, geoOfKey(ev.Tenant), member)
		return
	}
	pipe.ZAdd(ctx, geoKey(ev.Tenant, cc), redis.Z{Score: float64(ev.At), Member: member})
	pipe.HSet(ctx, geoOfKey(ev.Tenant), member, cc)
	pipe.SAdd(ctx, geoCountriesKey(ev.Tenant), cc)
}

func (l *Live) geoLeave(pipe redis.Pipeliner, ctx context.Context, ev pulse.Event, member string) {
	prev := l.prevCountry(ctx, ev.Tenant, member)
	if prev != "" {
		pipe.ZRem(ctx, geoKey(ev.Tenant, prev), member)
	}
	pipe.HDel(ctx, geoOfKey(ev.Tenant), member)
}

// countryOf is the event's country code, upper case; "" when the event names none.
func countryOf(ev pulse.Event) string {
	if a, ok := ev.Get("country"); ok {
		return strings.ToUpper(a.Text())
	}
	return ""
}

// prevCountry reads the country a member is recorded in; "" when none.
func (l *Live) prevCountry(ctx context.Context, tenant, member string) string {
	cc, err := l.rdb.HGet(ctx, geoOfKey(tenant), member).Result()
	if err != nil {
		return ""
	}
	return cc
}

// CountryPeople is one country's people online right now, with its placement.
type CountryPeople struct {
	Code   string
	Lat    float64
	Lon    float64
	People int64
}

// Countries counts the members online in each country at now.
func (l *Live) Countries(ctx context.Context, tenant string, now time.Time) ([]CountryPeople, error) {
	codes, err := l.rdb.SMembers(ctx, geoCountriesKey(tenant)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]CountryPeople, 0, len(codes))
	for _, cc := range codes {
		n, err := l.rdb.ZCount(ctx, geoKey(tenant, cc), scoreFrom(now.Add(-presenceWindow)), "+inf").Result()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		lat, lon, ok := pulse.CountryCentreOf(cc)
		if !ok {
			continue
		}
		out = append(out, CountryPeople{Code: cc, Lat: lat, Lon: lon, People: n})
	}
	return out, nil
}
