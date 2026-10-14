package contentuse

import (
	"context"
	"errors"
	"sort"
	"strings"

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
	out := wire.ContentPrompt{ID: p.ID, Title: p.Title, Body: p.Body, Tags: p.Tags, Slash: p.Slash, Starter: p.Starter, Files: p.Files, Selection: p.Selection}
	for _, v := range p.Variables {
		out.Variables = append(out.Variables, wire.ContentPromptVariable{Name: v.Name, Label: v.Label, Default: v.Default, Options: v.Options})
	}
	return out
}

// PromptHit is one prompt of a prompt pack that matched a search, at the
// pack's latest version.
type PromptHit struct {
	Component store.ContentComponent
	Version   string
	Prompt    contentpkg.Prompt
}

// SearchPrompts finds the prompts, in the prompt packs the viewer can see
// (Visible: their own and their shared copies, their organization's, the
// platform's and the built-in ones), whose title, body, tags or slash
// command contain text (case-insensitive): most recently updated packs
// first, at most limit hits. The prompts live in the stored manifests, so no
// bundle is read.
func SearchPrompts(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer, text string, limit int) ([]PromptHit, error) {
	needle := strings.ToLower(strings.TrimSpace(text))
	if needle == "" || limit <= 0 {
		return nil, nil
	}
	packs, err := Visible(ctx, st, v, contentpkg.KindPrompt)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(packs, func(i, j int) bool { return packs[i].UpdatedAtMs > packs[j].UpdatedAtMs })
	var out []PromptHit
	for _, c := range packs {
		ver, err := st.GetContentComponentVersion(ctx, c.ID, c.LatestVersion)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		m, err := Manifest(ver)
		if err != nil {
			return nil, err
		}
		for _, p := range m.Prompts {
			if promptMatches(p, needle) {
				out = append(out, PromptHit{Component: c, Version: ver.Version, Prompt: p})
				if len(out) == limit {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

func promptMatches(p contentpkg.Prompt, needle string) bool {
	if strings.Contains(strings.ToLower(p.Title), needle) || strings.Contains(strings.ToLower(p.Body), needle) ||
		strings.Contains(strings.ToLower(p.Slash), needle) {
		return true
	}
	for _, t := range p.Tags {
		if strings.Contains(strings.ToLower(t), needle) {
			return true
		}
	}
	return false
}
