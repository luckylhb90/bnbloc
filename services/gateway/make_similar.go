package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// "Make similar" (docs/prd/2026-10-13-works-gallery-and-make-similar.md
// §4): a work's recipe becomes a composer draft in a room of the caller's.
//
//	POST /v1/content-components/{id}/make-similar   {version?, roomId? | newRoom:{name?}} → wire.MakeSimilarDraft
//
// The recipe's reference files are copied into the room as the caller's
// attachments, its skill packs and prompt packs are turned on as the
// room's content uses, and its expert is picked: each only where the
// caller may (a part they cannot see, or a room's packs they may not
// change, is skipped and reported). The member edits the draft and sends
// it with their own agent.

// handleMakeSimilar answers a draft of a work visible to the caller, in a
// room they own (roomId: one they may post in) or a new room of theirs
// (newRoom), and counts it as a use of the work (scope make-similar).
func (s *Service) handleMakeSimilar(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims := actor.Claims
	var in wire.MakeSimilarRequest
	c, v, pkg, ok := s.galleryWork(w, r, claims, &in.Version, &in)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), appCopyTimeout)
	defer cancel()
	owner, roomID, created, ok := s.galleryRoom(ctx, w, claims, in.RoomID, in.NewRoom, firstNonEmpty(c.DisplayName, c.Name))
	if !ok {
		return
	}
	draft := wire.MakeSimilarDraft{RoomID: roomID, CreatedRoom: created, AttachmentIDs: []string{}, Attachments: []wire.ChatAttachment{}}
	recipe := pkg.Manifest.Recipe
	if recipe != nil {
		draft.Text = recipe.Prompt
		if err := s.similarContent(ctx, owner, roomID, recipe, &draft); err != nil {
			obs.ServerError(w, "turn on the recipe's packs failed", err)
			httpx.Error(w, "turn on the recipe's packs failed", http.StatusInternalServerError)
			return
		}
	}
	for _, f := range similarFiles(c, pkg) {
		att, err := s.uploadAttachment(ctx, owner, roomID, f)
		switch {
		case errors.Is(err, errAttachmentRefused):
			draft.Skipped = append(draft.Skipped, f.Ref.Name+" (not a file a message can attach)")
			continue
		case err != nil:
			obs.ServerError(w, "copy the recipe's files failed", err)
			httpx.Error(w, "copy the recipe's files failed", http.StatusBadGateway)
			return
		}
		draft.AttachmentIDs = append(draft.AttachmentIDs, att.ID)
		draft.Attachments = append(draft.Attachments, att)
	}
	if err := s.ContentComponents.PutContentUse(ctx, store.ContentUse{TenantID: owner.Tenant, ScopeKind: store.ContentUseMakeSimilar, ScopeID: roomID,
		ComponentID: c.ID, Kind: c.Kind, PinnedVersion: v.Version, CreatedBy: claims.Subject, CreatedAtMs: s.nowTime().UnixMilli()}); err != nil {
		obs.ServerError(w, "count the work's use failed", err)
		httpx.Error(w, "count the work's use failed", http.StatusInternalServerError)
		return
	}
	s.auditContent(ctx, "use", claims.Subject, owner.Tenant, c, v.Version,
		contentshare.Label(c)+" v"+v.Version+" made similar by "+claims.Subject+" in room "+roomID)
	httpx.WriteJSON(w, http.StatusCreated, draft)
}

