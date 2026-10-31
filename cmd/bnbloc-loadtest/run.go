package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// room is one room under load and the streams its members hold in it.
type room struct {
	id string
	// channels are the room's channels in the run, the default first.
	channels []string
	members  []*member
	streams  []*stream
	created  bool
	// stored counts the sends stored in the room.
	stored atomic.Int64
}

// liveStreams returns the ids of the room's currently open streams that
// have channelID open (the ones a message there reaches in full).
func (r *room) liveStreams(channelID string) []int {
	out := make([]int, 0, len(r.streams))
	for _, s := range r.streams {
		if s.live.Load() && s.channel == channelID {
			out = append(out, s.id)
		}
	}
	return out
}

// hasPoster reports whether a member of the room posts in channelID (one of
// its streams has it open).
func (r *room) hasPoster(channelID string) bool {
	for _, s := range r.streams {
		if s.channel == channelID {
			return true
		}
	}
	return false
}

// sendStats aggregates POST .../chat outcomes.
type sendStats struct {
	mu          sync.Mutex
	attempted   int
	ok          int
	rateLimited int
	skipped     int
	errors      map[string]int
	latencies   []time.Duration
}

// run executes one load test. It returns a report whenever sending started
// (also when interrupted), and an error when setup failed.
func run(ctx context.Context, cfg config, logw io.Writer) (*Report, error) {
	logf := func(format string, a ...any) { fmt.Fprintf(logw, format+"\n", a...) }
	runID, err := randomHex(3)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 0
	transport.MaxIdleConnsPerHost = cfg.MaxInflight + cfg.LoginParallel
	transport.DisableCompression = true
	a := &api{base: cfg.Gateway, http: &http.Client{Transport: transport}, timeout: cfg.Timeout}
	defer transport.CloseIdleConnections()

	signInStart := time.Now()
	members, err := signIn(ctx, cfg, a, logf)
	if err != nil {
		return nil, err
	}
	signInTook := time.Since(signInStart)
	if cfg.TokensFile != "" {
		logf("read %d member tokens from %s", len(members), cfg.TokensFile)
	} else {
		logf("signed in %d members in %.1fs", len(members), signInTook.Seconds())
	}

	rooms, perRoom := assignRooms(cfg, members, runID)
	// Rooms the tool created are deleted even when setup fails half-way or
	// the run is interrupted (cleanup uses its own context).
	if len(cfg.RoomIDs) == 0 {
		if cfg.Cleanup {
			defer cleanupRooms(a, rooms, cfg.LoginParallel, logf)
		} else {
			defer logKeptRooms(rooms, logf)
		}
	}
	if err := setupRooms(ctx, cfg, a, rooms); err != nil {
		return nil, err
	}
	logf("rooms ready: %d rooms x %d members", len(rooms), perRoom)

	tr := newTracker()
	ss := newStreamStats()
	astats := newActivityStats()
	// Every tab of a member carries the unread of all the member's rooms.
	memberRooms := map[*member][]string{}
	for _, r := range rooms {
		for _, m := range r.members {
			memberRooms[m] = append(memberRooms[m], r.id)
		}
	}
	var streams []*stream
	for _, r := range rooms {
		for k, m := range r.members {
			st := &stream{id: len(streams), room: r, channel: r.channels[k%len(r.channels)], member: m, api: a, tracker: tr, stats: ss,
				rooms: memberRooms[m], unread: astats, idle: cfg.StreamIdle, tz: tzFor(cfg.TZ, len(streams))}
			r.streams = append(r.streams, st)
			streams = append(streams, st)
		}
	}
	report := &Report{Gateway: cfg.Gateway, Setup: SetupReport{
		Members: len(members), Rooms: len(rooms), MembersPerRoom: perRoom, Channels: cfg.Channels, Streams: len(streams),
		RoomsCreated: len(cfg.RoomIDs) == 0, TargetRate: cfg.Rate, PerMemberRate: perMemberRate(cfg.Rate, rooms),
		DurationSec: cfg.Duration.Seconds(), RampUpSec: cfg.RampUp.Seconds(), TextBytes: cfg.TextBytes,
		SignInSec: signInTook.Seconds(),
	}}
	var notes []string
	if exp := earliestExpiry(members); !exp.IsZero() && exp.Before(time.Now().Add(cfg.RampUp+cfg.Settle+cfg.Duration+cfg.Drain)) {
		notes = append(notes, fmt.Sprintf("a session token expires at %s, before the run ends; requests after that fail with 401.", exp.Format(time.RFC3339)))
		logf("warning: %s", notes[len(notes)-1])
	}

	// Streams open evenly over the ramp-up and live until the drain ends.
	streamCtx, stopStreams := context.WithCancel(ctx)
	var streamWG sync.WaitGroup
	for i, st := range streams {
		delay := time.Duration(0)
		if len(streams) > 1 {
			delay = cfg.RampUp * time.Duration(i) / time.Duration(len(streams))
		}
		streamWG.Add(1)
		go func() {
			defer streamWG.Done()
			select {
			case <-streamCtx.Done():
				return
			case <-time.After(delay):
			}
			st.run(streamCtx)
		}()
	}
	stopAll := func() {
		stopStreams()
		streamWG.Wait()
	}
	logf("opening %d streams over %s", len(streams), cfg.RampUp)
	if !sleepCtx(ctx, cfg.RampUp) || !waitAllLive(ctx, streams, cfg.Settle) {
		stopAll()
		report.Interrupted = true
		report.Notes = notes
		return report, nil
	}
	report.Streams.OpenAtStart = countLive(streams)
	logf("%d/%d streams open; sending %.1f msgs/s for %s", report.Streams.OpenAtStart, len(streams), cfg.Rate, cfg.Duration)

	sends := &sendStats{errors: map[string]int{}}
	stopProgress := startProgress(cfg.Progress, logf, sends, tr, streams)
	window, interrupted := sendLoop(ctx, cfg, a, rooms, runID, tr, sends)
	report.Interrupted = interrupted
	if !interrupted {
		drain(ctx, tr, cfg.Drain)
	}
	stopProgress()
	report.Streams.OpenAtEnd = countLive(streams)
	// The streams send what changed in their unread once a second: wait
	// out the last one.
	sleepCtx(ctx, activityLastFlush)
	report.Unread = unreadReport(streams, astats, rooms)
	stopAll()

	res := tr.result()
	report.Sends = SendReport{
		Attempted: sends.attempted, OK: sends.ok, RateLimited: sends.rateLimited, Skipped: sends.skipped,
		Errors: sends.errors, WindowSec: window.Seconds(), LatencyMs: summarize(sends.latencies),
	}
	if window > 0 {
		report.Sends.AchievedRate = float64(sends.ok) / window.Seconds()
	}
	report.Deliver = DeliverReport{
		Expected: res.expected, Delivered: res.delivered, Lost: res.expected - res.delivered,
		Extra: res.extra, OutOfOrder: ss.outOfOrder, LatencyMs: summarize(res.latencies),
	}
	if window > 0 {
		report.Deliver.PerSecond = float64(res.delivered) / window.Seconds()
	}
	report.Streams.Opened = ss.opened
	report.Streams.Disconnects = ss.disconnects
	report.Streams.Reconnects = ss.reconnects
	report.Streams.ConnectErrors = ss.connectErrors
	report.Streams.ConnectMs = summarize(ss.connect)
	report.Streams.Gaps = ss.gaps
	report.Activity = activityReport(streams, ss)
	report.Notes = append(notes, report.notes()...)
	for _, l := range tr.lostSamples(20) {
		logf("lost: %s", l)
	}
	return report, nil
}

