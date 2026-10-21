package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A deleted room's agent workspaces are removed on positive evidence only,
// in two steps (docs/bugs/2026-10-22-deleted-room-workspaces-never-removed.md).
//
// Every signal the bridge has of a room going away is an absence: it is no
// longer listed, its stream answers 403 (the owner's room-scoped token no
// longer covers it, deleted or not) or its tenant-scoped detail 404s (a live
// room of another tenant does too). None of them deletes anything. The
// evidence is the room service's own deletion marker
// (GET /v1/my/deleted-rooms) or the room_deleted event its stream sends.
//
//  1. Mark. A room reported deleted gets a mark in workspaces.json (when
//     first): its bindings stay.
//  2. Purge. The sweep (at start, then every roomSweepEvery) asks about the
//     rooms it holds workspaces for that the owner's listing no longer
//     names. A mark at least roomPurgeAfter old whose room is still
//     reported deleted is purged slot by slot: the core's workspace, the
//     sandbox space with its home (purge) and the workdir under the
//     workspaces root. A slot that fails to purge stays for the next sweep;
//     a room not reported deleted any more loses its mark. A failed listing
//     or a failed question removes nothing.

// errRoomDeleted: the room's stream said the room was deleted.
var errRoomDeleted = errors.New("the room was deleted")

const (
	// roomSweepEvery paces the sweep.
	roomSweepEvery = 10 * time.Minute
	// roomPurgeAfter is how long a room stays reported deleted before its
	// workspaces go: a misreport has a day to be noticed and corrected.
	roomPurgeAfter = 24 * time.Hour
	// maxDeletedRoomsQuery is the room service's page (wire.DeletedRooms).
	maxDeletedRoomsQuery = 100
)

// roomSweep paces the sweep: one at a time, at most every roomSweepEvery.
type roomSweep struct {
	mu      sync.Mutex
	running bool
	last    time.Time
	// now is the clock (nil: time.Now); tests move it.
	now func() time.Time
}

func (b *Bridge) clock() time.Time {
	b.sweep.mu.Lock()
	now := b.sweep.now
	b.sweep.mu.Unlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

// sweepGoneRooms starts a sweep in the background when one is due; listed
// is the owner's current room set (a successful listing).
func (b *Bridge) sweepGoneRooms(ctx context.Context, listed map[string]struct{}) {
	now := b.clock()
	b.sweep.mu.Lock()
	if b.sweep.running || (!b.sweep.last.IsZero() && now.Sub(b.sweep.last) < roomSweepEvery) {
		b.sweep.mu.Unlock()
		return
	}
	b.sweep.running, b.sweep.last = true, now
	b.sweep.mu.Unlock()
	keep := make(map[string]struct{}, len(listed))
	for room := range listed {
		keep[room] = struct{}{}
	}
	go func() {
		defer func() {
			b.sweep.mu.Lock()
			b.sweep.running = false
			b.sweep.mu.Unlock()
		}()
		b.sweepOnce(b.detached(ctx), keep)
	}()
}

// sweepOnce is one sweep (see the file comment).
func (b *Bridge) sweepOnce(ctx context.Context, listed map[string]struct{}) {
	var unseen []string
	for _, room := range b.workspaces.rooms() {
		if _, ok := listed[room]; ok {
			// Listed: live. A mark from a misreport goes.
			b.workspaces.clearGone(room)
			continue
		}
		unseen = append(unseen, room)
	}
	if len(unseen) == 0 {
		return
	}
	deleted, err := b.deletedRooms(ctx, unseen)
	if err != nil {
		slog.Warn("bnbloc bridge could not ask which rooms were deleted; their workspaces stay",
			"event", "bnbloc.bridge.room_sweep_failed", "rooms", len(unseen), "error", err)
		return
	}
	now := b.clock()
	for _, room := range unseen {
		if !deleted[room] {
			// Not listed and not deleted: the owner left, or a room of
			// another tenant the listing missed. Its workspaces stay.
			b.workspaces.clearGone(room)
			continue
		}
		at := b.workspaces.markGone(room, now.UnixMilli())
		if at == 0 || now.Sub(time.UnixMilli(at)) < roomPurgeAfter {
			continue
		}
		b.purgeRoomWorkspaces(ctx, room)
	}
}

// deletedRooms asks the room service which of rooms were deleted.
func (b *Bridge) deletedRooms(ctx context.Context, rooms []string) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; start < len(rooms); start += maxDeletedRoomsQuery {
		page := rooms[start:min(start+maxDeletedRoomsQuery, len(rooms))]
		raw, err := b.gatewayGET(ctx, "/v1/my/deleted-rooms?ids="+url.QueryEscape(strings.Join(page, ",")))
		if err != nil {
			return nil, err
		}
		var reply wire.DeletedRooms
		if err := json.Unmarshal(raw, &reply); err != nil {
			return nil, err
		}
		for _, room := range reply.Deleted {
			out[room] = true
		}
	}
	return out, nil
}

// purgeRoomWorkspaces removes a deleted room's workspaces slot by slot: the
// core's workspace (best effort: a core without deleteWorkspace keeps its
// own), and for a sandboxed slot the space with its home and the workdir
// under the workspaces root. A sandboxed slot whose removal fails stays
// bound for the next sweep.
func (b *Bridge) purgeRoomWorkspaces(ctx context.Context, room string) {
	b.mu.Lock()
	b.forgetRoom(room)
	b.mu.Unlock()
	for k, id := range b.workspaces.slots(room) {
		if entry, ok := b.coreEntry(k.core); ok {
			b.deleteWorkspace(ctx, entry, id)
		}
		if k.sandboxed {
			if b.sandbox == nil || !b.sandbox.cfg.present() {
				// No sandbox handed over now: its space and workdir wait.
				continue
			}
			if err := b.sandbox.removeRoomWorkspace(ctx, k); err != nil {
				slog.Warn("bnbloc bridge could not remove a deleted room's sandbox workspace; the next sweep tries again",
					"event", "bnbloc.bridge.room_workspace_remove_failed", "room", k.room, "core", k.core, "error", err)
				continue
			}
		}
		b.workspaces.purged(k, id)
		slog.Info("bnbloc bridge removed a deleted room's workspace",
			"event", "bnbloc.bridge.room_workspace_removed", "room", k.room, "core", k.core, "provider", k.provider, "sandboxed", k.sandboxed, "workspace", id)
	}
}
