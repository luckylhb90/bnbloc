// Package schedule is the recurrence engine of scheduled prompts (N2):
// validation of a schedule's recurrence + timezone, and the DST-correct
// computation of its next run. It is pure (no I/O, no clock): realtime's
// scheduler and the room service's template instantiation both call it.
//
// Every recurrence kind except "once" compiles to one cron-like spec
// (minute/hour/day-of-month/month/day-of-week sets) evaluated on the
// schedule's LOCAL wall clock, so "daily at 09:00 Europe/Berlin" stays at
// 09:00 across DST changes. Local times that do not exist (the spring-forward
// gap) run at the first instant after the gap (02:30 -> 03:30); local times
// that exist twice (the fall-back overlap) run once, at the earlier instant.
package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MinInterval is the shortest spacing between two runs of one schedule. It is
// a product rule of the feature (a scheduled prompt is a reminder/report
// cadence, not a polling loop that could flood a room or burn an agent's
// quota), not deployment data: it is enforced statically on the recurrence
// and again at run time (NextRun) so a DST transition can never compress two
// runs closer than this.
const MinInterval = 5 * time.Minute

// maxSearchDays bounds the next-run search. Eight years covers the sparsest
// valid expression (February 29th on a given weekday recurs within 28 years,
// but February 29th alone recurs within 8).
const maxSearchDays = 8*366 + 1

// spec is a compiled recurrence: the sets of local wall-clock fields that
// fire. domStar/dowStar record an unrestricted field for cron's day rule.
type spec struct {
	minutes [60]bool
	hours   [24]bool
	dom     [32]bool
	months  [13]bool
	dow     [7]bool
	domStar bool
	dowStar bool
}

// Validate checks a recurrence and timezone, returning the normalized
// recurrence (sorted unique weekdays, trimmed fields, irrelevant fields
// cleared) and the loaded location. A one-shot time is not checked against
// the clock here; callers check Next(…, now) when they enable a schedule.
func Validate(rec wire.ScheduleRecurrence, tz string) (wire.ScheduleRecurrence, *time.Location, error) {
	loc, err := LoadZone(tz)
	if err != nil {
		return rec, nil, err
	}
	out := wire.ScheduleRecurrence{Kind: strings.TrimSpace(rec.Kind)}
	switch out.Kind {
	case wire.RecurOnce:
		if rec.AtMs <= 0 {
			return rec, nil, errors.New("recurrence: a one-time schedule needs atMs")
		}
		out.AtMs = rec.AtMs
		return out, loc, nil
	case wire.RecurDaily, wire.RecurWeekly:
		if _, _, err := parseHHMM(rec.Time); err != nil {
			return rec, nil, err
		}
		out.Time = strings.TrimSpace(rec.Time)
		if out.Kind == wire.RecurWeekly {
			days, err := normalizeDays(rec.Days)
			if err != nil {
				return rec, nil, err
			}
			out.Days = days
		}
	case wire.RecurCron:
		out.Cron = strings.Join(strings.Fields(rec.Cron), " ")
	default:
		return rec, nil, fmt.Errorf("recurrence: unknown kind %q (once, daily, weekly, cron)", rec.Kind)
	}
	sp, err := compile(out)
	if err != nil {
		return rec, nil, err
	}
	if err := sp.checkMinInterval(); err != nil {
		return rec, nil, err
	}
	return out, loc, nil
}

// NewID mints a schedule id.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "sch_" + hex.EncodeToString(b)
}

// NormalizeRecipient validates a schedule recipient: "room" (or empty) is a
// people-only message; "agent" needs the agent's owner and provider.
func NormalizeRecipient(r wire.ScheduleRecipient) (wire.ScheduleRecipient, error) {
	switch strings.TrimSpace(r.Kind) {
	case "", wire.ScheduleRecipientRoom:
		return wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom}, nil
	case wire.ScheduleRecipientAgent:
		out := wire.ScheduleRecipient{
			Kind:     wire.ScheduleRecipientAgent,
			OwnerID:  strings.TrimSpace(r.OwnerID),
			Provider: strings.TrimSpace(r.Provider),
			Core:     strings.TrimSpace(r.Core),
		}
		if out.OwnerID == "" || out.Provider == "" {
			return out, errors.New("recipient: an agent recipient needs ownerId and provider")
		}
		return out, nil
	}
	return r, fmt.Errorf("recipient: unknown kind %q (room, agent)", r.Kind)
}

// LoadZone loads an IANA timezone. The process-local zone is refused: a
// schedule must mean the same instant on every replica.
func LoadZone(tz string) (*time.Location, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" || strings.EqualFold(tz, "Local") {
		return nil, errors.New("timezone: an IANA zone name is required (e.g. Europe/Berlin, UTC)")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("timezone: unknown zone %q", tz)
	}
	return loc, nil
}

