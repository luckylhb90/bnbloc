package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/mcptool"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The complete room capture (docs/prd/2026-10-20-complete-room-capture.md
// §2.2, slice 1): beside its setup a room package carries the room's
// instructions, the apps made in it and the files its saver ticks, and
// requires the MCP tools its runs had. Capture offers them
// (component-draft: draftDeliverables, draftTools), a save takes the ticked
// ones from the room as the saver reads them (roomIncluded, a content
// save's include), and install restores them (installInstructions,
// installMadeApps, installFiles) inside installRoom's one cleanup.

// Bounds of a draft's offer.
const (
	// maxDraftFiles bounds the room files a draft offers (as the room
	// lists them); the rest are said in Left.
	maxDraftFiles = 50
	// draftArtifactsPage is the deliverables a draft reads (one page).
	draftArtifactsPage = 200
	// draftRunsPage is how many of the room's latest runs a draft reads
	// for the tools their sessions had.
	draftRunsPage = 100
)

// draftDeliverables captures the room's deliverables: its apps copied in
// from works (Apps, by "name@^major"), the apps and sites made in it
// (MadeApps) and its files (Files), each with its current version's size.
// What a draft does not offer is said in Left.
func (s *Service) draftDeliverables(ctx context.Context, claims auth.Claims, roomID string, d *wire.RoomComponentDraft) error {
	var list wire.ArtifactList
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/artifacts?limit="+strconv.Itoa(draftArtifactsPage), "", nil,
		claims.Subject, claims.Tenant, &list); err != nil {
		return err
	}
	seen, more := map[string]bool{}, 0
	for _, a := range list.Artifacts {
		if a.CurrentVersion < 1 {
			continue
		}
		site := a.Kind == store.ArtifactKindSite
		if site {
			part, err := s.workOfCopy(ctx, claims, a.ArtifactID)
			if err != nil {
				return err
			}
			if part != nil {
				if !seen[part.Name] {
					seen[part.Name] = true
					d.Apps = append(d.Apps, *part)
				}
				continue
			}
		} else if len(d.Files) == maxDraftFiles {
			more++
			continue
		}
		_, v, err := s.deliverableVersion(ctx, claims, roomID, a.ArtifactID)
		if errors.Is(err, errSiteNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		item := wire.RoomDraftDeliverable{ArtifactID: a.ArtifactID, Title: workTitle(a), FileName: a.FileName, MimeType: firstNonEmpty(v.MimeType, a.MimeType), SizeBytes: v.SizeBytes}
		if site {
			d.MadeApps = append(d.MadeApps, item)
		} else {
			d.Files = append(d.Files, item)
		}
	}
	if more > 0 {
		d.Left = append(d.Left, fmt.Sprintf("%s past the %d a package is offered", plural(more, "more room file"), maxDraftFiles))
	}
	if list.NextCursor != "" {
		d.Left = append(d.Left, fmt.Sprintf("deliverables past the room's latest %d", draftArtifactsPage))
	}
	return nil
}

// workOfCopy is the work a room's site was copied from ("Use this app":
// its app-copy use names the work) as a part a package names, when the
// editor in claims may see it; nil for a site made in the room.
func (s *Service) workOfCopy(ctx context.Context, claims auth.Claims, artifactID string) (*wire.RoomDraftPart, error) {
	uses, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseAppCopy, artifactID)
	if err != nil {
		return nil, err
	}
	var part *wire.RoomDraftPart
	for _, u := range uses {
		c, err := s.ContentComponents.GetContentComponent(ctx, u.ComponentID)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if c.Kind == contentpkg.KindWork && extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Visible {
			version := firstNonEmpty(u.PinnedVersion, c.LatestVersion)
			part = &wire.RoomDraftPart{ID: c.ID, Name: c.Name, DisplayName: c.DisplayName, Version: version, Ref: contentpkg.MajorRef(c.Name, version)}
		}
	}
	return part, nil
}