// similarContent picks the recipe's expert and turns its skill packs and
// prompt packs on in the room, for the caller in claims (the room's
// organization): only a part they may see (extpipe.MemberVisibility, as
// the Library's use route asks), its packs only where they may change the
// room's (contentUseScope's rule, roomEditorClaims). The rest is recorded
// in draft.Skipped.
func (s *Service) similarContent(ctx context.Context, claims auth.Claims, roomID string, recipe *wire.WorkRecipe, draft *wire.MakeSimilarDraft) error {
	viewer := viewerOf(claims)
	if e := recipe.Expert; e != nil {
		c, ver, err := contentuse.Usable(ctx, s.ContentComponents, viewer, roomID, e.ID, "")
		switch {
		case errors.Is(err, contentuse.ErrNotUsable) || (err == nil && c.Kind != contentpkg.KindExpert):
			draft.Skipped = append(draft.Skipped, "expert "+firstNonEmpty(e.DisplayName, e.Name))
		case err != nil:
			return err
		default:
			draft.Expert = &wire.WorkRecipePart{ID: c.ID, Name: c.Name, DisplayName: c.DisplayName, Version: ver.Version}
		}
	}
	editor, mayEdit := s.roomEditorClaims(claims, roomID)
	for _, set := range []struct {
		parts      []wire.WorkRecipePart
		kind, word string
	}{{recipe.Skills, contentpkg.KindSkill, "skill pack"}, {recipe.Prompts, contentpkg.KindPrompt, "prompt pack"}} {
		for _, p := range set.parts {
			label := set.word + " " + firstNonEmpty(p.DisplayName, p.Name)
			c, err := s.ContentComponents.GetContentComponent(ctx, p.ID)
			if errors.Is(err, store.ErrContentNotFound) || (err == nil && (c.Kind != set.kind || !extpipe.MemberVisibility(viewer, extpipe.GateForContent(c), 0).Installed)) {
				draft.Skipped = append(draft.Skipped, label)
				continue
			}
			if err != nil {
				return err
			}
			if !mayEdit {
				draft.Skipped = append(draft.Skipped, label+" (only the room's owner or admins turn it on)")
				continue
			}
			if err := s.putContentUse(ctx, editor, store.ContentUseRoom, roomID, c, ""); err != nil {
				return err
			}
			draft.Enabled = append(draft.Enabled, label)
		}
	}
	return nil
}

// similarFiles are the files a draft attaches: the recipe's references,
// or, when the recipe is empty, the work itself (its one file, or a site's
// files as a zip).
func similarFiles(c store.ContentComponent, pkg *contentpkg.Package) []contentpkg.WorkReferenceFile {
	if !contentpkg.RecipeEmpty(pkg.Manifest.Recipe) {
		return contentpkg.WorkReferences(pkg)
	}
	if name, data, ok := contentpkg.WorkFile(pkg); ok {
		return []contentpkg.WorkReferenceFile{{Ref: wire.WorkReference{Name: name, ContentType: store.ArtifactMIME(name, data[:min(len(data), 512)])}, Data: data}}
	}
	zipped, err := tarbundle.ZipFiles(contentpkg.WorkSite(pkg))
	if err != nil {
		return nil
	}
	return []contentpkg.WorkReferenceFile{{Ref: wire.WorkReference{Name: c.Name + ".zip", ContentType: "application/zip"}, Data: zipped}}
}

// errAttachmentRefused: realtime does not take the file as a chat
// attachment (its type, or its size).
var errAttachmentRefused = errors.New("not a file a message can attach")

// uploadAttachment stores f as an attachment of roomID owned by the member
// in claims, through realtime's own upload route and checks (types, size,
// membership).
func (s *Service) uploadAttachment(ctx context.Context, claims auth.Claims, roomID string, f contentpkg.WorkReferenceFile) (wire.ChatAttachment, error) {
	base := strings.TrimRight(s.Backends.Realtime, "/")
	if base == "" {
		return wire.ChatAttachment{}, errAttachmentRefused
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": f.Ref.Name}))
	h.Set("Content-Type", f.Ref.ContentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return wire.ChatAttachment{}, err
	}
	if _, err := part.Write(f.Data); err != nil {
		return wire.ChatAttachment{}, err
	}
	if err := mw.Close(); err != nil {
		return wire.ChatAttachment{}, err
	}
	resp, err := callBackendTyped(ctx, backendStreamHTTP, http.MethodPost, base+"/v1/rooms/"+url.PathEscape(roomID)+"/attachments",
		mw.FormDataContentType(), body.Bytes(), claims.Subject, claims.Tenant)
	if err != nil {
		return wire.ChatAttachment{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnsupportedMediaType || resp.StatusCode == http.StatusRequestEntityTooLarge ||
		resp.StatusCode == http.StatusServiceUnavailable:
		return wire.ChatAttachment{}, errAttachmentRefused
	case resp.StatusCode != http.StatusOK:
		return wire.ChatAttachment{}, upstreamStatusError(resp)
	}
	var att wire.ChatAttachment
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&att); err != nil {
		return wire.ChatAttachment{}, err
	}
	return att, nil
}
