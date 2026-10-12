// Package contentuse answers which content components a member may use and
// which are in effect for an agent turn (docs/prd/2026-10-12-experts-and-
// teams.md): the chat path (realtime) pins the expert a message asks for,
// the gateway resolves a job's expert and skill packs for the bridge driving
// it and grants it their bundles, and both decide through the one
// visibility function (extpipe.MemberVisibility) plus the room's pins.
package contentuse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// ErrNotUsable: the component does not exist, or the member may not use it
// here (not visible to them and not pinned in the room).
var ErrNotUsable = errors.New("content component not available")

// Usable resolves a component the viewer asks to use in a room: visible to
// them (their own, or published to them), or pinned/enabled in the room by
// a member. version "" picks the room's pinned version, else the latest.
func Usable(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer, roomID, id, version string) (store.ContentComponent, store.ContentComponentVersion, error) {
	c, err := st.GetContentComponent(ctx, id)
	if errors.Is(err, store.ErrContentNotFound) {
		return store.ContentComponent{}, store.ContentComponentVersion{}, ErrNotUsable
	}
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, err
	}
	pinned, inRoom, err := roomPin(ctx, st, roomID, id)
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, err
	}
	if !inRoom && !extpipe.MemberVisibility(v, extpipe.GateForContent(c), 0).Installed {
		return store.ContentComponent{}, store.ContentComponentVersion{}, ErrNotUsable
	}
	if version == "" {
		version = pinned
	}
	if version == "" {
		version = c.LatestVersion
	}
	ver, err := st.GetContentComponentVersion(ctx, id, version)
	if errors.Is(err, store.ErrContentNotFound) {
		return store.ContentComponent{}, store.ContentComponentVersion{}, ErrNotUsable
	}
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, err
	}
	return c, ver, nil
}

// roomPin reports whether a room uses the component and at which pinned
// version ("" = latest).
func roomPin(ctx context.Context, st store.ContentComponentStore, roomID, id string) (string, bool, error) {
	if roomID == "" {
		return "", false, nil
	}
	uses, err := st.ListContentUses(ctx, store.ContentUseRoom, roomID)
	if err != nil {
		return "", false, err
	}
	for _, u := range uses {
		if u.ComponentID == id {
			return u.PinnedVersion, true, nil
		}
	}
	return "", false, nil
}

// Manifest decodes a version's manifest.
func Manifest(v store.ContentComponentVersion) (contentpkg.Manifest, error) {
	var m contentpkg.Manifest
	if err := json.Unmarshal(v.ManifestJSON, &m); err != nil {
		return contentpkg.Manifest{}, fmt.Errorf("content %s@%s manifest: %w", v.ComponentID, v.Version, err)
	}
	return m, nil
}

// Ref describes one resolved package for the bridge.
func Ref(c store.ContentComponent, v store.ContentComponentVersion, source string) (wire.ContentRef, error) {
	m, err := Manifest(v)
	if err != nil {
		return wire.ContentRef{}, err
	}
	short := c.Kind
	if k, ok := contentpkg.Lookup(c.Kind); ok {
		short = k.Short
	}
	return wire.ContentRef{
		ComponentID: c.ID, Kind: short, Name: c.Name, DisplayName: m.DisplayName, Version: v.Version,
		SHA256: v.SHA256, BundleKey: v.BundleBlobKey, Owner: c.Owner, Source: source,
		Permissions: m.Permissions(), NeedsSandbox: m.NeedsSandbox(),
	}, nil
}

