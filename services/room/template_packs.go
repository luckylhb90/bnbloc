package room

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A template's starters are its prompt packs' (docs/bugs/2026-10-13-
// starters-and-shared-use-counts.md): the template names packs, and a room
// made from it gets them as room content uses, so their starter prompts
// show through the one starter mechanism (prompt packs' starter prompts).

// maxTemplatePromptPacks bounds a template's prompt packs.
const maxTemplatePromptPacks = 20

// errTemplateContentUnavailable: the template names prompt packs and this
// service has no content store to check or turn them on with.
var errTemplateContentUnavailable = errors.New("prompt packs unavailable")

// normalizeTemplatePacks trims, de-duplicates (by component) and bounds a
// template's prompt packs; whether each is a pack the author may use is
// checkTemplatePacks' (it needs the store).
func normalizeTemplatePacks(in []wire.TemplateContentUse) ([]wire.TemplateContentUse, error) {
	out := []wire.TemplateContentUse{}
	seen := map[string]bool{}
	for _, p := range in {
		p = wire.TemplateContentUse{ComponentID: strings.TrimSpace(p.ComponentID), Version: strings.TrimSpace(p.Version)}
		if p.ComponentID == "" || seen[p.ComponentID] {
			continue
		}
		seen[p.ComponentID] = true
		out = append(out, p)
	}
	if len(out) > maxTemplatePromptPacks {
		return out, fmt.Errorf("at most %d prompt packs", maxTemplatePromptPacks)
	}
	return out, nil
}

// templateViewer is the member a template's packs are checked for, in the
// request's tenant.
func templateViewer(ctx context.Context, user string) extpipe.Viewer {
	return extpipe.Viewer{Subject: user, Tenant: tenant.FromContext(ctx)}
}

// usablePack resolves one template pack for viewer: a prompt pack they may
// use (contentuse.Usable owns the rule), at the pinned version when one is
// named. contentuse.ErrNotUsable when it is not.
func (s *Service) usablePack(ctx context.Context, v extpipe.Viewer, p wire.TemplateContentUse) (store.ContentComponent, error) {
	c, _, err := contentuse.Usable(ctx, s.Content, v, "", p.ComponentID, p.Version)
	if err == nil && c.Kind != contentpkg.KindPrompt {
		err = contentuse.ErrNotUsable
	}
	return c, err
}

// checkTemplatePacks refuses a template whose prompt packs the author may
// not use (a bad request), or any when there is no content store.
func (s *Service) checkTemplatePacks(ctx context.Context, user string, packs []wire.TemplateContentUse) (badRequest bool, err error) {
	if len(packs) == 0 {
		return false, nil
	}
	if s.Content == nil {
		return false, errTemplateContentUnavailable
	}
	v := templateViewer(ctx, user)
	for _, p := range packs {
		if _, err := s.usablePack(ctx, v, p); errors.Is(err, contentuse.ErrNotUsable) {
			return true, fmt.Errorf("prompt pack %s is not a prompt pack you can use", p.ComponentID)
		} else if err != nil {
			return false, err
		}
	}
	return false, nil
}

// roomPromptPacks are the room's prompt pack uses that user may use
// themselves: what "Save as template" captures.
func (s *Service) roomPromptPacks(ctx context.Context, roomID, user string) ([]wire.TemplateContentUse, error) {
	out := []wire.TemplateContentUse{}
	if s.Content == nil {
		return out, nil
	}
	uses, err := s.Content.ListContentUses(ctx, store.ContentUseRoom, roomID)
	if err != nil {
		return nil, err
	}
	v := templateViewer(ctx, user)
	for _, u := range uses {
		if u.Kind != contentpkg.KindPrompt {
			continue
		}
		p := wire.TemplateContentUse{ComponentID: u.ComponentID, Version: u.PinnedVersion}
		if _, err := s.usablePack(ctx, v, p); errors.Is(err, contentuse.ErrNotUsable) {
			continue
		} else if err != nil {
			return nil, err
		}
		out = append(out, p)
		if len(out) == maxTemplatePromptPacks {
			break
		}
	}
	return out, nil
}

// applyTemplatePacks turns the template's prompt packs on in the new room
// (room content uses, by the creator); it runs last, so nothing after it
// can fail the room. A pack the creator may not use is reported in
// res.FailedPromptPacks and left out.
func (s *Service) applyTemplatePacks(ctx context.Context, user, roomID string, packs []wire.TemplateContentUse, res *wire.RoomTemplateInstance) error {
	if len(packs) == 0 {
		return nil
	}
	if s.Content == nil {
		return errTemplateContentUnavailable
	}
	v := templateViewer(ctx, user)
	for _, p := range packs {
		c, err := s.usablePack(ctx, v, p)
		if errors.Is(err, contentuse.ErrNotUsable) {
			res.FailedPromptPacks = append(res.FailedPromptPacks, p.ComponentID)
			continue
		}
		if err != nil {
			return err
		}
		if err := s.Content.PutContentUse(ctx, store.ContentUse{
			TenantID: v.Tenant, ScopeKind: store.ContentUseRoom, ScopeID: roomID, ComponentID: c.ID, Kind: c.Kind,
			PinnedVersion: p.Version, CreatedBy: user, CreatedAtMs: s.Now(),
		}); err != nil {
			// The room is deleted on failure; take the packs already
			// turned on with it.
			for _, id := range res.PromptPacks {
				_ = s.Content.DeleteContentUse(context.WithoutCancel(ctx), store.ContentUseRoom, roomID, id)
			}
			return fmt.Errorf("turn on prompt pack %s: %w", c.ID, err)
		}
		res.PromptPacks = append(res.PromptPacks, c.ID)
	}
	return nil
}
