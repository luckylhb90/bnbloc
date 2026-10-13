package assist

import (
	"context"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// The tasks: assist components resolved by name, nearest first — the
// member's own, a copy shared with them, their organization's, the
// platform's, then the built-in — so an organization restyles a built-in
// task by publishing its own of the same name, and a member by saving one.

// scopeRank orders the scopes nearest first.
var scopeRank = map[store.ContentScope]int{
	store.ContentScopePersonal: 0,
	store.ContentScopeShared:   1,
	store.ContentScopeOrg:      2,
	store.ContentScopePlatform: 3,
	store.ContentScopeBuiltin:  4,
}

// Tasks lists the assists v may run, one per name (the nearest), by name.
func Tasks(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer) ([]store.ContentComponent, error) {
	list, err := contentuse.Visible(ctx, st, v, contentpkg.KindAssist)
	if err != nil {
		return nil, err
	}
	nearest := map[string]store.ContentComponent{}
	for _, c := range list {
		if !extpipe.MemberVisibility(v, extpipe.GateForContent(c), 0).Installed {
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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
