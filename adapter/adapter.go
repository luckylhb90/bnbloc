// Package adapter is the daemon-side host adapter for bnbloc: the
// ActivityReporter a member's local agent runtime wires into its own ports to
// post session-state and messages to bnbloc-activity. A runtime integration
// adapts it to the durable activity-reporter hook its daemon config requires;
// it edits no runtime core source.
//
// roomId is always the daemon's workspaceId (roomId ≡ workspaceId).
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Config configures the bnbloc host adapter clients.
type Config struct {
	// GatewayBaseURL is the bnbloc-gateway base URL the daemon talks to.
	GatewayBaseURL string
	// SessionToken authorizes requests (obtained from the gateway login).
	SessionToken string
	HTTP         *http.Client
}

func (c Config) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// ActivityReporter projects local agent activity to bnbloc-activity.
type ActivityReporter struct {
	cfg Config
}

// NewActivityReporter constructs an activity reporter client.
func NewActivityReporter(cfg Config) *ActivityReporter { return &ActivityReporter{cfg: cfg} }

// ReportSessionState posts a session-state snapshot. workspaceId is sent as the
// room scope with no translation.
func (a *ActivityReporter) ReportSessionState(ctx context.Context, in wire.ReportSessionStateInput) (wire.ReportSessionStateReply, error) {
	var reply wire.ReportSessionStateReply
	err := a.post(ctx, "/v1/rooms/"+in.WorkspaceID+"/agent-activity/session-state", in, &reply)
	return reply, err
}

// ReportMessages posts a batch of activity messages.
func (a *ActivityReporter) ReportMessages(ctx context.Context, in wire.ReportMessagesInput) (wire.ReportMessagesReply, error) {
	var reply wire.ReportMessagesReply
	err := a.post(ctx, "/v1/rooms/"+in.WorkspaceID+"/agent-activity/messages", in, &reply)
	return reply, err
}

func (a *ActivityReporter) post(ctx context.Context, path string, body, out any) error {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.GatewayBaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.SessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.SessionToken)
	}
	resp, err := a.cfg.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bnbloc activity %s: status %d: %s", path, resp.StatusCode, raw)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
