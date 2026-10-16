package room

import (
	"log"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A share link's creator can copy it again while it works
// (docs/prd/2026-10-16-share-link-copy-again.md): its token is stored sealed
// (internal/sealbox) next to the hash the link is found by, and one
// creator-only route answers it. This file is the one owner of both halves.

// shareSealAD binds a sealed token to its tenant and share: a value copied to
// another row does not open.
func shareSealAD(tenantID, shareID string) string { return tenantID + "/" + shareID }

// sealShareToken is the token sealed for a new link, "" when the link cannot
// be copied again (no key, or sealing failed: the create goes on, the link is
// shown once as before).
func (s *Service) sealShareToken(r *http.Request, shareID, token string) string {
	if s.ShareBox == nil {
		return ""
	}
	tid, err := tenant.Require(r.Context())
	if err != nil {
		return ""
	}
	sealed, err := s.ShareBox.Seal([]byte(token), shareSealAD(tid, shareID))
	if err != nil {
		log.Printf("room: seal share %s token: %v", shareID, err)
		return ""
	}
	return sealed
}

// viewOwnShare is viewShare for a caller: the creator's own active link that
// was stored sealed is Copyable. Nobody else's view says so.
func viewOwnShare(sh store.ArtifactShare, nowMs int64, user string) wire.ShareView {
	v := viewShare(sh, nowMs)
	v.Copyable = v.Active && sh.CreatedBy == user && sh.TokenSealed != ""
	return v
}

// handleShareLink answers GET …/shares/{shareId}/link: the creator's active
// link, as wire.ShareLink{Token} (the gateway turns the token into its URL).
// Not the creator, a revoked, expired or never-sealed link, a key that no
// longer opens it: one 404, so the route does not say which. A room owner, an
// org admin or anyone else who may revoke a link may not read it: a link is a
// capability its creator handed out.
func (s *Service) handleShareLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.sharesReady(w) {
		return
	}
	art, ok := s.shareArtifact(w, r)
	if !ok {
		return
	}
	gone := func() { httpx.Error(w, "this link can't be shown", http.StatusNotFound) }
	sh, err := s.Artifacts.GetArtifactShare(r.Context(), r.PathValue("shareId"))
	if err != nil || sh.RoomID != art.RoomID || sh.ArtifactID != art.ArtifactID ||
		sh.CreatedBy != caller(r) || !sh.Active(s.Now()) || sh.TokenSealed == "" || s.ShareBox == nil {
		gone()
		return
	}
	tid, err := tenant.Require(r.Context())
	if err != nil {
		obs.ServerError(w, "read tenant failed", err)
		httpx.Error(w, "read tenant failed", http.StatusInternalServerError)
		return
	}
	token, err := s.ShareBox.Open(sh.TokenSealed, shareSealAD(tid, sh.ShareID))
	if err != nil {
		log.Printf("room: share %s token does not open (key changed?): %v", sh.ShareID, err)
		gone()
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.ShareLink{Token: string(token)})
}
