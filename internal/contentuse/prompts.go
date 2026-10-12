package contentuse

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// ResolvePromptRef resolves an expert quick prompt's "pack/prompt-id" for
// the viewer: the pack named in the expert's requires.prompts, at the
// highest version satisfying its range among the prompt packs the viewer
// may use and those turned on in the room (a room pin fixes its version).
// It returns nil when no usable pack has the prompt.
func ResolvePromptRef(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer, roomID string, requires []string, ref string) (*wire.ContentPromptRef, error) {
	pack, id, ok := contentpkg.ParsePromptRef(ref)
	if !ok {
		return nil, nil
	}
	req := ""
	for _, r := range requires {
		if pr, err := contentpkg.ParseReference(r); err == nil && pr.Name == pack {
			req = r
			break
		}
	}
	if req == "" {
		return nil, nil
	}
	var roomUses []store.ContentUse
	if roomID != "" {
		var err error
		if roomUses, err = st.ListContentUses(ctx, store.ContentUseRoom, roomID); err != nil {
			return nil, err
		}
	}
	c, ver, found, err := resolveReference(ctx, st, v, v.Tenant, roomUses, req, contentpkg.KindPrompt)
	if err != nil || !found {
		return nil, err
	}
	m, err := Manifest(ver)
	if err != nil {
		return nil, err
	}
	p, ok := m.FindPrompt(id)
	if !ok {
		return nil, nil
	}
	return &wire.ContentPromptRef{ComponentID: c.ID, Pack: c.Name, Version: ver.Version, Prompt: WirePrompt(p)}, nil
}

// WirePrompt is a manifest prompt on the wire (Uses filled by the caller).
func WirePrompt(p contentpkg.Prompt) wire.ContentPrompt {
	out := wire.ContentPrompt{ID: p.ID, Title: p.Title, Body: p.Body, Tags: p.Tags, Slash: p.Slash}
	for _, v := range p.Variables {
		out.Variables = append(out.Variables, wire.ContentPromptVariable{Name: v.Name, Label: v.Label, Default: v.Default, Options: v.Options})
	}
	return out
}