// signIn produces the members: from --tokens-file, or one dev sign-in per
// member.
func signIn(ctx context.Context, cfg config, a *api, logf func(string, ...any)) ([]*member, error) {
	if cfg.TokensFile != "" {
		ms, err := readTokensFile(cfg.TokensFile)
		if err != nil {
			return nil, err
		}
		if len(ms) == 0 {
			return nil, fmt.Errorf("%s holds no tokens", cfg.TokensFile)
		}
		out := make([]*member, len(ms))
		for i := range ms {
			out[i] = &ms[i]
		}
		// API tokens become sessions now (the exchange is throttled per
		// client IP like a sign-in: --forwarded-for as with the dev
		// sign-in).
		err = parallel(ctx, len(out), cfg.LoginParallel, func(ctx context.Context, i int) error {
			if out[i].apiToken == "" {
				return nil
			}
			xff := ""
			if cfg.ForwardedFor {
				xff = syntheticIP(i)
			}
			m, err := a.exchangeAPIToken(ctx, out[i].apiToken, xff)
			if err != nil {
				return fmt.Errorf("API token of line member %d: %w", i+1, err)
			}
			*out[i] = m
			return nil
		})
		return out, err
	}
	out := make([]*member, cfg.Members)
	logf("signing in %d members (%s-0001 ...)", cfg.Members, cfg.MemberPrefix)
	err := parallel(ctx, cfg.Members, cfg.LoginParallel, func(ctx context.Context, i int) error {
		xff := ""
		if cfg.ForwardedFor {
			xff = syntheticIP(i)
		}
		m, err := a.login(ctx, fmt.Sprintf("%s-%04d", cfg.MemberPrefix, i+1), xff)
		var se *statusError
		if errors.As(err, &se) && se.status == http.StatusTooManyRequests {
			return fmt.Errorf("sign-in of member %d: the gateway's per-IP sign-in throttle answered 429. Use --tokens-file, or --forwarded-for with the gateway trusting this host as a proxy (BNBLOC_TRUSTED_PROXIES)", i+1)
		}
		if err != nil {
			return fmt.Errorf("sign-in of member %d: %w", i+1, err)
		}
		out[i] = &m
		return nil
	})
	return out, err
}

