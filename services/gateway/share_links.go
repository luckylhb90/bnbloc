package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/sharelink"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Short share links (docs/prd/2026-10-13-short-share-links.md): a link is
// `<public origin>/s/<token>` (internal/sharelink spells it). The gateway
// is the link's one URL owner: the create answer leaves with its URL
// (withShareURL), and GET /s/{token} sends the browser to the SPA's share
// page, which opens the link through the share routes as before.

// handleShareLink is GET /s/{token}: a well-formed token is sent to the
// share page (sharelink.PagePath), which resolves it and shows a dead link
// as "no longer available"; anything else is a 404. It resolves nothing
// itself, so it reveals nothing and spends no guess budget. It is not a
// site path, so with a sites origin it is served on the main origin only.
func (s *Service) handleShareLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	if !sharelink.Valid(token) {
		httpx.Error(w, "share not found", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, sharelink.PagePath(token), http.StatusFound)
}

// maxShareCreatedBody bounds the room service's create answer the proxy
// reads to add the URL (a link and its token).
const maxShareCreatedBody = 64 << 10

// withShareURL completes a link's create answer (wire.ShareCreated) with
// its URL on the gateway's public origin. Any other status passes as the
// room service wrote it.
func (s *Service) withShareURL(r *http.Request) func(*http.Response) error {
	origin := s.publicOrigin(r)
	return func(resp *http.Response) error {
		if resp.StatusCode != http.StatusCreated {
			return nil
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxShareCreatedBody+1))
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		var created wire.ShareCreated
		if len(raw) > maxShareCreatedBody || json.Unmarshal(raw, &created) != nil || !sharelink.Valid(created.Token) {
			return fmt.Errorf("share create answered an unreadable link")
		}
		created.URL = sharelink.URL(origin, created.Token)
		body, err := json.Marshal(created)
		if err != nil {
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		resp.Header.Set("Cache-Control", "no-store")
		return nil
	}
}
