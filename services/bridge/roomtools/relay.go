package roomtools

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/component"
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
//   - only the routes in wire.RoomRelayRoutes are reachable, each one a
//     gateway route that admits a job grant;
//   - GET <relay>/session answers the bound turn itself
//     (component.RelaySession), never reaching the gateway.

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
	sess := s.authenticate(r)
	if sess == nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="bnbloc-room"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, RelayPath)
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
	q.Set("room", sess.Scope().RoomID)
	q.Set("job", job.JobID)
	raw, err := s.cfg.Gateway.Do(r.Context(), r.Method, path+"?"+q.Encode(), RawBody{ContentType: r.Header.Get("Content-Type"), Data: body})
	var se *StatusError
	switch {
	case errors.As(err, &se):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(se.Status)
		_, _ = w.Write(se.Body)
	case err != nil:
		obs.ServerError(w, "relay a room-credential tool's call failed", err)
		http.Error(w, "the gateway could not be reached", http.StatusBadGateway)
	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
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
// its path exact (no query, no traversal).
func relayed(method, path string) bool {
	if u, err := url.Parse(path); err != nil || u.Path != path || u.RawQuery != "" {
		return false
	}
	for _, rt := range wire.RoomRelayRoutes {
		if rt == method+" "+path {
			return true
		}
	}
	return false
}
