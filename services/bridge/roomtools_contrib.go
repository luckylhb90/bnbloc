package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	corepkg "github.com/luckylhb90/bnbloc/core"
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
	srv, err := roomtools.Start(roomtools.Config{Gateway: roomToolsGateway{b}, GatewayBaseURL: b.cfg.GatewayBaseURL})
	if err != nil {
		return nil, err
	}
	b.AddSessionContributor(&roomToolsContributor{srv: srv, shimCommand: shimCommand})
	return func() { _ = srv.Close() }, nil
}

// roomToolsGateway is the tools' gateway client: the bridge's own
// authenticated session, never handed to the agent.
type roomToolsGateway struct{ b *Bridge }

func (g roomToolsGateway) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var raw []byte
	var err error
	if rb, ok := body.(roomtools.RawBody); ok {
		raw, err = g.b.doRaw(ctx, g.b.http, method, g.b.cfg.GatewayBaseURL+path, g.b.gatewayAuth(ctx), rb.ContentType, bytes.NewReader(rb.Data))
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
	token, sess, err := c.srv.Open(roomtools.Scope{RoomID: info.RoomID, TenantID: info.TenantID, Owner: info.Owner, Core: info.Core, Provider: info.Provider})
	if err != nil {
		return SessionContribution{}, err
	}
	if server.Type == corepkg.MCPHTTP {
		server.Headers = []corepkg.NameValue{{Name: "Authorization", Value: "Bearer " + token}}
	} else {
		server.Env = []corepkg.NameValue{{Name: roomtools.TokenEnv, Value: token}}
	}
	return SessionContribution{
		MCPServers:   []corepkg.MCPServer{server},
		Instructions: roomtools.Instructions,
		Turn: func(t TurnInfo) {
			sess.Bind(roomtools.Job{JobID: t.JobID, RequesterUserID: t.RequesterUserID, ChannelID: t.ChannelID, MessageSeq: t.MessageSeq, Workspace: t.Workspace})
		},
		Release: func() { c.srv.Revoke(sess) },
	}, nil
}
