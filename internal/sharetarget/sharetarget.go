// Package sharetarget is the share-target component kind: an endpoint
// component (internal/endpointpkg) the gateway calls when a member shares an
// item to a conversation of another app. Its contract is GET /discover and
// POST /v1/share, signed with the component's credential like a notifier's
// delivery (docs/architecture/share-targets.md).
package sharetarget

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/sharecard"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Paths of the share-target contract, appended to the endpoint's base URL.
const (
	DiscoverPath = "/discover"
	SharePath    = "/v1/share"
	// PagePath answers the share page: the document the member's client
	// opens (the host app's calls live in it).
	PagePath = "/v1/share/page"
)

// MaxPageBytes bounds the share page document.
const MaxPageBytes = 512 << 10

// sendTimeout bounds one share call: the target calls the other app's API
// inside it.
const sendTimeout = 10 * time.Second

// maxResponseBytes bounds how much of a target's answer is read.
const maxResponseBytes = 64 * 1024

// Kind is the share target's endpoint-component description.
var Kind = endpointpkg.Kind{
	Kind:         extreg.KindShareTarget,
	PackageKind:  pkgmeta.KindShareTargetPackageV1,
	Label:        "share-target",
	Contract:     pkgmeta.ShareTargetContractV1,
	DiscoverPath: DiscoverPath,
	Timeout:      sendTimeout,
}

// Installer is the kind's extpipe lifecycle: the shared endpoint install
// path (validate, network guard, discover probe).
func Installer() extpipe.KindInstaller { return Kind.Installer() }

// Share is the body of POST /v1/share. HostToken is the other app's
// credential for the member: a bearer in another system, used for this one
// call. Share has no String or log form on purpose; never format it.
type Share struct {
	DeliveryID string           `json:"deliveryId"`
	Dest       []wire.ShareDest `json:"dest"`
	Card       sharecard.Card   `json:"card"`
	Sharer     Sharer           `json:"sharer"`
	HostToken  string           `json:"hostToken"`
}

// Sharer is the member who shares, by their bnbloc subject.
type Sharer struct {
	Subject string `json:"subject"`
}

// Refusal is a target's own refusal: its answer was HTTP 4xx/5xx, with the
// member-readable reason it gave. Nothing was delivered.
type Refusal struct {
	Status int
	Reason string
}

func (r *Refusal) Error() string {
	if r.Reason != "" {
		return r.Reason
	}
	return fmt.Sprintf("the share target answered HTTP %d", r.Status)
}

// Send POSTs one signed share to the target at {baseUrl}/v1/share. A 2xx is
// accepted; a non-2xx is a *Refusal (the target's `reason` when it gave
// one); a transport failure is any other error. Redirects are never
// followed. The endpoint client enforces the component's permissions.network.
func Send(ctx context.Context, ep endpointpkg.Endpoint, secret string, s Share, now time.Time) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.BaseURL+SharePath, bytes.NewReader(body))
	if err != nil {
		return errors.New("build the share request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bnbloc-share/1")
	req.Header.Set(wire.ShareSignatureHeader, notify.Sign(secret, now, body))
	client := http.DefaultClient
	if ep.Client != nil {
		client = ep.Client
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		// The transport error names the URL, never the body; keep only the cause class.
		return errors.New("the share target is unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var answer struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &answer)
	return &Refusal{Status: resp.StatusCode, Reason: sharecard.Summary(strings.TrimSpace(answer.Reason))}
}
