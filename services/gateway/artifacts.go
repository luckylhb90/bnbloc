package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// maxArtifactUploadBytes bounds a super-admin artifact upload at the gateway
// edge. It mirrors the realtime blob cap; the gateway streams the body straight
// through (never buffered), so this only guards against an unbounded request.
const maxArtifactUploadBytes = 256 * 1024 * 1024

type artifactCapability struct {
	Enabled bool   `json:"enabled"`
	Backend string `json:"backend"`
}

func (s *Service) handleArtifactCapability(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	base := strings.TrimRight(s.Backends.Realtime, "/")
	if base == "" {
		httpx.Unavailable(w, "artifact store")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	resp, err := callBackend(ctx, http.MethodGet, base+"/internal/v1/blobs", nil, gatewayServiceSubject, claims.Tenant)
	if err != nil {
		obs.ServerError(w, "artifact store unreachable", err)
		httpx.Error(w, "artifact store unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeBlobStoreErr(w, upstreamStatusError(resp))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, artifactCapability{Enabled: true, Backend: "realtime-blob"})
}

// handleArtifactUpload streams a super-admin-uploaded binary through to
// realtime's generic blob store (the only service with S3 credentials) and
// returns the content-addressed key + sha256. The gateway holds no S3 access
// and never buffers the bytes: it is a signed streaming reverse-proxy. Used by
// `bnbloc cores publish-sidecar` to stage each platform's sidecar binary before
// assembling the signed core package.
func (s *Service) handleArtifactUpload(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	base := strings.TrimRight(s.Backends.Realtime, "/")
	if base == "" {
		httpx.Unavailable(w, "artifact store")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxArtifactUploadBytes)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	req, err := svcauth.NewRequest(ctx, http.MethodPost, base+"/internal/v1/blobs", r.Body, gatewayServiceSubject, claims.Tenant)
	if err != nil {
		obs.ServerError(w, "artifact upload failed", err)
		httpx.Error(w, "artifact upload failed", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = r.ContentLength
	// The upload answers only after realtime has stored the whole blob, so
	// the exchange keeps its overall deadline instead of a header timeout.
	s.proxyArtifact(w, req, backendStreamHTTP)
}

// handleArtifactDownload streams a stored artifact back from realtime by its
// content-addressed key. The blob store is shared (it also holds other
// uploads), so a key is served only to a caller entitled to it
// (artifactEntitled); everything else is a 404, indistinguishable from a
// missing key. The sha256 in the key plus the client-side pin against the
// signed package remains the integrity gate.
func (s *Service) handleArtifactDownload(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	key := strings.TrimPrefix(r.PathValue("key"), "/")
	// A content bundle named with ?room= or ?job=&room= resolves its actor
	// exactly as the Library route that listed it (contentGrant). A refused
	// grant is a 404 like any key the caller is not entitled to.
	actor, err := s.contentGrant(r, claims)
	var refused *grantError
	if errors.As(err, &refused) && refused.status < 500 {
		httpx.Error(w, "artifact not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeGrantErr(w, err)
		return
	}
	entitled, err := s.artifactEntitled(r.Context(), actor, key)
	if err != nil {
		obs.ServerError(w, "load artifact entitlement failed", err)
		httpx.Error(w, "load artifact entitlement failed", http.StatusInternalServerError)
		return
	}
	if !entitled {
		httpx.Error(w, "artifact not found", http.StatusNotFound)
		return
	}
	// A built-in content bundle ships inside the binary, never in the blob
	// store.
	if raw, ok := contentbuiltin.Bundle(key); ok {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		streamBlob(w, bytes.NewReader(raw), int64(len(raw)))
		return
	}
	if strings.TrimSpace(s.Backends.Realtime) == "" {
		httpx.Unavailable(w, "artifact store")
		return
	}
	// ?direct=1 (the desktop's component downloader): a presigned S3 URL
	// when realtime hands them out, so the bytes skip the gateway.
	if r.URL.Query().Get("direct") == "1" {
		if s.redirectToPresignedBlob(w, r, key, "", "application/octet-stream") {
			return
		}
	}
	// No deadline on the exchange: a large artifact over a slow link may take
	// longer than any fixed bound. The client's own disconnect cancels it.
	rc, size, err := s.blobs().OpenBlob(r.Context(), key)
	if err != nil {
		writeBlobStoreErr(w, err)
		return
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Content-Type-Options", "nosniff")
	streamBlob(w, rc, size)
}

// blobs is the gateway's client onto realtime's blob store (the only blob
// owner). Reads assert gatewayServiceSubject: the gateway has already
// authorized them.
func (s *Service) blobs() *realtime.HandoffClient {
	s.blobClientOnce.Do(func() { s.blobClient = realtime.NewHandoffClient(s.Backends.Realtime, "gateway") })
	return s.blobClient
}

// redirectToPresignedBlob answers 302 to a presigned download URL of key
// when realtime hands them out (BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS on
// an S3 store) and reports true. With direct downloads disabled it writes
// nothing and reports false: the caller streams. Any other failure is
// answered (502) and reported true.
func (s *Service) redirectToPresignedBlob(w http.ResponseWriter, r *http.Request, key, fileName, contentType string) bool {
	p, err := s.blobs().PresignBlob(r.Context(), key, fileName, contentType)
	if errors.Is(err, realtime.ErrDirectDownloadsDisabled) {
		return false
	}
	if err != nil {
		obs.ServerError(w, "blob presign failed", err)
		httpx.Error(w, "blob presign failed", http.StatusBadGateway)
		return true
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	// The redirecting URL may carry a capability (share token, ticket):
	// never hand it to the storage host as a Referer.
	h.Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, p.URL, http.StatusFound)
	return true
}

// writeBlobStoreErr maps a failed blob read: realtime's own refusal keeps
// its status (404 for a key it does not hold), anything else is a 502.
func writeBlobStoreErr(w http.ResponseWriter, err error) {
	var se *realtime.StatusError
	if errors.As(err, &se) {
		httpx.WriteStatus(w, se.Status, "artifact store failed via realtime: "+strings.TrimSpace(se.Body), err)
		return
	}
	obs.ServerError(w, "artifact store unreachable", err)
	httpx.Error(w, "artifact store unreachable", http.StatusBadGateway)
}

// streamBlob copies a blob body to the client, declaring its length when
// known so a cut transfer is distinguishable from a complete one.
func streamBlob(w http.ResponseWriter, body io.Reader, size int64) {
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		// The upstream read failed mid-body. Abort the connection so the
		// client sees a broken transfer, never a short response that looks
		// complete.
		panic(http.ErrAbortHandler)
	}
}

// artifactEntitled reports whether the caller may download the blob key. A
// super-admin may fetch any staged key (they upload them). Everyone else may
// fetch only keys referenced by something already published to them:
//   - the artifacts of a component that passes their rollout + tenant gate
//     (the same gate /v1/components and /v1/cores/{name}/artifact apply);
//   - the files of a published desktop release (/v1/app/releases) and the
//     current desktop update (/v1/app/update), which every member may read;
//   - the bundle of a content component version visible to them, used by a
//     room they are in (?room=), or in effect for a job their bridge runs
//     (?job=&room=; contentBundleEntitled over the contentGrant actor).
func (s *Service) artifactEntitled(ctx context.Context, actor contentActor, key string) (bool, error) {
	if key == "" {
		return false, nil
	}
	claims := actor.Claims
	if rbac.Can(claims.Role, rbac.PermManagePlatform) {
		return true, nil
	}
	view, err := s.loadMemberComponents(ctx, claims)
	if err != nil {
		return false, err
	}
	for _, rec := range view.records {
		if !componentKindVisibleInMemberCatalog(componentKindForName(rec)) || !view.visibility(rec).Installed {
			continue
		}
		cfg, err := extpipe.DecodeConfig(rec)
		if err != nil {
			return false, err
		}
		for _, art := range cfg.Artifacts {
			if artifactKeyIs(art.URL, key) {
				return true, nil
			}
		}
	}
	hub, err := loadReleaseHub(ctx, s.PlatformSettings)
	if err != nil {
		return false, err
	}
	for _, rel := range hub.Releases {
		for _, f := range rel.Files {
			if artifactKeyIs(f.URL, key) {
				return true, nil
			}
		}
	}
	update, err := loadUpdateConfig(ctx, s.PlatformSettings)
	if err != nil {
		return false, err
	}
	for _, p := range update.Release.Platforms {
		if artifactKeyIs(p.URL, key) {
			return true, nil
		}
	}
	return s.contentBundleEntitled(ctx, actor, key)
}

// artifactKeyIs reports whether a published artifact URL names key
// (pkgmeta.ArtifactKeyOf, any origin).
func artifactKeyIs(raw, key string) bool {
	k, ok := pkgmeta.ArtifactKeyOf(raw)
	return ok && k == key
}

// proxyArtifact performs the signed upload request to realtime and copies
// the status, content-type, and body back to the caller (a flat-memory
// pass-through).
func (s *Service) proxyArtifact(w http.ResponseWriter, req *http.Request, client *http.Client) {
	resp, err := client.Do(req)
	if err != nil {
		obs.ServerError(w, "artifact store unreachable", err)
		httpx.Error(w, "artifact store unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeBlobStoreErr(w, upstreamStatusError(resp))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// blobOpenErr is the one description of a failed blob open on the
// gateway's own behalf (a package probe, a previewer or site bundle read).
func blobOpenErr(err error) error {
	var se *realtime.StatusError
	switch {
	case errors.As(err, &se) && se.Status == http.StatusNotFound:
		return fmt.Errorf("artifact not found in gateway artifact store")
	case errors.As(err, &se):
		return fmt.Errorf("artifact store read failed with status %d", se.Status)
	default:
		return fmt.Errorf("artifact store unreachable")
	}
}

// upstreamStatusError reads a failed realtime blob answer into the
// StatusError writeBlobStoreErr maps (the same shape the blob client
// returns), so every blob failure is answered in one place.
func upstreamStatusError(resp *http.Response) *realtime.StatusError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &realtime.StatusError{Status: resp.StatusCode, Body: httpx.ErrorText(raw)}
}

func (s *Service) probeUploadedArtifacts(ctx context.Context, pkg pkgmeta.Package, artifactBaseURL string) error {
	if strings.TrimSpace(s.Backends.Realtime) == "" {
		return fmt.Errorf("artifact store not configured")
	}
	publicArtifactBaseURL, err := pkgmeta.ParseGatewayArtifactBaseURL(artifactBaseURL)
	if err != nil {
		return err
	}
	for platform, artifact := range pkg.Artifacts {
		key, err := pkgmeta.GatewayArtifactKey(artifact.URL, publicArtifactBaseURL)
		if err != nil {
			return fmt.Errorf("artifacts.%s.url: %w", platform, err)
		}
		if err := s.probeUploadedArtifact(ctx, key, artifact.SHA256); err != nil {
			return fmt.Errorf("artifacts.%s.url: %w", platform, err)
		}
	}
	return nil
}

func (s *Service) probeUploadedArtifact(ctx context.Context, key, wantSHA string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rc, _, err := s.blobs().OpenBlob(probeCtx, key)
	if err != nil {
		return blobOpenErr(err)
	}
	defer rc.Close()
	hash, _, err := copySHA256(rc)
	if err != nil {
		return fmt.Errorf("artifact hash failed")
	}
	if hash != wantSHA {
		return fmt.Errorf("artifact bytes do not match sha256")
	}
	return nil
}

func copySHA256(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
