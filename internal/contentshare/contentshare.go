// Package contentshare gives members copies of an author's content
// component (docs/prd/2026-10-12-experts-and-teams.md §1, step 4 and "Gap
// closure" 2). A shared copy is a component owned by the recipient (scope
// shared, source_id = the author's component, shared_by = the author)
// whose versions are the author's version rows cloned: same manifest, same
// content-addressed bundle. The gateway's share route and realtime's
// member-join step both copy through Copy, so a member who joins a room
// with a standing room share gets exactly what a share would have given
// them.
package contentshare

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// CloneVersion is a version row copied onto another component: the same
// manifest and content-addressed bundle (no bytes are copied), keeping who
// wrote it. A platform signature never carries over.
func CloneVersion(v store.ContentComponentVersion, now int64) store.ContentComponentVersion {
	return store.ContentComponentVersion{Version: v.Version, ManifestJSON: v.ManifestJSON, BundleBlobKey: v.BundleBlobKey,
		SHA256: v.SHA256, SizeBytes: v.SizeBytes, CreatedBy: v.CreatedBy, CreatedAtMs: now}
}

// ListingFields copies a version's display fields onto a component row.
func ListingFields(c *store.ContentComponent, v store.ContentComponentVersion, now int64) error {
	m, err := contentuse.Manifest(v)
	if err != nil {
		return err
	}
	c.DisplayName, c.Description, c.Category, c.UpdatedAtMs = m.DisplayName, m.Description, m.Category, now
	return nil
}

// KindLabel is a kind's display word ("expert", "skill pack", "prompt pack").
func KindLabel(kind string) string {
	k, ok := contentpkg.Lookup(kind)
	switch {
	case ok && k.Short == "skill":
		return "skill pack"
	case ok && k.Short == "prompt":
		return "prompt pack"
	case ok:
		return k.Short
	}
	return kind
}

// Label names a component in notifications and audit details:
// `expert "Reviewer"`.
func Label(c store.ContentComponent) string {
	name := c.DisplayName
	if name == "" {
		name = c.Name
	}
	return KindLabel(c.Kind) + ` "` + name + `"`
}

// Copy gives member a copy of src at version v: a new copy, or their
// existing copy of src moved forward to v (never back).
func Copy(ctx context.Context, st store.ContentComponentStore, src store.ContentComponent, v store.ContentComponentVersion, member string, now int64) (wire.ContentShareResult, error) {
	for attempt := 0; attempt < 2; attempt++ {
		existing, err := st.ListContentComponents(ctx, store.ContentComponentQuery{
			TenantID: src.TenantID, Owner: member, Kind: src.Kind, Scopes: []store.ContentScope{store.ContentScopeShared}, SourceID: src.ID, Limit: 1,
		})
		if err != nil {
			return wire.ContentShareResult{}, err
		}
		if len(existing) > 0 {
			cp := existing[0]
			res := wire.ContentShareResult{Member: member, ComponentID: cp.ID, Version: cp.LatestVersion, Status: wire.ContentShareUnchanged}
			if cmp, err := semver.Compare(v.Version, cp.LatestVersion); err != nil || cmp <= 0 {
				return res, nil
			}
			next := cp
			if err := ListingFields(&next, v, now); err != nil {
				return wire.ContentShareResult{}, err
			}
			switch err := st.AddContentComponentVersion(ctx, next, cp.LatestVersion, CloneVersion(v, now)); {
			case errors.Is(err, store.ErrContentVersionConflict):
				return res, nil // moved by a concurrent share: it has a version at least as new
			case err != nil:
				return wire.ContentShareResult{}, err
			}
			res.Version, res.Status = v.Version, wire.ContentShareUpdated
			return res, nil
		}
		cp := store.ContentComponent{
			ID: ids.New("cc_"), Kind: src.Kind, Name: src.Name, TenantID: src.TenantID, Owner: member,
			Scope: store.ContentScopeShared, Status: store.ContentStatusActive, SourceID: src.ID, SharedBy: src.Owner, CreatedAtMs: now,
		}
		if err := ListingFields(&cp, v, now); err != nil {
			return wire.ContentShareResult{}, err
		}
		switch err := st.CreateContentComponent(ctx, cp, CloneVersion(v, now)); {
		case errors.Is(err, store.ErrContentExists):
			continue // a concurrent share created it: move that one instead
		case err != nil:
			return wire.ContentShareResult{}, err
		}
		return wire.ContentShareResult{Member: member, ComponentID: cp.ID, Version: v.Version, Status: wire.ContentShareCreated}, nil
	}
	return wire.ContentShareResult{}, fmt.Errorf("share %s with %s: copy changed concurrently", src.ID, member)
}

// RecordRoomShare records (or moves forward, never back) the standing share
// of src at v with roomID, so later joiners get a copy too.
func RecordRoomShare(ctx context.Context, st store.ContentComponentStore, src store.ContentComponent, v store.ContentComponentVersion, roomID, note string, now int64) error {
	cur, err := st.ListContentRoomShares(ctx, store.ContentRoomShareQuery{RoomID: roomID, ComponentID: src.ID})
	if err != nil {
		return err
	}
	if len(cur) > 0 {
		if cmp, err := semver.Compare(v.Version, cur[0].Version); err == nil && cmp < 0 {
			return nil
		}
	}
	return st.PutContentRoomShare(ctx, store.ContentRoomShare{TenantID: src.TenantID, RoomID: roomID, ComponentID: src.ID, Version: v.Version,
		SharedBy: src.Owner, Note: note, CreatedAtMs: now, UpdatedAtMs: now})
}

// Grant is one room share handed to a member who joined the room.
type Grant struct {
	Source store.ContentComponent
	Share  store.ContentRoomShare
	Result wire.ContentShareResult
}

// GrantRoomShares gives member, who just joined roomID in tenantID, a copy
// of each standing share of the room: never their own component, never a
// share whose author is no longer a human member of the room (authorInRoom
// reports it; a share stops with its author's membership), never a source
// that is no longer its author's personal component. The results include
// unchanged copies; a caller notifies the created and moved ones.
func GrantRoomShares(ctx context.Context, st store.ContentComponentStore, tenantID, roomID, member string, authorInRoom func(author string) bool, now int64) ([]Grant, error) {
	shares, err := st.ListContentRoomShares(ctx, store.ContentRoomShareQuery{RoomID: roomID})
	if err != nil {
		return nil, err
	}
	var out []Grant
	for _, sh := range shares {
		if sh.TenantID != tenantID || strings.EqualFold(sh.SharedBy, member) || !authorInRoom(sh.SharedBy) {
			continue
		}
		src, err := st.GetContentComponent(ctx, sh.ComponentID)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		if src.Scope != store.ContentScopePersonal || src.Owner != sh.SharedBy || src.TenantID != tenantID {
			continue
		}
		v, err := st.GetContentComponentVersion(ctx, src.ID, sh.Version)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		res, err := Copy(ctx, st, src, v, member, now)
		if err != nil {
			return out, err
		}
		out = append(out, Grant{Source: src, Share: sh, Result: res})
	}
	return out, nil
}
