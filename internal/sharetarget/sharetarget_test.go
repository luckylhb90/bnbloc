package sharetarget

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/sharecard"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func sampleShare() Share {
	return Share{
		DeliveryID: "d-1",
		Dest:       []wire.ShareDest{{ID: "u1", Type: wire.ShareDestUser}},
		Card:       sharecard.Card{Kind: wire.ShareItemRoom, Title: "Plan", Summary: "s", URL: "https://b.example/#/room/r"},
		Sharer:     Sharer{Subject: "maya"},
		HostToken:  "host-token-secret",
	}
}

// The target receives the share signed with its credential, at the
// contract's path, with every field of the share.
func TestSendSignsAndShapesTheShare(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var got Share
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != SharePath || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if err := notify.Verify(r.Header.Get(wire.ShareSignatureHeader), "whsec_x", body, now, notify.SignatureTolerance); err != nil {
			t.Errorf("signature: %v", err)
		}
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := Send(t.Context(), endpointpkg.Endpoint{BaseURL: srv.URL}, "whsec_x", sampleShare(), now); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.DeliveryID != "d-1" || got.Dest[0].ID != "u1" || got.Card.Title != "Plan" || got.Sharer.Subject != "maya" || got.HostToken != "host-token-secret" {
		t.Fatalf("target received %+v", got)
	}
}

// A target that refuses says why; the member reads its reason.
func TestSendRefusalCarriesTheTargetsReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"reason":"the bot may not post there"}`))
	}))
	defer srv.Close()
	err := Send(t.Context(), endpointpkg.Endpoint{BaseURL: srv.URL}, "whsec_x", sampleShare(), time.Now())
	var ref *Refusal
	if !errors.As(err, &ref) || ref.Status != http.StatusBadGateway || ref.Reason != "the bot may not post there" {
		t.Fatalf("err = %v", err)
	}
}

// A redirect is a refusal, never followed; an unreachable target's error
// holds neither the URL nor the host token.
func TestSendNeverFollowsRedirectsNorLeaksTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/", http.StatusFound)
	}))
	err := Send(t.Context(), endpointpkg.Endpoint{BaseURL: srv.URL}, "whsec_x", sampleShare(), time.Now())
	var ref *Refusal
	if !errors.As(err, &ref) || ref.Status != http.StatusFound {
		t.Fatalf("redirect err = %v", err)
	}
	srv.Close()
	err = Send(t.Context(), endpointpkg.Endpoint{BaseURL: srv.URL}, "whsec_x", sampleShare(), time.Now())
	if err == nil || strings.Contains(err.Error(), "host-token-secret") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("unreachable err = %v", err)
	}
}
