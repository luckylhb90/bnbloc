package gateway

import (
	"errors"
	"net/http"
	"path"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/filematch"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/previewerruntime"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Previewers on a share (docs/prd/2026-10-27-shared-file-viewers.md): a shared
// file renders in its viewer for a recipient who has installed nothing. The
// previewers a link may use are the ones its CREATOR can use in the link's
// organization (what the sharer sees is what the recipient sees), listed here
// with frame tickets the existing frame route (previewers.go) already serves
// with no session. That route re-reads the ticket subject's installed state on
// every request, and the ticket's subject is the creator, so a creator who
// uninstalls a previewer, or leaves the organization, takes it from the link
// at once. The route's rules are unchanged; only the minting of the tickets is
// new, through the one minter (previewerCatalog).

// Domain series (Service.Metrics), one label each: outcome, from a fixed set.
const (
	metricSharePreviewers = "share_previewer_lists_total" // listed, empty, creator-gone, closed, unverified
	metricShareCovers     = "share_covers_total"          // served, none
)

// DescribeMetrics sets the help text of the gateway's domain series.
func DescribeMetrics(m *obs.Metrics) {
	m.Describe(metricSharePreviewers, "Previewer lists answered for a share link by outcome (listed, empty, creator-gone, closed, unverified).")
	m.Describe(metricShareCovers, "Share cover requests by outcome (served, none).")
}

func (s *Service) shareMetric(name, outcome string) {
	s.Metrics.CounterInc(name, obs.L("outcome", outcome))
}

// sharePreviewerList is GET /v1/public/shares/{token}/previewers.
type sharePreviewerList struct {
	Components []sharePreviewerEntry `json:"components"`
}

// sharePreviewerEntry is the least of a member catalog entry the web's
// previewer parser reads: no permissions, settings, publisher, artifacts,
// counts or hooks, and no bundle address (the desktop's, which a share page
// never is). A visitor learns of the previewers that claim this file, nothing
// of the rest of the organization's components.
type sharePreviewerEntry struct {
	Kind        string                         `json:"kind"`
	Name        string                         `json:"name"`
	Version     string                         `json:"version"`
	Description string                         `json:"description,omitempty"`
	Installed   bool                           `json:"installed"`
	Previewer   *wire.MemberComponentPreviewer `json:"previewer"`
}

// handlePublicSharePreviewers answers GET /v1/public/shares/{token}/previewers:
// the previewers that claim the shared file and that the link's creator may
// use, each with a frame base. Same access as /content (publicShare); it never
// counts a view. Nothing to offer is an empty list, never an error: the page
// then shows what it showed before.
func (s *Service) handlePublicSharePreviewers(w http.ResponseWriter, r *http.Request) {
	o, ok := s.publicShare(w, r, false)
	if !ok {
		return
	}
	entries, outcome, err := s.sharePreviewers(r, o.res)
	if err != nil {
		obs.ServerError(w, "load share previewers failed", err)
		httpx.Error(w, "load share previewers failed", http.StatusInternalServerError)
		return
	}
	s.shareMetric(metricSharePreviewers, outcome)
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, sharePreviewerList{Components: entries})
}

// sharePreviewers lists the previewers for a share and says why when none.
func (s *Service) sharePreviewers(r *http.Request, res wire.ShareResolution) ([]sharePreviewerEntry, string, error) {
	none := []sharePreviewerEntry{}
	creator := res.Share.CreatedBy
	// The creator must still belong to the link's organization, and the
	// organization be open: the reads shareAccess makes for an org link. With
	// no tenant directory it fails closed, as there.
	role, err := s.memberRole(res.TenantID, creator)
	switch {
	case errors.Is(err, errNotTenantMember):
		return none, "creator-gone", nil
	case err != nil:
		return none, "unverified", nil
	case s.tenantClosed(res.TenantID, role):
		return none, "closed", nil
	}
	// The viewer is the creator in the link's organization; the ticket carries
	// no more than a bundle read, so the lowest role stands in for theirs.
	claims := auth.Claims{Subject: creator, Tenant: res.TenantID, Role: string(rbac.RoleViewer)}
	view, err := s.loadMemberComponents(r.Context(), claims)
	if err != nil {
		return nil, "", err
	}
	name, mimeType := shareFileName(res), publicMIME(res)
	entries := []sharePreviewerEntry{}
	for _, rec := range view.list {
		if rec.Kind != string(extreg.KindPreviewer) || !view.visibility(rec).Installed {
			continue
		}
		cfg, err := previewerruntime.ValidateRecord(rec)
		if err != nil || cfg.Previewer == nil || !filematch.Claims(cfg.Previewer.Match, name, mimeType) {
			// A record that does not validate is the operator's to fix; it
			// never fails a stranger's page.
			continue
		}
		block := s.previewerCatalog(claims, rec, cfg)
		if block == nil {
			continue
		}
		block.BundleURL = ""
		entries = append(entries, sharePreviewerEntry{
			Kind: rec.Kind, Name: rec.Name, Version: rec.Version, Description: cfg.Description, Installed: true, Previewer: block,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	if len(entries) == 0 {
		return entries, "empty", nil
	}
	return entries, "listed", nil
}

// shareFileName is the name that types the shared file for matching: the
// artifact's file name when it has an extension, else its title, as the web
// reads one (artifactExtension).
func shareFileName(res wire.ShareResolution) string {
	if path.Ext(res.Artifact.FileName) != "" {
		return res.Artifact.FileName
	}
	return res.Artifact.Title
}