// syntheticIP is member i's X-Forwarded-For address (10.0.0.1, 10.0.0.2, ...).
func syntheticIP(i int) string {
	n := i + 1
	return fmt.Sprintf("10.%d.%d.%d", (n>>16)&255, (n>>8)&255, n&255)
}

// assignRooms lays members out over rooms: room r's slot k is member
// (r*perRoom + k) mod N, so with rooms*perRoom == N the rooms partition the
// members, and above it members wrap into further rooms. Slot 0 owns a
// created room.
func assignRooms(cfg config, members []*member, runID string) ([]*room, int) {
	n := len(members)
	perRoom := cfg.MembersPerRoom
	if perRoom == 0 {
		perRoom = max(1, n/cfg.Rooms)
	}
	perRoom = min(perRoom, n)
	rooms := make([]*room, cfg.Rooms)
	for r := range rooms {
		rm := &room{}
		if len(cfg.RoomIDs) > 0 {
			rm.id = cfg.RoomIDs[r]
		} else {
			rm.id = fmt.Sprintf("lt-%s-%03d", runID, r+1)
		}
		for k := 0; k < perRoom; k++ {
			rm.members = append(rm.members, members[(r*perRoom+k)%n])
		}
		rooms[r] = rm
	}
	return rooms, perRoom
}

