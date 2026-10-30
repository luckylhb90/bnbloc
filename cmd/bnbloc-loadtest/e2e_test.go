package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeToken(subject string, exp time.Time) string {
	raw, _ := json.Marshal(map[string]any{"sub": subject, "exp": exp.Unix()})
	return base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

func fakeKindToken(kind string) string {
	raw, _ := json.Marshal(map[string]any{"sub": "maya", "kind": kind})
	return base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

// fakeGateway implements the public routes the load test calls, with a room
// hub that, like realtime, replays a room's earlier events to a new
// subscriber.
type fakeGateway struct {
	t *testing.T
	// chatBurst > 0 lets each subject send that many chats, then 429s.
	chatBurst int
	// dropAfter > 0 closes every stream's FIRST connection after that long.
	dropAfter time.Duration

	mu        sync.Mutex
	rooms     map[string]*fakeRoom
	sent      map[string]int
	deleted   []string
	connected map[string]bool // subject/room that already had a connection
	badQuery  []string
}

type fakeRoom struct {
	owner    string
	members  map[string]bool
	channels []string
	events   []fakeEvent
	// subs maps each stream to the channel it has open (a lanes stream:
	// full frames there, a channel_activity frame for the others).
	subs map[chan string]string
}

type fakeEvent struct{ channel, frame string }

func (rm *fakeRoom) hasChannel(id string) bool {
	for _, c := range rm.channels {
		if c == id {
			return true
		}
	}
	return false
}

func newFakeGateway(t *testing.T) *fakeGateway {
	return &fakeGateway{t: t, rooms: map[string]*fakeRoom{}, sent: map[string]int{}, connected: map[string]bool{}}
}

func (g *fakeGateway) subject(r *http.Request) string {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		tok = r.URL.Query().Get("access_token")
	}
	sub, _, err := sessionClaims(tok)
	if err != nil {
		return ""
	}
	return sub
}

func (g *fakeGateway) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDPToken string `json:"idpToken"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSONT(w, map[string]string{"sessionToken": fakeToken(body.IDPToken, time.Now().Add(time.Hour)), "subject": body.IDPToken})
	})
	mux.HandleFunc("POST /v1/rooms", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RoomID string `json:"roomId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sub := g.subject(r)
		g.mu.Lock()
		defer g.mu.Unlock()
		if _, ok := g.rooms[body.RoomID]; ok {
			http.Error(w, "exists", http.StatusConflict)
			return
		}
		g.rooms[body.RoomID] = &fakeRoom{owner: sub, members: map[string]bool{sub: true}, channels: []string{"ch-" + body.RoomID}, subs: map[chan string]string{}}
		writeJSONT(w, map[string]string{"roomId": body.RoomID})
	})
	mux.HandleFunc("POST /v1/rooms/{id}/members", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UserID string `json:"userId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		defer g.mu.Unlock()
		rm := g.rooms[r.PathValue("id")]
		if rm == nil || rm.owner != g.subject(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		rm.members[body.UserID] = true
	})
	mux.HandleFunc("GET /v1/rooms/{id}/channels", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		rm := g.rooms[r.PathValue("id")]
		if rm == nil {
			http.Error(w, "no room", http.StatusNotFound)
			return
		}
		var list []map[string]any
		for i, c := range rm.channels {
			list = append(list, map[string]any{"channelId": c, "isDefault": i == 0})
		}
		// The room service lists the default first; the tool must not rely on it.
		sort.Slice(list, func(i, j int) bool { return list[i]["channelId"].(string) > list[j]["channelId"].(string) })
		writeJSONT(w, map[string]any{"channels": list})
	})
	mux.HandleFunc("POST /v1/rooms/{id}/channels", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		rm := g.rooms[r.PathValue("id")]
		if rm == nil || rm.owner != g.subject(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		id := fmt.Sprintf("ch-%s-%d", r.PathValue("id"), len(rm.channels)+1)
		rm.channels = append(rm.channels, id)
		writeJSONT(w, map[string]string{"channelId": id})
	})
	mux.HandleFunc("DELETE /v1/rooms/{id}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		id := r.PathValue("id")
		if rm := g.rooms[id]; rm == nil || rm.owner != g.subject(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		g.deleted = append(g.deleted, id)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/rooms/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text        string `json:"text"`
			ClientMsgID string `json:"clientMsgId"`
			ChannelID   string `json:"channelId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sub := g.subject(r)
		g.mu.Lock()
		defer g.mu.Unlock()
		rm := g.rooms[r.PathValue("id")]
		if rm == nil || !rm.members[sub] {
			http.Error(w, "not a member", http.StatusForbidden)
			return
		}
		if body.ClientMsgID == "" || !rm.hasChannel(body.ChannelID) {
			http.Error(w, "bad chat body", http.StatusBadRequest)
			return
		}
		if g.chatBurst > 0 && g.sent[sub] >= g.chatBurst {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		g.sent[sub]++
		seq := len(rm.events) + 1
		data, _ := json.Marshal(map[string]any{"sequence": seq, "kind": "chat_message", "channelId": body.ChannelID,
			"payload": map[string]any{"userId": sub, "text": body.Text, "clientMsgId": body.ClientMsgID}})
		frame := fmt.Sprintf("id: %d\nevent: chat_message\ndata: %s\n\n", seq, data)
		act, _ := json.Marshal(map[string]any{"kind": "channel_activity", "channelId": body.ChannelID,
			"payload": map[string]any{"channelId": body.ChannelID, "lastSeq": seq, "events": 1}})
		activity := fmt.Sprintf("event: channel_activity\ndata: %s\n\n", act)
		rm.events = append(rm.events, fakeEvent{body.ChannelID, frame})
		for ch, open := range rm.subs {
			f := frame
			if open != body.ChannelID {
				f = activity
			}
			select {
			case ch <- f:
			default:
			}
		}
		writeJSONT(w, map[string]int{"sequence": seq})
	})
	mux.HandleFunc("GET /v1/rooms/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id := r.PathValue("id")
		sub := g.subject(r)
		ch := make(chan string, 1024)
		g.mu.Lock()
		rm := g.rooms[id]
		if rm == nil || !rm.members[sub] {
			g.mu.Unlock()
			http.Error(w, "not a member", http.StatusForbidden)
			return
		}
		open := q.Get("open")
		if q.Get("access_token") == "" || q.Get("lanes") != "1" || q.Get("includePeople") != "1" || !rm.hasChannel(open) || q.Has("allChannels") || r.Header.Get("Authorization") != "" {
			g.badQuery = append(g.badQuery, r.URL.RawQuery)
		}
		var replay []string
		for _, e := range rm.events {
			if e.channel == open {
				replay = append(replay, e.frame)
			}
		}
		rm.subs[ch] = open
		key := sub + "/" + id
		first := !g.connected[key]
		g.connected[key] = true
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			delete(rm.subs, ch)
			g.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, ": connected\n\n")
		for _, f := range replay {
			io.WriteString(w, f)
		}
		fl.Flush()
		var drop <-chan time.Time
		if first && g.dropAfter > 0 {
			drop = time.After(g.dropAfter)
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-drop:
				return
			case f := <-ch:
				io.WriteString(w, f)
				fl.Flush()
			}
		}
	})
	return mux
}

