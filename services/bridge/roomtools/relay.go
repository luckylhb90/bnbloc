package roomtools

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The room relay: how an MCP tool component whose credentials are the room
// (pkgmeta.MCPCredentialsRoom) reaches the gateway. The bridge gives the
// tool's process this server's relay address and the agent session's
// bearer; the tool calls a gateway route through it, and the relay sends
// the call with the bridge's own client under the bound job's grant
// (?room=&job=, set here, never by the tool). So:
//
//   - the gateway session never leaves the bridge (the bearer means nothing
//     outside this process and dies with the session binding);
//   - every call acts as the requester of the turn being driven now, the
//     job re-bound at each turn (Session.Bind), like the room tools' own;
//     a call may name an earlier job of the session instead (?job=, one
//     Session.drove), which the gateway admits only where a route allows
//     a job whose turn ended (a live view waiting for a person, §3.7);
//   - only the routes in wire.RoomRelayRoutes are reachable, each one a
//     gateway route that admits a job grant;
//   - a relayed call lasts as long as the tool's call that made it (its
//     request's context), never a fixed limit: a picture or a video's
//     publish takes what it takes (docs/prd/2026-10-19-video-generation.md
//     §2.4), and a stalled gateway ends with the agent's tool call;
//   - GET <relay>/session answers the bound turn itself
//     (component.RelaySession), never reaching the gateway;
//   - once the session is revoked its bearer still posts the live view of
//     a job it drove (POST /v1/live?job=, relayRetired) and nothing else,
//     until the gateway refuses the view or it goes quiet.

// RelayPath prefixes the relay's routes: <server>/relay/<gateway path>.
const RelayPath = "/relay"

// maxRelayBody bounds one relayed request body (a transcription's audio is
// the largest the routes take).
const maxRelayBody = wire.MaxMediaAudioBytes + 1<<20

// RelayURL is the relay base a room-credential tool is given.
func (s *Server) RelayURL() string { return "http://" + s.ln.Addr().String() + RelayPath }

// handleRelay serves one relayed call.
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	if !loopbackOrigin(r.Header.Get("Origin")) || !loopbackHost(r.Host) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, RelayPath)
	sess := s.authenticate(r)
	if sess == nil {
		if s.relayRetired(w, r, path) {
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="bnbloc-room"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && path == component.RelaySessionPath {
		s.relaySession(w, sess)
		return
	}
	if !relayed(r.Method, path) {
		http.Error(w, "the room relay does not reach "+r.Method+" "+path, http.StatusNotFound)
		return
	}
	job := sess.current()
	if job.JobID == "" {
		http.Error(w, "no turn is running in this session", http.StatusConflict)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRelayBody+1))
	if err != nil || len(body) > maxRelayBody {
		http.Error(w, "request too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	q := r.URL.Query()
	jobID := job.JobID
	if named := strings.TrimSpace(q.Get("job")); named != "" {
		if !sess.drove(named) {
			http.Error(w, "this session did not drive job "+named, http.StatusForbidden)
			return
		}
		jobID = named
	}
	q.Set("room", sess.Scope().RoomID)
	q.Set("job", jobID)
	s.forward(w, r, path, q, body)
}

// relayRetired serves a revoked session's live post (POST /v1/live?job=, a
// job it drove): the view a sign-in waits on outlives the agent session it
// was started in (PRD §3.7); the gateway admits it only while the view
// waits. It answers whether it handled the request.
func (s *Server) relayRetired(w http.ResponseWriter, r *http.Request, path string) bool {
	if r.Method != http.MethodPost || path != liveRelayPath {
		return false
	}
	token, _ := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	token = strings.TrimSpace(token)
	job := strings.TrimSpace(r.URL.Query().Get("job"))
	ret := s.retiredFor(token, job)
	if token == "" || job == "" || ret == nil {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRelayBody+1))
	if err != nil || len(body) > maxRelayBody {
		http.Error(w, "request too large or unreadable", http.StatusRequestEntityTooLarge)
		return true
	}
	q := url.Values{}
	q.Set("room", ret.scope.RoomID)
	q.Set("job", job)
	if status := s.forward(w, r, path, q, body); status == http.StatusConflict || status == http.StatusForbidden || status == http.StatusNotFound {
		s.dropRetired(token) // the view no longer waits: nothing more is relayed for it
	}
	return true
}

// liveRelayPath is the one route a retired session reaches.
const liveRelayPath = "/v1/live"

// forward sends one relayed call with the bridge's client, bound to the
// tool's request alone (RawBody.Relay), and copies the gateway's answer; it
// returns the answer's status.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, path string, q url.Values, body []byte) int {
	raw, err := s.cfg.Gateway.Do(r.Context(), r.Method, path+"?"+q.Encode(), RawBody{ContentType: r.Header.Get("Content-Type"), Data: body, Relay: true})
	var se *StatusError
	switch {
	case errors.As(err, &se):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(se.Status)
		_, _ = w.Write(se.Body)
		return se.Status
	case err != nil:
		obs.ServerError(w, "relay a room-credential tool's call failed", err)
		http.Error(w, "the gateway could not be reached", http.StatusBadGateway)
		return http.StatusBadGateway
	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return http.StatusOK
	}
}

// relaySession answers the turn the session is driving now
// (component.RelaySession): the facts a tool needs to tell who it acts for,
// whose machine it runs on, and whether this turn is newer than one it
// recorded. 409 when no turn is bound.
func (s *Server) relaySession(w http.ResponseWriter, sess *Session) {
	job := sess.current()
	if job.JobID == "" {
		http.Error(w, "no turn is running in this session", http.StatusConflict)
		return
	}
	scope := sess.Scope()
	raw, _ := json.Marshal(component.RelaySession{
		JobID: job.JobID, RoomID: scope.RoomID, ChannelID: job.ChannelID,
		Requester: job.RequesterUserID, Owner: scope.Owner,
		MessageSeq: job.MessageSeq, ByRequester: job.ByRequester,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// relayed reports a route the relay reaches: one of wire.RoomRelayRoutes,
// its path exact (no query, no escapes, no traversal) but for a route's
// {name} segment, which matches one path segment that is not empty or a
// dot segment (core.CleanRelPath; a video's id).
func relayed(method, path string) bool {
	if u, err := url.Parse(path); err != nil || u.Path != path || u.RawQuery != "" {
		return false
	}
	got := strings.Split(path, "/")
	for _, rt := range wire.RoomRelayRoutes {
		m, pattern, _ := strings.Cut(rt, " ")
		want := strings.Split(pattern, "/")
		if m != method || len(want) != len(got) {
			continue
		}
		match := true
		for i, seg := range want {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				clean, ok := core.CleanRelPath(got[i])
				match = match && ok && clean == got[i]
				continue
			}
			match = match && seg == got[i]
		}
		if match {
			return true
		}
	}
	return false
}
