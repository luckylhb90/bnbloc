package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/wire"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// Share link routes (PRD 2026-10-09 "Gateway"; access: PRD 2026-10-13):
// reads of one shared artifact version, never a session. Every request
// opens the link through openShare (share_access.go): the room service
// resolves it (POST /internal/v1/shares/resolve, 404 for every dead link)
// and shareAccess decides whether its caller — anonymous with the link's
// token, or a member with a share ticket in the token's place — may open
// it. The gateway reveals nothing of the room beyond the shared version.
// Reads are rate-limited per client IP, and a miss also spends the IP's
// guess budget (brute-force guard).

// publicShareView is GET /v1/public/shares/{token}.
type publicShareView struct {
	Artifact struct {
		Kind     string `json:"kind"`
		Title    string `json:"title"`
		FileName string `json:"fileName"`
	} `json:"artifact"`
	Version struct {
		Version     int    `json:"version"`
		MimeType    string `json:"mimeType"`
		SizeBytes   int64  `json:"sizeBytes"`
		CreatedAtMs int64  `json:"createdAtMs"`
		Summary     string `json:"summary,omitempty"`
	} `json:"version"`
	SharedBy struct {
		DisplayName string `json:"displayName"`
	} `json:"sharedBy"`
	ExpiresAtMs   int64  `json:"expiresAtMs"`
	FollowsLatest bool   `json:"followsLatest"`
	SiteURL       string `json:"siteUrl,omitempty"`
}

var errShareNotFound = errors.New("share not found")

// maxShareTokenLen bounds what is forwarded for resolution
// (bsh_<shareId>_<secret> is well under it).
const maxShareTokenLen = 256

