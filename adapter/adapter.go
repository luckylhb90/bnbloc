// Package adapter is the daemon-side host adapter for bnbloc. It contains the
// HTTP clients a member's local agent runtime wires into its own ports:
//
//   - ActivityReporter posts session-state, messages, and the submit-provenance
//     admission barrier to bnbloc-activity. A runtime integration adapts this to
//     the durable activity-reporter hook its daemon config requires; it edits
//     no runtime core source.
//   - SharedAgentAccessClient calls bnbloc-shareagent for every shared-agent
//     lifecycle action and fails closed on denial. A runtime integration installs
//     it as its shared-agent access controller/auditor hooks.
//
// roomId is always the daemon's workspaceId (roomId ≡ workspaceId).
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// ReportSubmitProvenance drives the atomic admission barrier. It returns only
// after bnbloc-activity acks that the client-submit id is durably queryable.
func (a *ActivityReporter) ReportSubmitProvenance(ctx context.Context, in wire.SubmitProvenanceInput) error {
	return a.post(ctx, "/v1/rooms/"+in.WorkspaceID+"/agent-activity/submit-provenance", in, nil)
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

// ErrSharedAgentDenied is returned when bnbloc-shareagent denies an action. It
// carries the stable deny reason so the daemon fails closed with a UI reason.
type ErrSharedAgentDenied struct {
	Reason string
}

func (e *ErrSharedAgentDenied) Error() string {
	return "shared agent access denied: " + e.Reason
}

// SharedAgentAccessClient calls bnbloc-shareagent for shared-agent lifecycle
// actions. It never receives or forwards an owner credential.
type SharedAgentAccessClient struct {
	cfg Config
}

// NewSharedAgentAccessClient constructs a shared-agent access client.
func NewSharedAgentAccessClient(cfg Config) *SharedAgentAccessClient {
	return &SharedAgentAccessClient{cfg: cfg}
}

// TurnRequest is the daemon-side shared-agent turn request. It carries the
// selected plan/model/capability, never a credential.
type TurnRequest struct {
	RoomID         string  `json:"-"`
	TargetID       string  `json:"-"`
	GrantID        string  `json:"grantId"`
	ModelPlanID    string  `json:"modelPlanId"`
	Model          string  `json:"model"`
	TurnCapability string  `json:"turnCapability"`
	Cost           float64 `json:"cost"`
	PayloadRef     any     `json:"payloadRef"`
}

// Turn authorizes and runs one shared-agent turn through bnbloc. On denial it
// returns *ErrSharedAgentDenied (fail closed); on allow it returns the opaque
// proxied result and the concurrency lease id.
func (c *SharedAgentAccessClient) Turn(ctx context.Context, req TurnRequest) (result any, leaseID string, err error) {
	path := "/v1/rooms/" + req.RoomID + "/shared-agents/" + req.TargetID + "/turn"
	buf, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.GatewayBaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.SessionToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.SessionToken)
	}
	resp, err := c.cfg.httpClient().Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		var deny struct {
			DenyReason string `json:"denyReason"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&deny)
		return nil, "", &ErrSharedAgentDenied{Reason: deny.DenyReason}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", errors.New("bnbloc shareagent turn: unexpected status")
	}
	var ok struct {
		LeaseID string `json:"leaseId"`
		Result  any    `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ok)
	return ok.Result, ok.LeaseID, nil
}
