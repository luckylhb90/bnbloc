package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// Percentiles summarizes a latency sample in milliseconds. Percentiles use
// the nearest-rank method: p is the smallest sample with at least p% of the
// samples at or below it, so p100 is the max and every value is a real
// observation (never interpolated).
type Percentiles struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
}

// summarize computes Percentiles over samples. It sorts samples in place.
func summarize(samples []time.Duration) Percentiles {
	n := len(samples)
	if n == 0 {
		return Percentiles{}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	var sum time.Duration
	for _, s := range samples {
		sum += s
	}
	return Percentiles{
		Count: n,
		Mean:  ms(sum / time.Duration(n)),
		P50:   ms(nearestRank(samples, 50)),
		P95:   ms(nearestRank(samples, 95)),
		P99:   ms(nearestRank(samples, 99)),
		Max:   ms(samples[n-1]),
	}
}

// nearestRank returns the p-th percentile of an ascending, non-empty sample.
func nearestRank(sorted []time.Duration, p float64) time.Duration {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// ms converts to milliseconds rounded to 0.01 ms.
func ms(d time.Duration) float64 {
	return math.Round(float64(d)/float64(time.Millisecond)*100) / 100
}

// Report is one run's result; --json prints it verbatim.
type Report struct {
	Gateway string        `json:"gateway"`
	Setup   SetupReport   `json:"setup"`
	Sends   SendReport    `json:"sends"`
	Deliver DeliverReport `json:"delivery"`
	Streams StreamReport  `json:"streams"`
	// Activity covers the lanes streams' channel_activity (--channels > 1).
	Activity ActivityReport `json:"activity"`
	// MemberActivity covers the members' activity streams (--member-activity).
	MemberActivity ActivityStreamReport `json:"memberActivity"`
	// Interrupted is true when SIGINT ended the send window early.
	Interrupted bool     `json:"interrupted"`
	Notes       []string `json:"notes,omitempty"`
}

// SetupReport describes the simulated population.
type SetupReport struct {
	Members        int     `json:"members"`
	Rooms          int     `json:"rooms"`
	MembersPerRoom int     `json:"membersPerRoom"`
	Channels       int     `json:"channelsPerRoom"`
	Streams        int     `json:"streams"`
	RoomsCreated   bool    `json:"roomsCreated"`
	TargetRate     float64 `json:"targetRatePerSec"`
	// PerMemberRate is the target rate each member sends at (the gateway's
	// chat limit is per subject).
	PerMemberRate float64 `json:"perMemberRatePerSec"`
	DurationSec   float64 `json:"durationSec"`
	RampUpSec     float64 `json:"rampUpSec"`
	TextBytes     int     `json:"textBytes"`
	// SignInSec is how long signing every member in took.
	SignInSec float64 `json:"signInSec"`
}

// SendReport covers POST .../chat.
type SendReport struct {
	Attempted int `json:"attempted"`
	OK        int `json:"ok"`
	// RateLimited counts 429 answers (the gateway's per-subject chat limit).
	RateLimited int `json:"rateLimited"`
	// Errors counts every non-200 answer by status, and transport failures
	// by "error: <kind>". 429s appear here too.
	Errors map[string]int `json:"errors,omitempty"`
	// Skipped sends were never issued because --max-inflight sends were
	// already outstanding: the load generator (or the server) fell behind.
	Skipped int `json:"skipped"`
	// WindowSec is the measured send window.
	WindowSec float64 `json:"windowSec"`
	// AchievedRate is OK sends per second over the window.
	AchievedRate float64 `json:"achievedRatePerSec"`
	// LatencyMs is the POST round trip of successful sends.
	LatencyMs Percentiles `json:"latencyMs"`
}

// DeliverReport covers chat_message arrival on the room streams.
type DeliverReport struct {
	// Expected is the sum over successful sends of the streams in the room
	// that were open when the send started.
	Expected int `json:"expected"`
	// Delivered is how many of those arrived (first arrival per stream).
	Delivered int `json:"delivered"`
	// Lost is Expected - Delivered after the drain wait.
	Lost int `json:"lost"`
	// Extra counts matching arrivals that were not expected: a stream that
	// opened after the send (replay buffer) or a repeat after a reconnect.
	Extra int `json:"extra"`
	// OutOfOrder counts events whose sequence was lower than one already
	// seen on the same connection.
	OutOfOrder int `json:"outOfOrder"`
	// PerSecond is Delivered divided by the send window.
	PerSecond float64 `json:"perSec"`
	// LatencyMs is send start to arrival on the receiving stream.
	LatencyMs Percentiles `json:"latencyMs"`
}

// ActivityReport covers channel_activity on the lanes streams: what moves
// a member's badges for the channels they do not have open.
type ActivityReport struct {
	// Events counts channel_activity events received.
	Events int `json:"events"`
	// Streams is how many streams had another channel with messages;
	// Complete how many of them heard of every such channel.
	Streams  int `json:"streams"`
	Complete int `json:"complete"`
}

// StreamReport covers the room SSE streams.
type StreamReport struct {
	// Opened counts successful opens (first opens plus reconnects).
	Opened int `json:"opened"`
	// Disconnects counts open streams that ended while the run was live.
	Disconnects int `json:"disconnects"`
	// Reconnects counts opens of a stream that had been open before.
	Reconnects int `json:"reconnects"`
	// ConnectErrors counts failed opens by status / transport error.
	ConnectErrors map[string]int `json:"connectErrors,omitempty"`
	// OpenAtStart is how many streams were open when sending began.
	OpenAtStart int `json:"openAtStart"`
	// OpenAtEnd is how many streams were open when the run ended.
	OpenAtEnd int `json:"openAtEnd"`
	// ConnectMs is request start to response headers for successful opens.
	ConnectMs Percentiles `json:"connectMs"`
	// Gaps counts stream_gap events: a stream told it lacked events, which
	// then read them from history (its deliveries count from there).
	Gaps int `json:"gaps"`
}

func (r *Report) writeJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func (r *Report) writeText(w io.Writer) {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	s := r.Setup
	p("bnbloc load test against %s", r.Gateway)
	if r.Interrupted {
		p("(interrupted: the send window ended early)")
	}
	p("")
	p("setup")
	p("  members %d, rooms %d (%s), %d members/room, %d channels/room, %d streams", s.Members, s.Rooms, map[bool]string{true: "created", false: "existing"}[s.RoomsCreated], s.MembersPerRoom, s.Channels, s.Streams)
	p("  target %.1f msgs/s (%.3f per member), duration %.0fs, ramp-up %.0fs, %d-byte text", s.TargetRate, s.PerMemberRate, s.DurationSec, s.RampUpSec, s.TextBytes)
	p("  sign-in took %.1fs", s.SignInSec)
	p("")
	p("sends")
	p("  attempted %d, ok %d, rate-limited (429) %d, skipped (max in-flight) %d", r.Sends.Attempted, r.Sends.OK, r.Sends.RateLimited, r.Sends.Skipped)
	p("  achieved %.1f msgs/s over %.1fs", r.Sends.AchievedRate, r.Sends.WindowSec)
	if len(r.Sends.Errors) > 0 {
		p("  errors: %s", formatCounts(r.Sends.Errors))
	}
	p("  POST latency ms   %s", formatPercentiles(r.Sends.LatencyMs))
	p("")
	p("delivery (send -> chat_message on each receiving stream)")
	p("  expected %d, delivered %d, lost %d, extra %d, out-of-order %d", r.Deliver.Expected, r.Deliver.Delivered, r.Deliver.Lost, r.Deliver.Extra, r.Deliver.OutOfOrder)
	p("  %.1f deliveries/s", r.Deliver.PerSecond)
	p("  latency ms        %s", formatPercentiles(r.Deliver.LatencyMs))
	p("")
	p("streams")
	p("  opened %d, disconnects %d, reconnects %d, gaps %d, open at start %d, open at end %d", r.Streams.Opened, r.Streams.Disconnects, r.Streams.Reconnects, r.Streams.Gaps, r.Streams.OpenAtStart, r.Streams.OpenAtEnd)
	if len(r.Streams.ConnectErrors) > 0 {
		p("  connect errors: %s", formatCounts(r.Streams.ConnectErrors))
	}
	p("  connect ms        %s", formatPercentiles(r.Streams.ConnectMs))
	if r.Activity.Streams > 0 {
		p("activity (channels not open)")
		p("  events %d; streams that heard of every other channel %d of %d", r.Activity.Events, r.Activity.Complete, r.Activity.Streams)
	}
	if r.MemberActivity.Streams > 0 {
		p("member activity streams (unread badges)")
		p("  %d streams, open at end %d, disconnects %d; states %d, deltas %d; told of %d of %d new messages",
			r.MemberActivity.Streams, r.MemberActivity.OpenAtEnd, r.MemberActivity.Disconnects, r.MemberActivity.States,
			r.MemberActivity.Deltas, r.MemberActivity.Told, r.MemberActivity.Expected)
	}
	for _, n := range r.Notes {
		p("")
		p("note: %s", n)
	}
}

func formatPercentiles(pc Percentiles) string {
	if pc.Count == 0 {
		return "(no samples)"
	}
	return fmt.Sprintf("p50 %.1f  p95 %.1f  p99 %.1f  max %.1f  mean %.1f  (n=%d)", pc.P50, pc.P95, pc.P99, pc.Max, pc.Mean, pc.Count)
}

// formatCounts renders a count map as "k=v, ..." in key order.
func formatCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// notes derives the human-facing caveats of a finished report.
func (r *Report) notes() []string {
	var out []string
	if r.Sends.RateLimited > 0 {
		out = append(out, fmt.Sprintf("the gateway's per-subject chat limit bound: %d sends got 429 at %.3f msgs/s per member. Add members (or lower --rate) to measure delivery instead of the limiter.", r.Sends.RateLimited, r.Setup.PerMemberRate))
	}
	if r.Sends.Skipped > 0 {
		out = append(out, fmt.Sprintf("%d sends were skipped because --max-inflight sends were outstanding: POST latency exceeded the send interval, so the offered rate was not reached.", r.Sends.Skipped))
	}
	if r.Deliver.Lost > 0 {
		out = append(out, fmt.Sprintf("%d expected deliveries never arrived within the drain wait (see disconnects; a stream that reconnects catches up only through the replay buffer).", r.Deliver.Lost))
	}
	if r.Activity.Complete < r.Activity.Streams {
		out = append(out, fmt.Sprintf("only %d of %d streams heard of every other channel's messages (channel_activity): badges would be wrong.", r.Activity.Complete, r.Activity.Streams))
	}
	if r.Streams.OpenAtStart < r.Setup.Streams {
		out = append(out, fmt.Sprintf("only %d of %d streams were open when sending began.", r.Streams.OpenAtStart, r.Setup.Streams))
	}
	return out
}