// setupRooms creates the rooms (unless given), adds their members, and
// reads each room's channels, creating the ones --channels asks for beyond
// those the room has.
func setupRooms(ctx context.Context, cfg config, a *api, rooms []*room) error {
	create := len(cfg.RoomIDs) == 0
	if create {
		err := parallel(ctx, len(rooms), cfg.LoginParallel, func(ctx context.Context, i int) error {
			r := rooms[i]
			if err := a.createRoom(ctx, r.members[0], r.id); err != nil {
				return fmt.Errorf("create room %s: %w", r.id, err)
			}
			r.created = true
			return nil
		})
		if err != nil {
			return err
		}
		type add struct {
			r *room
			m *member
		}
		var adds []add
		for _, r := range rooms {
			for _, m := range r.members[1:] {
				if m.subject != r.members[0].subject {
					adds = append(adds, add{r, m})
				}
			}
		}
		err = parallel(ctx, len(adds), cfg.LoginParallel, func(ctx context.Context, i int) error {
			if err := a.addMember(ctx, adds[i].r.members[0], adds[i].r.id, adds[i].m.subject); err != nil {
				return fmt.Errorf("add %s to room %s: %w", adds[i].m.subject, adds[i].r.id, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return parallel(ctx, len(rooms), cfg.LoginParallel, func(ctx context.Context, i int) error {
		r := rooms[i]
		chs, err := a.channels(ctx, r.members[0], r.id)
		if err != nil {
			return fmt.Errorf("room %s channels: %w", r.id, err)
		}
		for n := len(chs) + 1; len(chs) < cfg.Channels; n++ {
			id, err := a.createChannel(ctx, r.members[0], r.id, fmt.Sprintf("lt-%d", n))
			if err != nil {
				return fmt.Errorf("room %s: create a channel: %w", r.id, err)
			}
			chs = append(chs, id)
		}
		r.channels = chs[:cfg.Channels]
		return nil
	})
}

// cleanupRooms deletes the rooms the tool created, as their owner.
func cleanupRooms(a *api, rooms []*room, workers int, logf func(string, ...any)) {
	var created []*room
	for _, r := range rooms {
		if r.created {
			created = append(created, r)
		}
	}
	if len(created) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var mu sync.Mutex
	failed := 0
	_ = parallel(ctx, len(created), workers, func(ctx context.Context, i int) error {
		if err := a.deleteRoom(ctx, created[i].members[0], created[i].id); err != nil {
			mu.Lock()
			failed++
			mu.Unlock()
			logf("cleanup: delete room %s: %v", created[i].id, err)
		}
		return nil
	})
	logf("cleanup: deleted %d of %d rooms", len(created)-failed, len(created))
}

// logKeptRooms names the created rooms --cleanup=false leaves behind, for a
// later --room-ids run.
func logKeptRooms(rooms []*room, logf func(string, ...any)) {
	var ids []string
	for _, r := range rooms {
		if r.created {
			ids = append(ids, r.id)
		}
	}
	if len(ids) > 0 {
		logf("kept rooms (--room-ids): %s", strings.Join(ids, ","))
	}
}

// sendLoop issues sends at cfg.Rate for cfg.Duration, round-robin over a
// shuffled list of every (room, member) pair so the per-subject rate stays as
// low as the population allows. It is open-loop: a send is due by the clock,
// not when the previous one answered. It returns the window length and
// whether ctx ended it early.
func sendLoop(ctx context.Context, cfg config, a *api, rooms []*room, runID string, tr *tracker, st *sendStats) (time.Duration, bool) {
	// A member posts in the channel their stream has open.
	type pair struct {
		r  *room
		m  *member
		ch string
	}
	var pairs []pair
	for _, r := range rooms {
		for k, m := range r.members {
			pairs = append(pairs, pair{r, m, r.channels[k%len(r.channels)]})
		}
	}
	mrand.Shuffle(len(pairs), func(i, j int) { pairs[i], pairs[j] = pairs[j], pairs[i] })
	pad := strings.Repeat("x", cfg.TextBytes)
	sem := make(chan struct{}, cfg.MaxInflight)
	var wg sync.WaitGroup
	tick := min(5*time.Millisecond, time.Duration(float64(time.Second)/cfg.Rate))
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	start := time.Now()
	deadline := start.Add(cfg.Duration)
	sent := 0
	interrupted := false
loop:
	for {
		now := time.Now()
		if !now.Before(deadline) {
			break
		}
		due := int(now.Sub(start).Seconds()*cfg.Rate) + 1
		for ; sent < due; sent++ {
			p := pairs[sent%len(pairs)]
			st.mu.Lock()
			st.attempted++
			st.mu.Unlock()
			select {
			case sem <- struct{}{}:
			default:
				st.mu.Lock()
				st.skipped++
				st.mu.Unlock()
				continue
			}
			id := fmt.Sprintf("lt-%s-%d", runID, sent+1)
			text := id + " " + pad
			text = text[:max(cfg.TextBytes, 1)]
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				// Sends finish on their own timeout, not ctx: an interrupt
				// stops issuing, it does not abandon answers in flight.
				t0 := time.Now()
				tr.begin(id, t0, p.r.liveStreams(p.ch))
				// As the web client: a network error or a 5xx is sent again
				// with the same clientMsgId (web/src/lib/sendRetry.ts).
				err := a.sendChat(context.Background(), p.m, p.r.id, p.ch, id, text)
				for i := 0; err != nil && retryableSend(err); i++ {
					wait, ok := sendRetryWait(i, err)
					if !ok {
						break
					}
					time.Sleep(wait)
					err = a.sendChat(context.Background(), p.m, p.r.id, p.ch, id, text)
				}
				took := time.Since(t0)
				st.mu.Lock()
				defer st.mu.Unlock()
				if err != nil {
					tr.abort(id)
					key := errKey(err)
					st.errors[key]++
					if key == "429" {
						st.rateLimited++
					}
					return
				}
				tr.commit(id)
				p.r.stored.Add(1)
				st.ok++
				st.latencies = append(st.latencies, took)
			}()
		}
		select {
		case <-ctx.Done():
			interrupted = true
			break loop
		case <-ticker.C:
		}
	}
	window := time.Since(start)
	wg.Wait()
	return window, interrupted
}

// drain waits up to d for every committed delivery to arrive.
func drain(ctx context.Context, tr *tracker, d time.Duration) {
	deadline := time.Now().Add(d)
	for tr.outstanding() > 0 && time.Now().Before(deadline) {
		if !sleepCtx(ctx, 50*time.Millisecond) {
			return
		}
	}
}

// startProgress prints a status line every interval until the returned stop
// is called.
func startProgress(every time.Duration, logf func(string, ...any), st *sendStats, tr *tracker, streams []*stream) func() {
	if every <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	start := time.Now()
	go func() {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				st.mu.Lock()
				ok, rl, skipped := st.ok, st.rateLimited, st.skipped
				errs := 0
				for _, n := range st.errors {
					errs += n
				}
				st.mu.Unlock()
				res := tr.result()
				logf("t=%3.0fs sent ok=%d 429=%d errors=%d skipped=%d delivered=%d outstanding=%d streams open=%d/%d",
					time.Since(start).Seconds(), ok, rl, errs, skipped, res.delivered, res.expected-res.delivered, countLive(streams), len(streams))
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

// perMemberRate is the target send rate of the busiest member: pairs are
// served round-robin, so a member in k rooms sends k times per cycle.
func perMemberRate(rate float64, rooms []*room) float64 {
	count := map[string]int{}
	total := 0
	for _, r := range rooms {
		for _, m := range r.members {
			count[m.subject]++
			total++
		}
	}
	busiest := 0
	for _, n := range count {
		busiest = max(busiest, n)
	}
	if total == 0 {
		return 0
	}
	return rate * float64(busiest) / float64(total)
}

func earliestExpiry(members []*member) time.Time {
	var out time.Time
	for _, m := range members {
		if !m.expires.IsZero() && (out.IsZero() || m.expires.Before(out)) {
			out = m.expires
		}
	}
	return out
}

func countLive(streams []*stream) int {
	n := 0
	for _, s := range streams {
		if s.live.Load() {
			n++
		}
	}
	return n
}

// waitAllLive waits up to d for every stream to be open. It reports false
// only when ctx ended.
func waitAllLive(ctx context.Context, streams []*stream, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for countLive(streams) < len(streams) && time.Now().Before(deadline) {
		if !sleepCtx(ctx, 50*time.Millisecond) {
			return false
		}
	}
	return ctx.Err() == nil
}

// sleepCtx sleeps d; false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// parallel runs f(0..n-1) on up to workers goroutines; the first error
// cancels the rest and is returned.
func parallel(ctx context.Context, n, workers int, f func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idx := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for w := 0; w < min(workers, n); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if err := f(ctx, i); err != nil {
					once.Do(func() { first = err; cancel() })
				}
			}
		}()
	}
feed:
	for i := 0; i < n; i++ {
		select {
		case idx <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(idx)
	wg.Wait()
	if first == nil && ctx.Err() != nil {
		// The parent context ended (interrupt during setup).
		return context.Canceled
	}
	return first
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sendRetryWaits are the web client's waits before sending again, and
// sendUnavailableWaits its further waits while the answer is a 503 (the
// database failing over) (web/src/lib/sendRetry.ts).
var (
	sendRetryWaits       = []time.Duration{400 * time.Millisecond, 1200 * time.Millisecond, 3 * time.Second}
	sendUnavailableWaits = []time.Duration{5 * time.Second, 8 * time.Second, 8 * time.Second, 8 * time.Second}
)

// sendRetryWait is the wait before attempt i+2 of a send that failed with
// err; false once there is none.
func sendRetryWait(i int, err error) (time.Duration, bool) {
	if i < len(sendRetryWaits) {
		return sendRetryWaits[i], true
	}
	var se *statusError
	if errors.As(err, &se) && se.status == 503 && i-len(sendRetryWaits) < len(sendUnavailableWaits) {
		return sendUnavailableWaits[i-len(sendRetryWaits)], true
	}
	return 0, false
}

// retryableSend: no answer, or a 5xx.
func retryableSend(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.status >= 500
	}
	return true
}
