package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A work's recipe (docs/prd/2026-10-13-works-gallery-and-make-similar.md
// §3): how a deliverable version was made, captured from the job that
// produced it when the version is published as a work.
//
//	GET /v1/rooms/{roomId}/artifacts/{artifactId}/recipe   what a publish of the current version captures
//
// deliverableRecipe is the one owner of "the recipe of a deliverable
// version"; the publish route and the capture route both ask it, and
// "Make similar" reads the recipe a work stored.

// capturedRecipe is a deliverable version's recipe as captured: its
// references name the request's attachments (AttachmentID, no File yet);
// Skipped names the attachments over the reference caps.
type capturedRecipe struct {
	Recipe  *wire.WorkRecipe
	Skipped []string
}

// deliverableRecipe is the recipe of v, a version of a deliverable of
// roomID, as the member in claims reads it: the producing job's request
// text and expert (the chat's, else the room's), the skill packs the turn
// wears (contentuse.Effective), the room's and the requester's prompt
// packs, and the request's attachments within wire.MaxWorkReferences and
// MaxWorkReferenceBytes, plus the agent. The producing job is the one
// producer rule's (roomlog.ProducerOf); a version without one (an upload,
// a person's publish, a job gone or of another room) has no recipe.
func (s *Service) deliverableRecipe(ctx context.Context, claims auth.Claims, roomID string, v wire.ArtifactVersion) (capturedRecipe, error) {
	if s.ContentJobs == nil {
		return capturedRecipe{}, nil
	}
	ctx = tenant.NewContext(ctx, claims.Tenant)
	var job store.AgentJob
	var readErr error
	p, _ := roomlog.ProducerOf(ctx, v, func(ctx context.Context, id string) (wire.AgentRunAgent, error) {
		j, err := s.ContentJobs.GetAgentRun(ctx, id)
		if err == nil && j.RoomID != roomID {
			err = store.ErrAgentJobNotFound
		}
		if err != nil && !errors.Is(err, store.ErrAgentJobNotFound) {
			readErr = err
		}
		job = j
		return wire.AgentRunAgent{Owner: j.OwnerUserID, Core: j.Core, Provider: j.Provider}, err
	})
	if readErr != nil {
		return capturedRecipe{}, readErr
	}
	if !p.IsAgent() {
		return capturedRecipe{}, nil
	}
	if job.TenantID == "" {
		job.TenantID = claims.Tenant
	}
	r := &wire.WorkRecipe{Prompt: strings.TrimSpace(job.MessageText), Agent: &wire.WorkRecipeAgent{Core: p.Core, Provider: p.Provider}}
	eff, err := contentuse.Effective(ctx, s.ContentComponents, job)
	if err != nil {
		return capturedRecipe{}, err
	}
	switch {
	case eff.Expert != nil:
		r.Expert = recipePart(*eff.Expert)
	case eff.RoomExpert != nil:
		r.Expert = recipePart(*eff.RoomExpert)
	}
	for _, ref := range eff.SkillPacks {
		r.Skills = append(r.Skills, *recipePart(ref))
	}
	if r.Prompts, err = s.recipePromptPacks(ctx, job); err != nil {
		return capturedRecipe{}, err
	}
	out := capturedRecipe{Recipe: r}
	var total int64
	for _, id := range job.AttachmentIDs {
		meta, found, err := s.attachmentMeta(ctx, claims, roomID, id)
		if err != nil {
			return capturedRecipe{}, err
		}
		if !found {
			continue
		}
		if len(r.References) >= wire.MaxWorkReferences || total+meta.Size > wire.MaxWorkReferenceBytes {
			out.Skipped = append(out.Skipped, meta.Filename)
			continue
		}
		total += meta.Size
		r.References = append(r.References, wire.WorkReference{AttachmentID: meta.ID, Name: meta.Filename, ContentType: meta.ContentType, SizeBytes: meta.Size})
	}
	return out, nil
}

// recipePart is a resolved content reference as a recipe names it.
func recipePart(ref wire.ContentRef) *wire.WorkRecipePart {
	return &wire.WorkRecipePart{ID: ref.ComponentID, Name: ref.Name, DisplayName: ref.DisplayName, Version: ref.Version}
}

// recipePromptPacks are the prompt packs in effect for job's request: the
// room's, then the requester's own (those they may still use), once each.
func (s *Service) recipePromptPacks(ctx context.Context, job store.AgentJob) ([]wire.WorkRecipePart, error) {
	room, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseRoom, job.RoomID)
	if err != nil {
		return nil, err
	}
	member, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseMember, job.RequesterUserID)
	if err != nil {
		return nil, err
	}
	requester := extpipe.Viewer{Subject: job.RequesterUserID, Tenant: job.TenantID}
	var out []wire.WorkRecipePart
	seen := map[string]bool{}
	for i, u := range append(room, member...) {
		if u.Kind != contentpkg.KindPrompt || seen[u.ComponentID] {
			continue
		}
		c, err := s.ContentComponents.GetContentComponent(ctx, u.ComponentID)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if i >= len(room) && !extpipe.MemberVisibility(requester, extpipe.GateForContent(c), 0).Installed {
			continue
		}
		seen[c.ID] = true
		out = append(out, wire.WorkRecipePart{ID: c.ID, Name: c.Name, DisplayName: c.DisplayName, Version: firstNonEmpty(u.PinnedVersion, c.LatestVersion)})
	}
	return out, nil
}

