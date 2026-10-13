package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/sharelink"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Who may open a share link (docs/prd/2026-10-13-share-link-sign-in.md).
// The room service decides whether a link is alive (its secret, expiry,
// revocation, the organization's switch for "anyone" links, room and
// artifact present); shareAccess, below, is the ONE answer to whether this
// caller may open a live link (TestShareAccessSingleOwner):
//
//   - an "anyone" link: every caller;
//   - a "signed_in" link: any signed-in caller, of any organization (or
//     none: a session of an organization they do not belong to), never a
//     membership; an anonymous caller is refused with
//     wire.ShareSignInRequired;
//   - an "org" link: a signed-in member of the link's organization (not
//     necessarily of the room); an anonymous caller is refused with
//     wire.ShareSignInRequired, anyone else with wire.ShareOrgOnly.
//
// Whichever the mode, a share ticket's member is the app's member:<subject>
// participant in the link's room and organization: never an owner unless
// the room service counts them one (a member of the room), and nothing
// else of that organization.
//
// Every share route's credential is the {token} path segment, resolved by
// openShare: the link's own token (sharelink.Valid, an anonymous caller), or a
// share ticket (auth.KindShareTicket) the share page minted after sign-in
// (handleShareTicket), which rides in the path in the token's place exactly
// like a member site ticket, so a frame, a download link and an app's
// relative ~app/ calls carry it and the sites origin never sees a session.

// shareTicketTTL bounds a share ticket: as long as a member site ticket
// (a running app keeps the frame URL it was loaded from), never past the
// link's expiry. Every use re-resolves the link and re-runs shareAccess
// (site files reuse a passed check for publicSiteResolveTTL), so a long
// life grants nothing new.
const shareTicketTTL = siteTicketTTL

// maxShareTicketLen bounds a credential checked as a share ticket.
const maxShareTicketLen = 4 << 10

// shareCaller is who presents a link: anonymous (the link's token), a
// session at the ticket mint, or a share ticket's member.
type shareCaller struct {
	// subject is the signed-in member; "" for an anonymous caller.
	subject string
	// ticket is set for a share ticket: the share and organization it was
	// minted for, which must be the link's.
	ticket          bool
	shareID, tenant string
}

// openedShare is a live link its caller may open.
type openedShare struct {
	res    wire.ShareResolution
	caller shareCaller
}

// errShareSignIn refuses an anonymous caller of an org or signed_in link.
var errShareSignIn = errors.New("sign in to open this link")

// shareOrgOnlyError refuses a signed-in caller outside the link's
// organization.
type shareOrgOnlyError struct{ org string }

func (e *shareOrgOnlyError) Error() string {
	return "This link is only for members of " + e.org
}

// shareAccess is the one rule of who may open a live link (above). A
// ticket must have been minted for this link and its organization. The
// organization's membership is the tenant directory's (memberRole) and its
// suspension the one gate (tenantClosed); without a directory it fails
// closed.
func (s *Service) shareAccess(res wire.ShareResolution, c shareCaller) error {
	if c.ticket && (c.shareID != res.Share.ShareID || c.tenant != res.TenantID) {
		return errShareNotFound
	}
	switch res.Share.Access {
	case wire.ShareAccessAnyone:
		return nil
	case wire.ShareAccessSignedIn, wire.ShareAccessOrg:
	default:
		return errShareNotFound
	}
	if c.subject == "" {
		return errShareSignIn
	}
	if res.Share.Access == wire.ShareAccessSignedIn {
		// Anyone who could sign in: no organization is asked.
		return nil
	}
	role, err := s.memberRole(res.TenantID, c.subject)
	switch {
	case errors.Is(err, errNotTenantMember):
		return &shareOrgOnlyError{org: res.OrgName}
	case err != nil:
		return err
	case s.tenantClosed(res.TenantID, role):
		return errTenantSuspended
	}
	return nil
}

// openShare resolves a share route's credential (the {token} path value)
// to a live link its caller may open: the one lookup of every route a
// share link reaches. An IP whose guess budget is spent is refused first
// (errShareGuessing); a credential that is neither a plausible token nor a
// valid share ticket, and a dead link, spend it (errShareNotFound). With
// reuse, a recent opening of the same credential is served from the short
// public-site cache (site file requests only).
func (s *Service) openShare(r *http.Request, reuse bool) (openedShare, error) {
	cred := r.PathValue("token")
	var (
		req    wire.ShareResolveRequest
		caller shareCaller
	)
	switch {
	case sharelink.Valid(cred):
		req.Token = cred
	case len(cred) <= maxShareTicketLen:
		t, err := s.Signer.VerifyKind(cred, auth.KindShareTicket)
		if err != nil || t.Subject == "" || t.ID == "" || t.Tenant == "" {
			return openedShare{}, s.shareMiss(r)
		}
		req.ShareID = t.ID
		caller = shareCaller{subject: t.Subject, ticket: true, shareID: t.ID, tenant: t.Tenant}
	default:
		return openedShare{}, s.shareMiss(r)
	}
	s.bundleCaches()
	sum := sha256.Sum256([]byte(cred))
	cacheKey := hex.EncodeToString(sum[:])
	if reuse {
		if o, ok := s.publicSites.get(cacheKey); ok {
			return o, nil
		}
	}
	res, err := s.lookupShare(r, req)
	if err != nil {
		return openedShare{}, err
	}
	if err := s.shareAccess(res, caller); err != nil {
		return openedShare{}, err
	}
	o := openedShare{res: res, caller: caller}
	if reuse {
		s.publicSites.put(cacheKey, o)
	}
	return o, nil
}