// Next returns the first run of rec strictly after after, in loc. false when
// the recurrence never fires again (a one-shot in the past, or a cron day
// set that does not occur within the search horizon). rec must be valid.
func Next(rec wire.ScheduleRecurrence, loc *time.Location, after time.Time) (time.Time, bool) {
	if rec.Kind == wire.RecurOnce {
		at := time.UnixMilli(rec.AtMs)
		if at.After(after) {
			return at, true
		}
		return time.Time{}, false
	}
	sp, err := compile(rec)
	if err != nil {
		return time.Time{}, false
	}
	return sp.next(loc, after)
}

// NextRun returns the run following the slot that just fired (or was
// skipped). Missed slots collapse: the result is never before now, so a
// scheduler that was down posts once, not once per missed slot. It is never
// closer than MinInterval to prev (the runtime half of the spacing rule).
func NextRun(rec wire.ScheduleRecurrence, loc *time.Location, prev, now time.Time) (time.Time, bool) {
	after := prev.Add(MinInterval - time.Millisecond)
	if now.After(after) {
		after = now
	}
	return Next(rec, loc, after)
}

// RunSlotClientMsgID is the chat idempotency id of one scheduled run: the
// schedule id plus the slot instant. A run retried after a crash (its claim
// lease expired and another tick re-claimed the slot) posts with the same id,
// so realtime's clientMsgId dedupe answers with the original message instead
// of a duplicate.
func RunSlotClientMsgID(scheduleID string, slot time.Time) string {
	return "sched:" + scheduleID + ":" + strconv.FormatInt(slot.UnixMilli(), 10)
}

// compile turns a validated, normalized recurrence into a spec.
func compile(rec wire.ScheduleRecurrence) (*spec, error) {
	sp := &spec{}
	switch rec.Kind {
	case wire.RecurDaily, wire.RecurWeekly:
		h, m, err := parseHHMM(rec.Time)
		if err != nil {
			return nil, err
		}
		sp.minutes[m], sp.hours[h] = true, true
		fill(sp.dom[1:])
		fill(sp.months[1:])
		sp.domStar = true
		if rec.Kind == wire.RecurDaily {
			fill(sp.dow[:])
			sp.dowStar = true
		} else {
			if len(rec.Days) == 0 {
				return nil, errors.New("recurrence: a weekly schedule needs at least one day")
			}
			for _, d := range rec.Days {
				if d < 0 || d > 6 {
					return nil, fmt.Errorf("recurrence: weekday %d out of range 0-6", d)
				}
				sp.dow[d] = true
			}
		}
		return sp, nil
	case wire.RecurCron:
		f := strings.Fields(rec.Cron)
		if len(f) != 5 {
			return nil, errors.New("recurrence: cron needs 5 fields: minute hour day-of-month month day-of-week")
		}
		var err error
		if _, err = parseField(f[0], 0, 59, sp.minutes[:]); err != nil {
			return nil, fmt.Errorf("cron minute: %w", err)
		}
		if _, err = parseField(f[1], 0, 23, sp.hours[:]); err != nil {
			return nil, fmt.Errorf("cron hour: %w", err)
		}
		if sp.domStar, err = parseField(f[2], 1, 31, sp.dom[:]); err != nil {
			return nil, fmt.Errorf("cron day-of-month: %w", err)
		}
		if _, err = parseField(f[3], 1, 12, sp.months[:]); err != nil {
			return nil, fmt.Errorf("cron month: %w", err)
		}
		// Day-of-week accepts 0-7 (both 0 and 7 are Sunday).
		var dow [8]bool
		if sp.dowStar, err = parseField(f[4], 0, 7, dow[:]); err != nil {
			return nil, fmt.Errorf("cron day-of-week: %w", err)
		}
		copy(sp.dow[:], dow[:7])
		if dow[7] {
			sp.dow[0] = true
		}
		return sp, nil
	}
	return nil, fmt.Errorf("recurrence: kind %q has no time spec", rec.Kind)
}

func fill(b []bool) {
	for i := range b {
		b[i] = true
	}
}

// parseField parses one cron-lite field into set (indexed by value): "*",
// "*/n", "a", "a-b", "a-b/n", and comma lists of those. star reports an
// unrestricted field ("*" or "*/1").
func parseField(field string, lo, hi int, set []bool) (star bool, err error) {
	if field == "" {
		return false, errors.New("empty field")
	}
	matched := false
	for _, part := range strings.Split(field, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			if step, err = strconv.Atoi(stepStr); err != nil || step < 1 {
				return false, fmt.Errorf("bad step %q", stepStr)
			}
		}
		from, to := lo, hi
		switch {
		case rng == "*":
			if step == 1 {
				star = true
			}
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			if from, err = strconv.Atoi(a); err != nil {
				return false, fmt.Errorf("bad value %q", a)
			}
			if to, err = strconv.Atoi(b); err != nil {
				return false, fmt.Errorf("bad value %q", b)
			}
		default:
			if from, err = strconv.Atoi(rng); err != nil {
				return false, fmt.Errorf("bad value %q", rng)
			}
			to = from
			if hasStep {
				return false, fmt.Errorf("step needs a range: %q", part)
			}
		}
		if from < lo || to > hi || from > to {
			return false, fmt.Errorf("%q out of range %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			set[v] = true
			matched = true
		}
	}
	if !matched {
		return false, errors.New("matches nothing")
	}
	return star, nil
}

