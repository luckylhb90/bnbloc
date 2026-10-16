package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// Room tools (docs/architecture/room-tools.md): every session the bridge
// creates on an agentProfile core gets the loopback room tools MCP server,
// with a bearer of its own bound to the job the session is driving.

// EnableRoomTools starts the room tools server and registers it as a
// session contributor. shimCommand is this executable (the stdio shim is
// its `roomtools-stdio` subcommand); "" offers only the http transport.
// The returned stop closes the server and revokes every bearer. Call it
// before Run.
func (b *Bridge) EnableRoomTools(shimCommand string) (stop func(), err error) {
	st := loadRoomToolsState(b.cfg.StateDir)
	srv, err := roomtools.Start(roomtools.Config{Gateway: roomToolsGateway{b}, GatewayBaseURL: b.cfg.GatewayBaseURL, Addr: st.Addr, Names: b.displayNames,
		Publish: b.publishGeneratedFile})
	if err != nil {
		return nil, err
	}
	st.Addr = srv.Addr()
	st.save(b.cfg.StateDir)
	b.AddSessionContributor(&roomToolsContributor{srv: srv, shimCommand: shimCommand, key: st.key})
	return func() { _ = srv.Close() }, nil
}

// The room tools server keeps, in the bridge's state directory, what lets a
// session restored after a restart keep the server its agent was given
// (services/bridge/session_restore.go): the address it listens on
// (roomtools.json; a busy address means a new one, and those sessions are
// started anew) and the key its session bearers derive from
// (roomtools.key, owner-only), so a session's bearer can be derived again
// from the nonce its binding kept. Without a state directory both are
// per run.
const (
	roomToolsStateFile = "roomtools.json"
	roomToolsKeyFile   = "roomtools.key"
)

type roomToolsState struct {
	Addr string `json:"addr"`
	key  []byte
}

func loadRoomToolsState(dir string) roomToolsState {
	var st roomToolsState
	if dir != "" {
		if raw, err := os.ReadFile(filepath.Join(dir, roomToolsStateFile)); err == nil {
			_ = json.Unmarshal(raw, &st)
		}
		if raw, err := os.ReadFile(filepath.Join(dir, roomToolsKeyFile)); err == nil && len(raw) >= 32 {
			st.key = raw
		}
	}
	if st.key == nil {
		st.key = make([]byte, 32)
		_, _ = rand.Read(st.key)
		if dir != "" {
			if err := os.MkdirAll(dir, 0o700); err == nil {
				if err := os.WriteFile(filepath.Join(dir, roomToolsKeyFile), st.key, 0o600); err != nil {
					slog.Warn("bnbloc bridge room tools key not saved", "event", "bnbloc.bridge.roomtools_key_save_failed", "error", err)
				}
			}
		}
	}
	return st
}

func (st roomToolsState) save(dir string) {
	if dir == "" {
		return
	}
	if err := writeFileAtomic(filepath.Join(dir, roomToolsStateFile), st); err != nil {
		slog.Warn("bnbloc bridge room tools state not saved", "event", "bnbloc.bridge.roomtools_state_save_failed", "error", err)
	}
}

// roomToolsGateway is the tools' gateway client: the bridge's own
// authenticated session, never handed to the agent.
type roomToolsGateway struct{ b *Bridge }

func (g roomToolsGateway) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var raw []byte
	var err error
	if rb, ok := body.(roomtools.RawBody); ok {
		header := rb.Header.Clone()
		var data io.Reader
		if rb.ContentType != "" {
			if header == nil {
				header = http.Header{}
			}
			header.Set("Content-Type", rb.ContentType)
			data = bytes.NewReader(rb.Data)
		}
		raw, _, err = g.b.send(ctx, g.b.http, method, g.b.cfg.GatewayBaseURL+path, g.b.gatewayAuth(ctx), header, data)
	} else {
		raw, err = g.b.do(ctx, method, g.b.cfg.GatewayBaseURL+path, g.b.gatewayAuth(ctx), body)
	}
	var ge *gatewayError
	if errors.As(err, &ge) {
		return nil, &roomtools.StatusError{Status: ge.Status, Body: ge.Body}
	}
	return raw, err
}

// roomToolsContributor attaches the server to one session.
type roomToolsContributor struct {
	srv         *roomtools.Server
	shimCommand string
	// key derives each session's bearer from the nonce its State keeps.
	key []byte
}

// roomToolsGrant is a session's State: how it reaches the server (never
// its bearer, which derives from the nonce with the key).
type roomToolsGrant struct {
	URL   string `json:"url"`
	Nonce string `json:"nonce"`
}

