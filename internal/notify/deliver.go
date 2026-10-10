package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// maxResponseBytes bounds how much of a notifier's answer is read.
const maxResponseBytes = 64 * 1024

// DeliveryResult is a notifier's answer to one POST.
type DeliveryResult struct {
	Status  int
	Results []wire.NotificationRecipientResult
}

// Failed returns the recipients the notifier reported as failed, split into
// retryable and final failures.
func (r DeliveryResult) Failed() (retryable, final []wire.NotificationRecipientResult) {
	for _, res := range r.Results {
		switch {
		case res.OK:
		case res.Retryable:
			retryable = append(retryable, res)
		default:
			final = append(final, res)
		}
	}
	return retryable, final
}

// Deliver POSTs one signed notification body to the notifier at
// {baseUrl}/v1/notifications. Any 2xx is success (its optional
// {results:[…]} body is the per-recipient report); anything else —
// including a redirect, which is never followed — is an error carrying the
// status so the caller can retry. The endpoint client enforces the
// component's permissions.network (extgov.NetGuard).
func Deliver(ctx context.Context, ep endpointpkg.Endpoint, secret, id string, body []byte, now time.Time) (DeliveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.BaseURL+DeliveryPath, bytes.NewReader(body))
	if err != nil {
		return DeliveryResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bnbloc-notifications/1")
	req.Header.Set(wire.NotificationEventIDHeader, id)
	req.Header.Set(wire.NotificationSignatureHeader, Sign(secret, now, body))
	client := http.DefaultClient
	if ep.Client != nil {
		client = ep.Client
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return DeliveryResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	out := DeliveryResult{Status: resp.StatusCode}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("notifier answered HTTP %d", resp.StatusCode)
	}
	var parsed wire.NotificationDeliveryResponse
	if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &parsed) == nil {
		out.Results = parsed.Results
	}
	return out, nil
}

// TestPayload builds the signed body of a test send to one member.
func TestPayload(tenantID, userID, publicURL string, now time.Time) wire.NotificationPayload {
	return wire.NotificationPayload{
		ID: "ntf_test_" + randomToken(), Type: wire.NotificationTest, TenantID: tenantID,
		Recipients: []wire.NotificationRecipient{{UserID: userID}}, Actor: userID,
		Title: "Test notification", Text: "Notifications from bnbloc reach you through this notifier.",
		URL: RoomURL(publicURL, ""), AtMs: now.UnixMilli(),
	}
}

// SendTest delivers one test payload synchronously and reports the outcome
// (bypassing the outbox: the caller wants the answer now, and a test is
// never retried).
func SendTest(ctx context.Context, ep endpointpkg.Endpoint, secret string, p wire.NotificationPayload, now time.Time) wire.NotificationTestResult {
	body, err := json.Marshal(p)
	if err != nil {
		return wire.NotificationTestResult{EventID: p.ID, Error: err.Error()}
	}
	res, derr := Deliver(ctx, ep, secret, p.ID, body, now)
	out := wire.NotificationTestResult{Status: res.Status, EventID: p.ID, Results: res.Results}
	if derr != nil {
		out.Error = derr.Error()
		return out
	}
	retryable, final := res.Failed()
	if failed := append(retryable, final...); len(failed) > 0 {
		out.Error = failed[0].Error
		if out.Error == "" {
			out.Error = "the notifier reported a failed recipient"
		}
		return out
	}
	out.Delivered = true
	return out
}