// plausibleShareToken rejects what can never be a share token without a
// backend round trip (it still spends the guess budget).
func plausibleShareToken(tok string) bool {
	if !strings.HasPrefix(tok, "bsh_") || len(tok) > maxShareTokenLen {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func publicSiteBase(token string) string {
	return "/v1/public/shares/" + url.PathEscape(token) + "/site/"
}

// publicShare rate-limits the caller and opens the path's link
// (openShare). It answers the refusal itself (429, 404, 401, 403, 502) and
// reports ok=false. With reuse, a recent opening of the same credential is
// served from the short public-site cache (site file requests only).
func (s *Service) publicShare(w http.ResponseWriter, r *http.Request, reuse bool) (openedShare, bool) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !s.publicAllow(s.clientIP(r)) {
		httpx.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return openedShare{}, false
	}
	o, err := s.openShare(r, reuse)
	if err != nil {
		writeShareErr(w, err)
		return openedShare{}, false
	}
	return o, true
}

// errShareGuessing refuses a client IP whose guess budget token misses
// have spent.
var errShareGuessing = errors.New("rate limit exceeded")

// resolveShare asks the room service to resolve a link (by its token, or
// by a share ticket's share id; svcauth-signed as the gateway's own
// service subject). Every dead link is errShareNotFound.
func (s *Service) resolveShare(ctx context.Context, req wire.ShareResolveRequest) (wire.ShareResolution, error) {
	base := strings.TrimRight(s.Backends.Room, "/")
	if base == "" {
		return wire.ShareResolution{}, fmt.Errorf("room service not configured")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return wire.ShareResolution{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := callBackend(ctx, http.MethodPost, base+"/internal/v1/shares/resolve", body, gatewayServiceSubject, "")
	if err != nil {
		return wire.ShareResolution{}, fmt.Errorf("room service unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return wire.ShareResolution{}, errShareNotFound
	default:
		return wire.ShareResolution{}, fmt.Errorf("share resolve answered %d", resp.StatusCode)
	}
	var res wire.ShareResolution
	// Inline versions carry their content: bound the read by the inline cap
	// plus envelope.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&res); err != nil {
		return wire.ShareResolution{}, fmt.Errorf("decode share resolution: %w", err)
	}
	return res, nil
}

// countShareView records one view of a link its caller opened. Best
// effort: a lost count never fails the page.
func (s *Service) countShareView(ctx context.Context, shareID string) {
	base := strings.TrimRight(s.Backends.Room, "/")
	body, err := json.Marshal(wire.ShareViewCount{ShareID: shareID})
	if base == "" || err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := callBackend(ctx, http.MethodPost, base+"/internal/v1/shares/view", body, gatewayServiceSubject, "")
	if err != nil {
		slog.Warn("bnbloc gateway: share view count failed", "event", "bnbloc.share.view_count_failed", "error", err)
		return
	}
	resp.Body.Close()
}

// handlePublicShare answers GET /v1/public/shares/{token}: what the link's
// caller may see of it (counts a view). The token is the link's credential
// (its token, or a share ticket); a site's siteUrl carries the same one.
func (s *Service) handlePublicShare(w http.ResponseWriter, r *http.Request) {
	o, ok := s.publicShare(w, r, false)
	if !ok {
		return
	}
	res := o.res
	s.countShareView(r.Context(), res.Share.ShareID)
	var out publicShareView
	out.Artifact.Kind = res.Artifact.Kind
	out.Artifact.Title = res.Artifact.Title
	out.Artifact.FileName = res.Artifact.FileName
	out.Version.Version = res.Version.Version
	out.Version.MimeType = publicMIME(res)
	out.Version.SizeBytes = res.Version.SizeBytes
	out.Version.CreatedAtMs = res.Version.CreatedAtMs
	out.Version.Summary = res.Version.Summary
	out.SharedBy.DisplayName = res.SharedBy.DisplayName
	out.ExpiresAtMs = res.Share.ExpiresAtMs
	out.FollowsLatest = res.Share.Version == 0
	if _, isSite := siteVersionOf(res.Version.Storage, res.Version.MimeType, res.Version.BlobKey, res.Version.ContentHash); isSite && res.Artifact.Kind == store.ArtifactKindSite {
		out.SiteURL = s.siteURL(publicSiteBase(r.PathValue("token")))
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, out)
}

// publicMIME is the version's media type (an inline version without one is
// typed from its artifact, as the room's content route does).
func publicMIME(res wire.ShareResolution) string {
	if mt := strings.TrimSpace(res.Version.MimeType); mt != "" {
		return mt
	}
	if res.Version.Storage == store.ArtifactStorageBlob {
		return "application/octet-stream"
	}
	return store.ArtifactInlineMIME(store.Artifact{Kind: res.Artifact.Kind, Title: res.Artifact.Title, FileName: res.Artifact.FileName})
}

// handlePublicShareContent answers GET /v1/public/shares/{token}/content:
// the version's bytes, sandboxed. ?download=1 makes it an attachment and,
// for a blob version with direct downloads on, a 302 to a presigned URL.
func (s *Service) handlePublicShareContent(w http.ResponseWriter, r *http.Request) {
	o, ok := s.publicShare(w, r, false)
	if !ok {
		return
	}
	res := o.res
	download := r.URL.Query().Get("download") == "1"
	mimeType := publicMIME(res)
	fileName := store.ArtifactDownloadName(res.Artifact.Kind, res.Artifact.FileName, res.Artifact.Title)
	if download && res.Version.Storage == store.ArtifactStorageBlob {
		if s.redirectToPresignedBlob(w, r, res.Version.BlobKey, fileName, mimeType) {
			return
		}
	}
	var body io.Reader
	size := int64(-1)
	if res.Version.Storage == store.ArtifactStorageBlob {
		rc, n, err := s.blobs().OpenBlob(r.Context(), res.Version.BlobKey)
		if err != nil {
			writeBlobStoreErr(w, err)
			return
		}
		defer rc.Close()
		body, size = rc, n
	} else {
		body, size = strings.NewReader(res.Version.Content), int64(len(res.Version.Content))
	}
	h := w.Header()
	h.Set("Content-Type", servedContentType(mimeType))
	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	h.Set("Content-Disposition", httpx.ContentDisposition(disposition, fileName))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		if size >= 0 {
			h.Set("Content-Length", strconv.FormatInt(size, 10))
		}
		return
	}
	streamBlob(w, body, size)
}

// handlePublicShareSite answers GET /v1/public/shares/{token}/site/{path...}
// with a file of a shared site version (the site file rules).
func (s *Service) handlePublicShareSite(w http.ResponseWriter, r *http.Request) {
	o, ok := s.publicShare(w, r, true)
	if !ok {
		return
	}
	res := o.res
	sv, isSite := siteVersionOf(res.Version.Storage, res.Version.MimeType, res.Version.BlobKey, res.Version.ContentHash)
	if !isSite || res.Artifact.Kind != store.ArtifactKindSite {
		http.NotFound(w, r)
		return
	}
	s.serveSite(w, r, sv, publicSiteBase(r.PathValue("token")))
}

// servedContentType adds the utf-8 charset to text types.
func servedContentType(mimeType string) string {
	mt := store.MediaType(mimeType)
	if mt == "" {
		return "application/octet-stream"
	}
	if strings.HasPrefix(mt, "text/") || mt == "application/json" {
		return mt + "; charset=utf-8"
	}
	return mt
}