// bearer is the session bearer a nonce derives.
func (c *roomToolsContributor) bearer(nonce string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte("bnbloc room tools bearer\x00" + nonce))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// hooks are a session's turn and release callbacks.
func (c *roomToolsContributor) hooks(sess *roomtools.Session) (func(TurnInfo), func()) {
	return func(t TurnInfo) {
			sess.Bind(roomtools.Job{JobID: t.JobID, RequesterUserID: t.RequesterUserID, ChannelID: t.ChannelID, MessageSeq: t.MessageSeq, Workspace: t.Workspace, Team: t.Team})
		}, func() {
			c.srv.Revoke(sess)
		}
}

// RestoreSession reopens a session's bearer after a bridge restart, when the
// server listens where its agent was told it does.
func (c *roomToolsContributor) RestoreSession(_ context.Context, info SessionInfo, state json.RawMessage) (SessionContribution, bool) {
	var g roomToolsGrant
	if json.Unmarshal(state, &g) != nil || g.Nonce == "" || g.URL != c.srv.URL() {
		return SessionContribution{}, false
	}
	sess, err := c.srv.OpenWithToken(roomToolsScope(info), c.bearer(g.Nonce))
	if err != nil {
		return SessionContribution{}, false
	}
	turn, release := c.hooks(sess)
	return SessionContribution{RoomTools: true, Turn: turn, Release: release, State: state}, true
}

// publishGeneratedFile publishes a file the room tools generated on the
// deployment's media provider (an image it drew) into the room's Docs, as a
// deliverable of the bound job — the bridge's one publish path
// (publishArtifactNow). It answers the artifact id.
func (b *Bridge) publishGeneratedFile(ctx context.Context, room string, job roomtools.Job, name, mimeType string, content []byte) (string, error) {
	rs := b.latestRoomSession(room)
	if rs == nil {
		return "", fmt.Errorf("the room has no session to publish through")
	}
	rel := "generated/" + name
	d := deliverable{rel: rel, kind: store.ArtifactKindForFile(rel, mimeType), title: name, fileName: name,
		mimeType: mimeType, content: content, turn: deliverableTurn{JobID: job.JobID, SourceSeq: job.MessageSeq}}
	if err := b.publishArtifactNow(ctx, room, job.ChannelID, rs, d, "agent"); err != nil {
		return "", err
	}
	return artifactIDForFile(room, rs, rel), nil
}

func roomToolsScope(info SessionInfo) roomtools.Scope {
	return roomtools.Scope{RoomID: info.RoomID, TenantID: info.TenantID, Owner: info.Owner, Core: info.Core, Provider: info.Provider}
}

// ContributeSession mints the session's bearer and attaches the server over
// http when the session's agent accepts http MCP servers, else through the
// stdio shim (every agent accepts stdio). The bearer is bound to each
// turn's job as the turn starts and revoked when the session binding is
// dropped.
func (c *roomToolsContributor) ContributeSession(_ context.Context, info SessionInfo) (SessionContribution, error) {
	server := corepkg.MCPServer{Name: roomtools.ServerName}
	switch {
	case (corepkg.Target{MCPTransports: info.MCPTransports}).TransportSupported(corepkg.MCPHTTP):
		server.Type = corepkg.MCPHTTP
		server.URL = c.srv.URL()
	case c.shimCommand != "":
		server.Type = corepkg.MCPStdio
		server.Command = c.shimCommand
		server.Args = []string{"roomtools-stdio", "--url", c.srv.URL()}
	default:
		return SessionContribution{}, fmt.Errorf("room tools: the %s agent accepts no http MCP server and no stdio shim is available", info.Provider)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return SessionContribution{}, fmt.Errorf("room tools: nonce: %w", err)
	}
	nonce := hex.EncodeToString(raw)
	token := c.bearer(nonce)
	sess, err := c.srv.OpenWithToken(roomToolsScope(info), token)
	if err != nil {
		return SessionContribution{}, err
	}
	state, _ := json.Marshal(roomToolsGrant{URL: c.srv.URL(), Nonce: nonce})
	if server.Type == corepkg.MCPHTTP {
		server.Headers = []corepkg.NameValue{{Name: "Authorization", Value: "Bearer " + token}}
	} else {
		server.Env = []corepkg.NameValue{{Name: roomtools.TokenEnv, Value: token}}
	}
	turn, release := c.hooks(sess)
	return SessionContribution{MCPServers: []corepkg.MCPServer{server}, RoomTools: true, Turn: turn, Release: release, State: state}, nil
}