// draftTools captures the MCP tools the room's latest runs had (the run
// records', read as the editor through activity's room route), each one
// the editor may use with the permission a package requiring it declares;
// one they may not use is said in Left, as is a run record that could not
// be read (the rest of the draft stands).
func (s *Service) draftTools(ctx context.Context, claims auth.Claims, roomID string, d *wire.RoomComponentDraft) error {
	if s.Backends.Activity == "" {
		return nil
	}
	var runs wire.AgentRunsReply
	if err := backendDo(ctx, backendHTTP, s.Backends.Activity, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/jobs?limit="+strconv.Itoa(draftRunsPage), "", nil,
		claims.Subject, claims.Tenant, &runs); err != nil {
		log.Printf("gateway: room %s draft: read its runs: %v", roomID, err)
		d.Left = append(d.Left, "the MCP tools its runs had (the run records could not be read: try again)")
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, r := range runs.Runs {
		for _, t := range r.Tools {
			if !seen[t] {
				seen[t] = true
				names = append(names, t)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	cands, err := s.mcpCandidates(ctx, claims)
	if err != nil {
		return err
	}
	for _, name := range names {
		c, ok := cands[name]
		if !ok || !c.Visibility.Visible || c.Config.MCP == nil {
			d.Left = append(d.Left, fmt.Sprintf("MCP tool %s (its runs had it; it is not one you can use, so a package cannot require it)", name))
			continue
		}
		info := contentpkg.MCPToolInfo{Transport: mcptool.Transport(c.Config), Host: mcptool.Host(c.Config)}
		d.Tools = append(d.Tools, wire.RoomDraftTool{Name: name, Permission: contentpkg.MCPPermission(info)})
	}
	return nil
}

// roomIncluded takes what a room package's save includes (inc) from its
// room into f.Captured: each app made in it (a site's current version)
// and each room file, read as the saver reads them, who must be the room's
// owner or an admin (a job's grant: its own room only), within the
// package's bounds before anything more is read. It answers the refusal
// and reports whether the save goes on (true when nothing is included).
func (s *Service) roomIncluded(w http.ResponseWriter, ctx context.Context, actor contentActor, inc *wire.RoomInclude, f *contentpkg.Form) bool {
	if inc == nil {
		return true
	}
	if spec, ok := contentpkg.Lookup(f.Kind); !ok || spec.Kind != contentpkg.KindRoom {
		httpx.Error(w, "include belongs to a room component's save", http.StatusBadRequest)
		return false
	}
	roomID := strings.TrimSpace(inc.RoomID)
	if actor.Room != "" && actor.Room != roomID {
		httpx.Error(w, "a job's save includes its own room's apps and files only", http.StatusForbidden)
		return false
	}
	editor, ok := s.roomEditorClaims(actor.Claims, roomID)
	if !ok {
		httpx.Error(w, "only the room's owner or admins include its apps and files", http.StatusForbidden)
		return false
	}
	switch {
	case len(inc.Apps) > contentpkg.MaxRoomApps:
		httpx.Error(w, fmt.Sprintf("at most %d apps", contentpkg.MaxRoomApps), http.StatusBadRequest)
		return false
	case len(inc.Files) > wire.MaxRoomFiles:
		httpx.Error(w, fmt.Sprintf("at most %d files", wire.MaxRoomFiles), http.StatusBadRequest)
		return false
	}
	capture := &contentpkg.RoomCapture{}
	var total int64
	for i, id := range append(append([]string(nil), inc.Apps...), inc.Files...) {
		d, err := s.currentDeliverable(ctx, editor, roomID, strings.TrimSpace(id))
		if !writeDeliverableErr(w, err) {
			return false
		}
		app := i < len(inc.Apps)
		switch {
		case app && d.Snapshot.Site == nil:
			httpx.Error(w, fmt.Sprintf("%s is a file, not an app or site: include it as a file", workTitle(d.Artifact)), http.StatusBadRequest)
			return false
		case !app && d.Snapshot.Site != nil:
			httpx.Error(w, fmt.Sprintf("%s is a site: include it as an app", workTitle(d.Artifact)), http.StatusBadRequest)
			return false
		case app:
			total += int64(d.Snapshot.Site.Size())
			capture.Apps = append(capture.Apps, contentpkg.CapturedApp{Title: workTitle(d.Artifact), Site: d.Snapshot.Site})
		default:
			total += int64(len(d.Snapshot.Data))
			capture.Files = append(capture.Files, contentpkg.CapturedFile{Name: d.Snapshot.Name, Data: d.Snapshot.Data})
		}
		if total > wire.MaxRoomCarryBytes {
			httpx.Error(w, fmt.Sprintf("the apps and files are larger than a room component carries (%d bytes together)", wire.MaxRoomCarryBytes), http.StatusRequestEntityTooLarge)
			return false
		}
	}
	f.Captured = capture
	return true
}

// installInstructions makes the package's instructions the new room's
// (docs/prd/2026-10-20-complete-room-capture.md §2.2): a private expert of
// the installer's whose INSTRUCTIONS.md they are, turned on in the room and
// made its expert (setRoomExpert, the one writer), so the room's agents
// wear them as any room expert's and the installer edits them in their
// Library. It answers the expert made (nil without instructions), which a
// failed install deletes.
func (s *Service) installInstructions(ctx context.Context, claims auth.Claims, roomID string, pkg *contentpkg.Package, reply *wire.RoomInstallReply) (*store.ContentComponent, error) {
	if strings.TrimSpace(pkg.Instructions) == "" {
		return nil, nil
	}
	form := contentpkg.Form{Kind: contentpkg.KindExpert, DisplayName: truncateRunes(reply.Name+" instructions", 120),
		Description:  truncateRunes(fmt.Sprintf("The instructions of room %s, from %s: the room's agents follow them. Edit them here.", reply.Name, reply.From), 1000),
		Instructions: pkg.Instructions}
	base := firstNonEmpty(contentpkg.NameFrom(roomID), "room")
	var c store.ContentComponent
	for n := 1; ; n++ {
		form.Name = base
		if n > 1 {
			form.Name = fmt.Sprintf("%s-%d", strings.TrimRight(base[:min(len(base), 58)], "-"), n)
		}
		p, bundle, err := contentpkg.Build(form, "1.0.0", contentpkg.Options{}, nil)
		if err != nil {
			return nil, fmt.Errorf("the room's instructions as an expert: %w", err)
		}
		c, err = s.newContent(ctx, contentActor{Claims: claims}, p, bundle)
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrContentExists) || n == 20 {
			return nil, fmt.Errorf("save the room's instructions as an expert: %w", err)
		}
	}
	if err := s.putContentUse(ctx, claims, store.ContentUseRoom, roomID, c, ""); err != nil {
		return &c, err
	}
	if err := s.setRoomExpert(ctx, claims, roomID, c); err != nil {
		return &c, err
	}
	reply.Instructions = contentshare.Label(c)
	reply.Expert = firstNonEmpty(c.DisplayName, c.Name)
	return &c, nil
}

// installMadeApps copies the apps made in the source room into the new
// room, each as a new site (copySite, the copy "Use this app" makes: a
// fresh, empty data space) of its folder's site (contentpkg.RoomSite).
func (s *Service) installMadeApps(ctx context.Context, claims auth.Claims, roomID string, pkg *contentpkg.Package, reply *wire.RoomInstallReply) error {
	for _, a := range pkg.Manifest.AppSnapshots {
		files, err := contentpkg.RoomSite(pkg, a)
		if err != nil {
			return fmt.Errorf("app %s: %w", a.Title, err)
		}
		snapshot, err := tarbundle.PackFiles(files, tarbundle.SiteLimits)
		if err != nil {
			return fmt.Errorf("pack app %s: %w", a.Title, err)
		}
		if _, _, err := s.copySite(ctx, claims, roomID, a.Title, path.Base(a.Dir), reply.From, snapshot); err != nil {
			return fmt.Errorf("copy app %s: %w", a.Title, err)
		}
		reply.MadeApps = append(reply.MadeApps, a.Title)
	}
	return nil
}

// installFiles restores the package's files as the new room's, each a
// deliverable of its own (createRoomArtifact), typed by its name and bytes
// as any room file is (store.ArtifactMIME, store.ArtifactKindForFile).
func (s *Service) installFiles(ctx context.Context, claims auth.Claims, roomID string, pkg *contentpkg.Package, reply *wire.RoomInstallReply) error {
	for _, f := range pkg.Manifest.Files {
		data := contentpkg.RoomFile(pkg, f)
		mt := store.ArtifactMIME(f.Name, data[:min(len(data), 512)])
		if _, _, err := s.createRoomArtifact(ctx, claims, roomID, store.ArtifactKindForFile(f.Name, mt), f.Name, f.Name, mt, reply.From, data); err != nil {
			return fmt.Errorf("restore file %s: %w", f.Name, err)
		}
		reply.Files = append(reply.Files, f.Name)
	}
	return nil
}
