package assist

import (
	"context"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The tasks: assist components resolved by name, nearest first — the
// member's own, a copy shared with them, their organization's, then the
// platform's (the default content) — so an organization restyles a task
// by publishing its own of the same name, and a member by saving one.
// The one exception is a task that explains a consent
// (wire.AssistContract.Consent, ConsentScope): it resolves to the
// organization's, then the platform's, never a member's own copy.

// scopeRank orders the scopes nearest first.
var scopeRank = map[store.ContentScope]int{
	store.ContentScopePersonal: 0,
	store.ContentScopeShared:   1,
	store.ContentScopeOrg:      2,
	store.ContentScopePlatform: 3,
}

// ConsentScope reports whether an assist may answer for its name: any
// scope, except for a task that explains a decision only an organization
// admin makes (explain-source-use), which only the organization's or the
// platform's version may answer, so a member's own copy never explains a
// grant (docs/prd/2026-10-20-no-built-in-content.md §2.3).
func ConsentScope(c store.ContentComponent) bool {
	if ct, ok := wire.AssistContractOf(c.Name); ok && ct.Consent {
		return orgScope(c)
	}
	return true
}

// Tasks lists the assists v may run, one per name (the nearest that
// ConsentScope admits), by name.
func Tasks(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer) ([]store.ContentComponent, error) {
	out, err := usable(ctx, st, v, contentpkg.KindAssist, ConsentScope)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// usable lists the components of kind v may use that admit takes (nil:
// all), one per name (the nearest), in no particular order.
func usable(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer, kind string, admit func(store.ContentComponent) bool) ([]store.ContentComponent, error) {
	list, err := contentuse.Visible(ctx, st, v, kind)
	if err != nil {
		return nil, err
	}
	nearest := map[string]store.ContentComponent{}
	for _, c := range list {
		if !extpipe.MemberVisibility(v, extpipe.GateForContent(c), 0).Installed || (admit != nil && !admit(c)) {
			continue
		}
		if have, ok := nearest[c.Name]; ok && scopeRank[have.Scope] <= scopeRank[c.Scope] {
			continue
		}
		nearest[c.Name] = c
	}
	out := make([]store.ContentComponent, 0, len(nearest))
	for _, c := range nearest {
		out = append(out, c)
	}
	return out, nil
}

// LibraryOf is the library context: the experts and skill packs v may use,
// one per name (the nearest), the member's own first (nearest scope, then
// by name), at most MaxLibraryItems each.
func LibraryOf(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer) (*Library, error) {
	out := &Library{}
	for _, k := range []struct {
		kind string
		into *[]LibraryItem
	}{{contentpkg.KindExpert, &out.Experts}, {contentpkg.KindSkill, &out.Skills}} {
		list, err := usable(ctx, st, v, k.kind, nil)
		if err != nil {
			return nil, err
		}
		sort.Slice(list, func(i, j int) bool {
			if a, b := scopeRank[list[i].Scope], scopeRank[list[j].Scope]; a != b {
				return a < b
			}
			return list[i].Name < list[j].Name
		})
		for _, c := range list[:min(len(list), MaxLibraryItems)] {
			*k.into = append(*k.into, LibraryItem{Name: c.Name, DisplayName: c.DisplayName, Description: c.Description})
		}
	}
	return out, nil
}

// Task is the nearest assist named name v may run (ok false: none).
func Task(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer, name string) (store.ContentComponent, bool, error) {
	list, err := Tasks(ctx, st, v)
	if err != nil {
		return store.ContentComponent{}, false, err
	}
	for _, c := range list {
		if c.Name == name {
			return c, true, nil
		}
	}
	return store.ContentComponent{}, false, nil
}

// orgScope admits the organization's and the platform's versions only.
func orgScope(c store.ContentComponent) bool {
	return c.Scope == store.ContentScopeOrg || c.Scope == store.ContentScopePlatform
}

// ServiceTask is the task named name a service runs for v's organization
// (docs/prd/2026-10-23-connections-ai.md §2): the organization's version,
// else the platform's, never a member's own or a shared copy, since what
// it says is the service's word to the organization.
func ServiceTask(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer, name string) (store.ContentComponent, bool, error) {
	list, err := usable(ctx, st, v, contentpkg.KindAssist, orgScope)
	if err != nil {
		return store.ContentComponent{}, false, err
	}
	for _, c := range list {
		if c.Name == name {
			return c, true, nil
		}
	}
	return store.ContentComponent{}, false, nil
}