func writeJSONT(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func e2eConfig(url string) config {
	return config{
		Gateway: url, Members: 6, MemberPrefix: "maya", LoginParallel: 4, Rooms: 2, Channels: 1,
		Rate: 40, Duration: time.Second, RampUp: 100 * time.Millisecond, Settle: 2 * time.Second,
		Drain: 2 * time.Second, TextBytes: 32, MaxInflight: 64, Timeout: 5 * time.Second,
		StreamIdle: 10 * time.Second, Cleanup: true,
	}
}

func TestEndToEndAgainstFakeGateway(t *testing.T) {
	g := newFakeGateway(t)
	srv := httptest.NewServer(g.handler())
	defer srv.Close()

	rep, err := run(context.Background(), e2eConfig(srv.URL), io.Discard)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(g.badQuery) > 0 {
		t.Fatalf("streams did not use the web client's query: %v", g.badQuery)
	}
	if rep.Setup.Streams != 6 || rep.Setup.MembersPerRoom != 3 || rep.Streams.OpenAtStart != 6 || rep.Streams.OpenAtEnd != 6 {
		t.Fatalf("streams: setup %+v, streams %+v", rep.Setup, rep.Streams)
	}
	if rep.Sends.OK < 35 || rep.Sends.OK != rep.Sends.Attempted || len(rep.Sends.Errors) != 0 {
		t.Fatalf("sends: %+v", rep.Sends)
	}
	// Every member of the room (sender included) receives every message.
	if rep.Deliver.Expected != rep.Sends.OK*3 || rep.Deliver.Delivered != rep.Deliver.Expected || rep.Deliver.Lost != 0 || rep.Deliver.Extra != 0 {
		t.Fatalf("delivery: %+v (ok sends %d)", rep.Deliver, rep.Sends.OK)
	}
	if rep.Deliver.LatencyMs.Count != rep.Deliver.Delivered || rep.Deliver.LatencyMs.Max <= 0 {
		t.Fatalf("latency summary: %+v", rep.Deliver.LatencyMs)
	}
	if rep.Streams.Disconnects != 0 || rep.Streams.Reconnects != 0 || rep.Streams.Opened != 6 {
		t.Fatalf("stream churn on a stable server: %+v", rep.Streams)
	}
	if len(g.deleted) != 2 {
		t.Fatalf("cleanup deleted %v, want both rooms", g.deleted)
	}
	if len(rep.Notes) != 0 {
		t.Fatalf("clean run has notes: %v", rep.Notes)
	}
}

// Members spread over a room's channels (--channels): a message reaches in
// full the streams that have its channel open, and every other stream hears
// of it as its channel's activity.
func TestEndToEndChannels(t *testing.T) {
	g := newFakeGateway(t)
	srv := httptest.NewServer(g.handler())
	defer srv.Close()
	cfg := e2eConfig(srv.URL)
	cfg.Rooms, cfg.Channels = 1, 3

	rep, err := run(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(g.badQuery) > 0 {
		t.Fatalf("streams did not use the web client's query: %v", g.badQuery)
	}
	// 6 members over 3 channels: two streams have each message's channel open.
	if rep.Setup.Channels != 3 || rep.Deliver.Expected != rep.Sends.OK*2 || rep.Deliver.Lost != 0 || rep.Deliver.Extra != 0 {
		t.Fatalf("delivery over channels: setup %+v, delivery %+v (ok sends %d)", rep.Setup, rep.Deliver, rep.Sends.OK)
	}
	if rep.Activity.Streams != 6 || rep.Activity.Complete != 6 || rep.Activity.Events != rep.Sends.OK*4 {
		t.Fatalf("activity: %+v (ok sends %d)", rep.Activity, rep.Sends.OK)
	}
	if len(g.deleted) != 1 {
		t.Fatalf("cleanup deleted %v", g.deleted)
	}
}

func TestEndToEndRateLimitIsReported(t *testing.T) {
	g := newFakeGateway(t)
	g.chatBurst = 2
	srv := httptest.NewServer(g.handler())
	defer srv.Close()
	cfg := e2eConfig(srv.URL)
	cfg.Members, cfg.Rooms, cfg.Rate = 2, 1, 20

	rep, err := run(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Sends.OK != 4 || rep.Sends.RateLimited != rep.Sends.Attempted-4 || rep.Sends.Errors["429"] != rep.Sends.RateLimited {
		t.Fatalf("sends: %+v", rep.Sends)
	}
	if rep.Deliver.Expected != 8 || rep.Deliver.Delivered != 8 {
		t.Fatalf("only committed sends are expected: %+v", rep.Deliver)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "per-subject chat limit bound") {
		t.Fatalf("notes do not flag the limiter: %v", rep.Notes)
	}
}

func TestEndToEndReconnectCatchesUpFromReplay(t *testing.T) {
	old := reconnectBase
	reconnectBase = 20 * time.Millisecond
	defer func() { reconnectBase = old }()
	g := newFakeGateway(t)
	g.dropAfter = 300 * time.Millisecond
	srv := httptest.NewServer(g.handler())
	defer srv.Close()
	cfg := e2eConfig(srv.URL)
	cfg.RampUp = 0

	rep, err := run(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Streams.Disconnects != 6 || rep.Streams.Reconnects != 6 || rep.Streams.Opened != 12 || rep.Streams.OpenAtEnd != 6 {
		t.Fatalf("streams: %+v", rep.Streams)
	}
	// The replay redelivers what each stream already had (extra) and fills
	// the gap, so nothing is lost.
	if rep.Deliver.Lost != 0 || rep.Deliver.Extra == 0 {
		t.Fatalf("delivery: %+v", rep.Deliver)
	}
}

func TestEndToEndInterruptStillCleansUp(t *testing.T) {
	g := newFakeGateway(t)
	srv := httptest.NewServer(g.handler())
	defer srv.Close()
	cfg := e2eConfig(srv.URL)
	cfg.Duration = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(700 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	rep, err := run(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Interrupted || time.Since(start) > 10*time.Second {
		t.Fatalf("interrupt not honored: interrupted=%v after %s", rep.Interrupted, time.Since(start))
	}
	if len(g.deleted) != 2 {
		t.Fatalf("cleanup after interrupt deleted %v", g.deleted)
	}
}

func TestParseFlags(t *testing.T) {
	c, err := parseFlags([]string{"--gateway", "http://gw/", "--room-ids", "a, b"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Gateway != "http://gw" || c.Rooms != 2 || len(c.RoomIDs) != 2 || !c.Cleanup {
		t.Fatalf("parsed %+v", c)
	}
	if c.Channels != 1 {
		t.Fatalf("default channels = %d, want 1", c.Channels)
	}
	for _, bad := range [][]string{{}, {"--gateway", "x", "--rate", "0"}, {"--gateway", "x", "extra"}, {"--gateway", "x", "--channels", "0"}} {
		if _, err := parseFlags(bad, io.Discard); err == nil {
			t.Errorf("parseFlags(%v) accepted", bad)
		}
	}
}

func TestEndToEndKeepThenReuseRooms(t *testing.T) {
	g := newFakeGateway(t)
	srv := httptest.NewServer(g.handler())
	defer srv.Close()
	cfg := e2eConfig(srv.URL)
	cfg.Cleanup = false
	cfg.Duration = 300 * time.Millisecond
	if _, err := run(context.Background(), cfg, io.Discard); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(g.deleted) != 0 || len(g.rooms) != 2 {
		t.Fatalf("--cleanup=false: deleted %v, rooms %d", g.deleted, len(g.rooms))
	}
	cfg.RoomIDs = nil
	for id := range g.rooms {
		cfg.RoomIDs = append(cfg.RoomIDs, id)
	}
	sort.Strings(cfg.RoomIDs)
	cfg.Rooms = len(cfg.RoomIDs)
	cfg.Cleanup = true
	rep, err := run(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("reuse run: %v", err)
	}
	if rep.Setup.RoomsCreated || len(g.rooms) != 2 || len(g.deleted) != 0 {
		t.Fatalf("existing rooms were created or deleted: created=%v rooms=%d deleted=%v", rep.Setup.RoomsCreated, len(g.rooms), g.deleted)
	}
	if rep.Deliver.Expected == 0 || rep.Deliver.Delivered != rep.Deliver.Expected {
		t.Fatalf("delivery in reused rooms: %+v", rep.Deliver)
	}
}