// Effective resolves what a job's agent turn wears: the expert the chat
// picked (at the version the job recorded), the skill packs the expert
// requires (resolved by name@range against what the requester may use and
// the room's packs), the room's skill packs, and the agent owner's own
// skill packs. A component appears once (first source wins: expert,
// expert requirements, room, member). An expert the requester may no
// longer use is left out and reported in Missing as "expert <name>".
func Effective(ctx context.Context, st store.ContentComponentStore, job store.AgentJob) (wire.ContentEffective, error) {
	out := wire.ContentEffective{SkillPacks: []wire.ContentRef{}}
	seen := map[string]bool{}
	add := func(c store.ContentComponent, v store.ContentComponentVersion, source string) error {
		if seen[c.ID] {
			return nil
		}
		seen[c.ID] = true
		ref, err := Ref(c, v, source)
		if err != nil {
			return err
		}
		out.SkillPacks = append(out.SkillPacks, ref)
		return nil
	}
	requester := extpipe.Viewer{Subject: job.RequesterUserID, Tenant: job.TenantID}
	owner := extpipe.Viewer{Subject: job.OwnerUserID, Tenant: job.TenantID}
	roomUses, err := st.ListContentUses(ctx, store.ContentUseRoom, job.RoomID)
	if err != nil {
		return out, err
	}
	if job.ExpertID != "" {
		c, v, err := Usable(ctx, st, requester, job.RoomID, job.ExpertID, job.ExpertVersion)
		switch {
		case errors.Is(err, ErrNotUsable):
			out.Missing = append(out.Missing, "expert "+firstNonEmpty(job.ExpertName, job.ExpertID))
		case err != nil:
			return out, err
		default:
			ref, err := Ref(c, v, wire.ContentSourceExpert)
			if err != nil {
				return out, err
			}
			out.Expert = &ref
			seen[c.ID] = true
			m, err := Manifest(v)
			if err != nil {
				return out, err
			}
			for _, req := range m.Requires.Skills {
				rc, rv, ok, err := resolveRequirement(ctx, st, requester, job.TenantID, roomUses, req)
				if err != nil {
					return out, err
				}
				if !ok {
					out.Missing = append(out.Missing, req)
					continue
				}
				if err := add(rc, rv, wire.ContentSourceExpertNeeds); err != nil {
					return out, err
				}
			}
		}
	}
	for _, u := range roomUses {
		if u.Kind != contentpkg.KindSkill {
			continue
		}
		c, v, err := pinnedVersion(ctx, st, u)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		if err := add(c, v, wire.ContentSourceRoom); err != nil {
			return out, err
		}
	}
	memberUses, err := st.ListContentUses(ctx, store.ContentUseMember, job.OwnerUserID)
	if err != nil {
		return out, err
	}
	for _, u := range memberUses {
		if u.Kind != contentpkg.KindSkill {
			continue
		}
		c, v, err := pinnedVersion(ctx, st, u)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		if !extpipe.MemberVisibility(owner, extpipe.GateForContent(c), 0).Installed {
			continue
		}
		if err := add(c, v, wire.ContentSourceMember); err != nil {
			return out, err
		}
	}
	return out, nil
}

// BundleKeys lists every bundle an effective resolution grants.
func BundleKeys(e wire.ContentEffective) map[string]bool {
	keys := map[string]bool{}
	if e.Expert != nil {
		keys[e.Expert.BundleKey] = true
	}
	for _, r := range e.SkillPacks {
		keys[r.BundleKey] = true
	}
	return keys
}

func pinnedVersion(ctx context.Context, st store.ContentComponentStore, u store.ContentUse) (store.ContentComponent, store.ContentComponentVersion, error) {
	c, err := st.GetContentComponent(ctx, u.ComponentID)
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, err
	}
	version := u.PinnedVersion
	if version == "" {
		version = c.LatestVersion
	}
	v, err := st.GetContentComponentVersion(ctx, c.ID, version)
	return c, v, err
}

// resolveRequirement picks, for "name@range", the highest satisfying
// version among the skill packs of that name the requester may use and the
// room's skill packs (a room pin fixes its version).
func resolveRequirement(ctx context.Context, st store.ContentComponentStore, v extpipe.Viewer, tenantID string, roomUses []store.ContentUse, req string) (store.ContentComponent, store.ContentComponentVersion, bool, error) {
	ref, err := contentpkg.ParseReference(req)
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, false, nil
	}
	type cand struct {
		c   store.ContentComponent
		pin string
	}
	var cands []cand
	for _, u := range roomUses {
		if u.Kind != contentpkg.KindSkill {
			continue
		}
		c, err := st.GetContentComponent(ctx, u.ComponentID)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return store.ContentComponent{}, store.ContentComponentVersion{}, false, err
		}
		if c.Name == ref.Name {
			cands = append(cands, cand{c, u.PinnedVersion})
		}
	}
	own, err := st.ListContentComponents(ctx, store.ContentComponentQuery{TenantID: tenantID, Kind: contentpkg.KindSkill})
	if err != nil {
		return store.ContentComponent{}, store.ContentComponentVersion{}, false, err
	}
	for _, c := range own {
		if c.Name == ref.Name && extpipe.MemberVisibility(v, extpipe.GateForContent(c), 0).Installed {
			cands = append(cands, cand{c, ""})
		}
	}
	var best store.ContentComponent
	var bestV store.ContentComponentVersion
	found := false
	for _, cd := range cands {
		versions, err := st.ListContentComponentVersions(ctx, cd.c.ID)
		if err != nil {
			return store.ContentComponent{}, store.ContentComponentVersion{}, false, err
		}
		sort.SliceStable(versions, func(i, j int) bool {
			c, _ := semver.Compare(versions[i].Version, versions[j].Version)
			return c > 0
		})
		for _, ver := range versions {
			if cd.pin != "" && ver.Version != cd.pin {
				continue
			}
			ok, err := semver.Satisfies(ver.Version, ref.Range)
			if err != nil || !ok {
				continue
			}
			if !found {
				best, bestV, found = cd.c, ver, true
			} else if c, _ := semver.Compare(ver.Version, bestV.Version); c > 0 {
				best, bestV = cd.c, ver
			}
			break
		}
	}
	return best, bestV, found, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