// attachmentMeta reads a room attachment's metadata from realtime as the
// member in claims (found=false: gone, or no attachment store).
func (s *Service) attachmentMeta(ctx context.Context, claims auth.Claims, roomID, id string) (wire.ChatAttachment, bool, error) {
	body, found, err := s.attachmentRead(ctx, claims, roomID, id, "/meta", 64<<10)
	if err != nil || !found {
		return wire.ChatAttachment{}, false, err
	}
	var meta wire.ChatAttachment
	if err := json.Unmarshal(body, &meta); err != nil {
		return wire.ChatAttachment{}, false, err
	}
	return meta, true, nil
}

// attachmentRead reads a room attachment (suffix "" its bytes, "/meta" its
// metadata) from realtime as the member in claims, at most max bytes.
// found=false when realtime answers 404 or has no attachment store.
func (s *Service) attachmentRead(ctx context.Context, claims auth.Claims, roomID, id, suffix string, max int64) ([]byte, bool, error) {
	base := strings.TrimRight(s.Backends.Realtime, "/")
	if base == "" {
		return nil, false, nil
	}
	resp, err := callBackend(ctx, http.MethodGet, base+"/v1/rooms/"+url.PathEscape(roomID)+"/attachments/"+url.PathEscape(id)+suffix, nil, claims.Subject, claims.Tenant)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusServiceUnavailable:
		return nil, false, nil
	case resp.StatusCode != http.StatusOK:
		return nil, false, upstreamStatusError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > max {
		return nil, false, fmt.Errorf("attachment %s is larger than %d bytes", id, max)
	}
	return data, true, nil
}

// errRecipeReference: a publish's recipe names a reference the capture
// does not hold.
var errRecipeReference = errors.New("the recipe's references are the request's attachments: untick one, never add one")

// publishedRecipe is the recipe a publish stores and its reference files:
// none with includeRecipe false; else the captured recipe, or the
// request's recipe (the author's edit) with the captured references it
// keeps (by attachmentId). An empty recipe is none.
func (s *Service) publishedRecipe(ctx context.Context, claims auth.Claims, roomID string, v wire.ArtifactVersion, in wire.WorkPublishRequest) (*wire.WorkRecipe, []contentpkg.WorkReferenceFile, error) {
	if in.IncludeRecipe != nil && !*in.IncludeRecipe {
		return nil, nil, nil
	}
	captured, err := s.deliverableRecipe(ctx, claims, roomID, v)
	if err != nil {
		return nil, nil, err
	}
	r := captured.Recipe
	if in.Recipe != nil {
		edit := *in.Recipe
		edit.References = nil
		keep := map[string]wire.WorkReference{}
		if r != nil {
			for _, ref := range r.References {
				keep[ref.AttachmentID] = ref
			}
		}
		for _, want := range in.Recipe.References {
			ref, ok := keep[want.AttachmentID]
			if !ok {
				return nil, nil, errRecipeReference
			}
			edit.References = append(edit.References, ref)
		}
		r = &edit
	}
	if contentpkg.RecipeEmpty(r) {
		return nil, nil, nil
	}
	var files []contentpkg.WorkReferenceFile
	for _, ref := range r.References {
		data, found, err := s.attachmentRead(ctx, claims, roomID, ref.AttachmentID, "", wire.MaxAttachmentBytes)
		if err != nil {
			return nil, nil, err
		}
		if found {
			files = append(files, contentpkg.WorkReferenceFile{Ref: ref, Data: data})
		}
	}
	return r, files, nil
}

// handleDeliverableRecipe answers what publishing a deliverable's current
// version would capture (wire.WorkRecipeCapture): the publish dialog shows
// it to the author first, to edit or drop. Only an owner of the room's
// deliverables reads it (deliverableOwner).
func (s *Service) handleDeliverableRecipe(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	roomID := r.PathValue("roomId")
	actor, ok := s.deliverableOwner(w, r, caller, roomID)
	if !ok {
		return
	}
	_, v, err := s.deliverableVersion(r.Context(), actor.Claims, roomID, r.PathValue("artifactId"))
	if !writeDeliverableErr(w, err) {
		return
	}
	captured, err := s.deliverableRecipe(r.Context(), actor.Claims, roomID, v)
	if !writeRecipeErr(w, err) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.WorkRecipeCapture{Version: v.Version, Recipe: captured.Recipe, Skipped: captured.Skipped})
}

// writeRecipeErr answers a recipe failure and reports whether there was
// none.
func writeRecipeErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, errRecipeReference):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
	default:
		obs.ServerError(w, "capture the recipe failed", err)
		httpx.Error(w, "capture the recipe failed", http.StatusBadGateway)
	}
	return false
}