func parseHHMM(s string) (h, m int, err error) {
	hs, ms, ok := strings.Cut(strings.TrimSpace(s), ":")
	if ok && len(hs) >= 1 && len(hs) <= 2 && len(ms) == 2 {
		h, herr := strconv.Atoi(hs)
		m, merr := strconv.Atoi(ms)
		if herr == nil && merr == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59 {
			return h, m, nil
		}
	}
	return 0, 0, fmt.Errorf("recurrence: time %q is not HH:MM (24h)", s)
}

func normalizeDays(days []int) ([]int, error) {
	seen := map[int]bool{}
	out := []int{}
	for _, d := range days {
		if d == 7 {
			d = 0
		}
		if d < 0 || d > 6 {
			return nil, fmt.Errorf("recurrence: weekday %d out of range 0-6 (0 = Sunday)", d)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("recurrence: a weekly schedule needs at least one day")
	}
	sort.Ints(out)
	return out, nil
}

// checkMinInterval enforces MinInterval on the wall-clock times of one day,
// treating the day as circular (the last run of a day and the first of the
// next): conservative for day sets that are never adjacent, exact otherwise.
func (sp *spec) checkMinInterval() error {
	var times []int
	for h := 0; h < 24; h++ {
		if !sp.hours[h] {
			continue
		}
		for m := 0; m < 60; m++ {
			if sp.minutes[m] {
				times = append(times, h*60+m)
			}
		}
	}
	gap := int(MinInterval / time.Minute)
	for i := 1; i < len(times); i++ {
		if times[i]-times[i-1] < gap {
			return fmt.Errorf("recurrence: runs must be at least %d minutes apart", gap)
		}
	}
	if len(times) > 1 && times[0]+24*60-times[len(times)-1] < gap {
		return fmt.Errorf("recurrence: runs must be at least %d minutes apart", gap)
	}
	return nil
}

// dayMatches applies cron's day rule: when both day-of-month and day-of-week
// are restricted, either matching fires; otherwise both must match (an
// unrestricted field always does).
func (sp *spec) dayMatches(d time.Time) bool {
	if !sp.months[int(d.Month())] {
		return false
	}
	domOK, dowOK := sp.dom[d.Day()], sp.dow[int(d.Weekday())]
	if !sp.domStar && !sp.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// next scans calendar days (pure date arithmetic in UTC, so DST never skips
// or repeats a date) and, on matching days, the wall-clock times in order.
func (sp *spec) next(loc *time.Location, after time.Time) (time.Time, bool) {
	la := after.In(loc)
	start := time.Date(la.Year(), la.Month(), la.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxSearchDays; i++ {
		day := start.AddDate(0, 0, i)
		if !sp.dayMatches(day) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !sp.hours[h] {
				continue
			}
			for m := 0; m < 60; m++ {
				if !sp.minutes[m] {
					continue
				}
				if c := LocalInstant(day.Year(), day.Month(), day.Day(), h, m, loc); c.After(after) {
					return c, true
				}
			}
		}
	}
	return time.Time{}, false
}

// LocalInstant resolves a local wall-clock time to an instant with explicit
// DST rules (time.Date leaves the choice unspecified): a time inside a
// spring-forward gap resolves to the first instant after the gap shifted by
// the same amount (02:30 -> 03:30), and a time inside a fall-back overlap
// resolves to the earlier of its two instants.
func LocalInstant(y int, mo time.Month, d, h, mi int, loc *time.Location) time.Time {
	wall := time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	probe := time.Date(y, mo, d, h, mi, 0, 0, loc)
	var valid, all []time.Time
	for _, p := range []time.Time{probe.Add(-12 * time.Hour), probe, probe.Add(12 * time.Hour)} {
		_, off := p.Zone()
		c := wall.Add(-time.Duration(off) * time.Second)
		all = append(all, c)
		lc := c.In(loc)
		if lc.Year() == y && lc.Month() == mo && lc.Day() == d && lc.Hour() == h && lc.Minute() == mi {
			valid = append(valid, c)
		}
	}
	pick := func(ts []time.Time, earliest bool) time.Time {
		best := ts[0]
		for _, t := range ts[1:] {
			if (earliest && t.Before(best)) || (!earliest && t.After(best)) {
				best = t
			}
		}
		return best
	}
	if len(valid) > 0 {
		return pick(valid, true)
	}
	return pick(all, false)
}