// lookupShare resolves a link through the room service under the caller's
// guess budget: refused while it is spent, spent by a dead link.
func (s *Service) lookupShare(r *http.Request, req wire.ShareResolveRequest) (wire.ShareResolution, error) {
	if s.guessBlocked(shareGuessKey(s.clientIP(r))) {
		return wire.ShareResolution{}, errShareGuessing
	}
	res, err := s.resolveShare(r.Context(), req)
	if errors.Is(err, errShareNotFound) {
		s.guessAllow(shareGuessKey(s.clientIP(r)))
	}
	return res, err
}

// shareMiss spends the caller's guess budget on a credential that can name
// no link (errShareGuessing once it is spent).
func (s *Service) shareMiss(r *http.Request) error {
	key := shareGuessKey(s.clientIP(r))
	if s.guessBlocked(key) {
		return errShareGuessing
	}
	s.guessAllow(key)
	return errShareNotFound
}

func shareGuessKey(ip string) string { return "share-ip:" + ip }

// writeShareErr answers a failure to open a link: the one status mapping
// of the share routes.
func writeShareErr(w http.ResponseWriter, err error) {
	var orgOnly *shareOrgOnlyError
	switch {
	case errors.Is(err, errShareGuessing):
		httpx.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
	case errors.Is(err, errShareNotFound):
		w.Header().Set("Cache-Control", "no-store")
		httpx.Error(w, "share not found", http.StatusNotFound)
	case errors.Is(err, errShareSignIn):
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusUnauthorized, httpx.ErrorBody{Error: err.Error(), Code: wire.ShareSignInRequired})
	case errors.As(err, &orgOnly):
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusForbidden, httpx.ErrorBody{Error: orgOnly.Error(), Code: wire.ShareOrgOnly})
	case errors.Is(err, errTenantSuspended):
		httpx.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, errSwitchUnavailable):
		httpx.Unavailable(w, "organization membership")
	default:
		obs.ServerError(w, "resolve share failed", err)
		httpx.Error(w, "resolve share failed", http.StatusBadGateway)
	}
}

// maxShareTicketBody bounds the ticket mint's body (a link's token).
const maxShareTicketBody = 4 << 10

// handleShareTicket is POST /v1/share-tickets (wire.ShareTicketRequest):
// the share page's ticket for a link, minted for the signed-in caller once
// shareAccess admits them: for an "org" link a member of the link's
// organization, who need not be a member of its room; for a "signed_in"
// link anyone with a session. The token comes in the body, never the URL.
// The ticket (auth.KindShareTicket: the caller, the link's organization and
// share id) replaces the token in the link's routes.
func (s *Service) handleShareTicket(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	w.Header().Set("Cache-Control", "no-store")
	var in wire.ShareTicketRequest
	if !httpx.DecodeJSON(w, r, maxShareTicketBody, &in) {
		return
	}
	token := strings.TrimSpace(in.Token)
	if !sharelink.Valid(token) {
		writeShareErr(w, s.shareMiss(r))
		return
	}
	res, err := s.lookupShare(r, wire.ShareResolveRequest{Token: token})
	if err == nil {
		err = s.shareAccess(res, shareCaller{subject: claims.Subject})
	}
	if err != nil {
		writeShareErr(w, err)
		return
	}
	expires := s.nowTime().Add(shareTicketTTL)
	if end := res.Share.ExpiresAtMs; end > 0 && end < expires.UnixMilli() {
		expires = time.UnixMilli(end)
	}
	ticket := s.Signer.Issue(auth.Claims{
		Subject:       claims.Subject,
		Tenant:        res.TenantID,
		Kind:          auth.KindShareTicket,
		ID:            res.Share.ShareID,
		ExpiresAtUnix: expires.Unix(),
	})
	httpx.WriteJSON(w, http.StatusOK, wire.ShareTicket{Ticket: ticket, ExpiresAtMs: expires.UnixMilli()})
}
