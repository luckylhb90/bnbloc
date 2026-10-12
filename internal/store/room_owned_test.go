package store

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// schemaTableColumns parses schema.sql into table -> column names.
func schemaTableColumns(t *testing.T) map[string][]string {
	t.Helper()
	head := regexp.MustCompile(`(?m)^CREATE TABLE IF NOT EXISTS (\w+) \(`)
	col := regexp.MustCompile(`^\s+(\w+)\s+[A-Z]`)
	out := map[string][]string{}
	for _, m := range head.FindAllStringSubmatchIndex(schemaSQL, -1) {
		name := schemaSQL[m[2]:m[3]]
		body := schemaSQL[m[1]:]
		body = body[:strings.Index(body, ";")]
		for _, line := range strings.Split(body, "\n") {
			c := col.FindStringSubmatch(line)
			if c == nil || c[1] == "PRIMARY" || c[1] == "UNIQUE" || c[1] == "KEY" || c[1] == "FULLTEXT" {
				continue
			}
			out[name] = append(out[name], c[1])
		}
	}
	if len(out) < 20 {
		t.Fatalf("parsed only %d schema tables", len(out))
	}
	return out
}

// TestRoomOwnedTablesDeletedWithRoom: every schema.sql table holding a room id
// is cleared by DeleteRoom (roomOwnedTables) or exempted with a reason, and
// every listed table exists. A new room-scoped table that is not listed would
// outlive its room (2026-10-12: message task titles, the message text
// snapshot, survived the room's deletion).
func TestRoomOwnedTablesDeletedWithRoom(t *testing.T) {
	tables := schemaTableColumns(t)
	listed := map[string]bool{}
	for _, rt := range roomOwnedTables {
		if listed[rt.table] {
			t.Errorf("roomOwnedTables lists %s twice", rt.table)
		}
		listed[rt.table] = true
		if _, ok := tables[rt.table]; !ok {
			t.Errorf("roomOwnedTables lists %s, which schema.sql does not define", rt.table)
		}
		if strings.Count(rt.where, "?") != len(rt.args("t", "r")) {
			t.Errorf("roomOwnedTables %s: %d placeholders, %d args", rt.table, strings.Count(rt.where, "?"), len(rt.args("t", "r")))
		}
	}
	var missing []string
	for name, cols := range tables {
		for _, c := range cols {
			if c != "room_id" {
				continue
			}
			if _, exempt := roomOwnedExempt[name]; !listed[name] && !exempt {
				missing = append(missing, name)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("schema.sql tables with a room_id that DeleteRoom does not clear: %v; add them to roomOwnedTables (and the memory store's deleteRoomRows) or to roomOwnedExempt with a reason", missing)
	}
	for name := range roomOwnedExempt {
		if listed[name] {
			t.Errorf("%s is both listed and exempt", name)
		}
	}
}

// DeleteRoom clears the same rows on both backends: everything a room owns
// is gone, and the other room's rows stay.
func TestContractDeleteRoomClearsRoomOwnedRows(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-del-")
		ctx := tenantCtx(tid)
		gone := newContractRoom(t, ctx, b, "maya")
		kept := newContractRoom(t, ctx, b, "maya")
		comp := ContentComponent{ID: uniq("cc-"), Kind: "bnbloc.expert.package/v1", Name: uniq("n"), TenantID: tid, Owner: "maya",
			Scope: ContentScopePersonal, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
		if err := b.ContentComponents.CreateContentComponent(ctx, comp, contentVersion(comp.ID, "1.0.0", "sha256/"+strings.Repeat("d", 64), 1)); err != nil {
			t.Fatal(err)
		}
		type probe struct {
			name  string
			check func(room string) bool // true while the room's row exists
		}
		var probes []probe
		seed := func(room string) {
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("seed %s: %v", room, err)
				}
			}
			must(b.Chat.AppendRoomMessage(ctx, RoomMessage{TenantID: tid, RoomID: room, Seq: 1, Kind: "chat_message", Author: "maya",
				Payload: json.RawMessage(`{"text":"secret"}`), CreatedAtMs: 1}))
			_, err := b.MessageTasks.PutMessageTask(ctx, wire.MessageTask{RoomID: room, Seq: 1, Status: "open", Title: "secret", CreatedBy: "maya", CreatedAtMs: 1})
			must(err)
			_, _, err = b.Activity.AppendMessages(ctx, room, wire.ReportMessagesInput{WorkspaceID: room, AgentSessionID: "s",
				Messages: []wire.Message{{Seq: 1, CreatedAtUnixMs: 1, Payload: map[string]any{"ownerUserId": room, "provider": "codex", "metrics": map[string]any{"inputTokens": 1.0}}}}})
			must(err)
			_, err = b.BridgeJobs.PutAgentJob(ctx, AgentJob{JobID: "job-" + room, RoomID: room, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "maya",
				Core: "c", Provider: "p", Status: AgentJobQueued, CreatedAtMs: 1, UpdatedAtMs: 1})
			must(err)
			must(b.Grant.PutGrant(ctx, Grant{GrantID: "g-" + room, RoomID: room, TargetID: "shared-agent:x", OwnerUserID: "maya", GranteeUserID: "*", MaxConcurrent: 1, CreatedAtMs: 1}))
			_, err = b.QuotaRequests.CreateQuotaRequest(ctx, QuotaRequest{RequestID: "q-" + room, RoomID: room, OwnerUserID: "maya", RequesterUserID: "jack", Provider: "p", CreatedAtMs: 1})
			must(err)
			must(b.Attachments.PutAttachment(ctx, Attachment{ID: "att-" + room, RoomID: room, Uploader: "maya", Filename: "f", ContentType: "text/plain", Size: 1, SHA256: "x", BlobKey: "k", CreatedAtMs: 1}))
			_, err = b.Artifacts.PutArtifact(ctx, Artifact{ArtifactID: "art-" + room, RoomID: room, Kind: "markdown", Title: "t", CreatedBy: "maya"})
			must(err)
			must(b.AgentPrefs.PutAgentPrefs(ctx, AgentPrefs{RoomID: room, Provider: "p"}, "maya"))
			must(b.RoomImports.PutRoomImport(ctx, wire.RoomHandoffInfo{RoomID: room, SourceRoomID: "src", ImportedBy: "maya", ImportedAtMs: 1}))
			_, err = b.Schedules.CreateSchedule(ctx, sampleSchedule("sch-"+room, room, "maya", 0))
			must(err)
			must(b.ReplyTokens.PutNotificationReplyToken(ctx, NotificationReplyToken{TokenHash: "tok-" + room, TenantID: tid, Notifier: "n", EventID: "e", RoomID: room, UserID: "maya", CreatedAtMs: 1, ExpiresAtMs: 9e15}))
			must(b.ContentComponents.PutContentDraft(ctx, ContentDraft{ID: "d-" + room, TenantID: tid, RoomID: room, JobID: "j", Requester: "maya", Author: "maya",
				Kind: comp.Kind, Name: "n", Version: "1.0.0", ManifestJSON: json.RawMessage(`{}`), BundleBlobKey: "k", SHA256: "x", SizeBytes: 1, CreatedAtMs: 1, ExpiresAtMs: 9e15}))
			must(b.ContentComponents.PutContentRoomShare(ctx, ContentRoomShare{RoomID: room, ComponentID: comp.ID, TenantID: tid, Version: "1.0.0", SharedBy: "maya", CreatedAtMs: 1, UpdatedAtMs: 1}))
			must(b.ContentComponents.PutContentUse(ctx, ContentUse{TenantID: tid, ScopeKind: ContentUseRoom, ScopeID: room, ComponentID: comp.ID, Kind: comp.Kind, CreatedBy: "maya", CreatedAtMs: 1}))
			must(b.OrgSync.RecordDefaultRoom(ctx, "maya", room, 1))
		}
		probes = []probe{
			{"room_messages", func(r string) bool { h, _ := b.Chat.RoomHistory(ctx, r, "", 0, 10); return len(h) > 0 }},
			{"message_tasks", func(r string) bool { l, _ := b.MessageTasks.ListMessageTasks(ctx, r); return len(l) > 0 }},
			{"agent_messages", func(r string) bool { return len(must(b.AgentUsage().AgentUsage(ctx, r, 0))(t)) > 0 }},
			{"agent_jobs", func(r string) bool {
				_, err := b.BridgeJobs.(AgentRunReader).GetAgentRun(ctx, "job-"+r)
				return err == nil
			}},
			{"shared_agent_grants", func(r string) bool { _, err := b.Grant.GetGrant(ctx, "g-"+r); return err == nil }},
			{"shared_agent_quota_requests", func(r string) bool { _, err := b.QuotaRequests.GetQuotaRequest(ctx, "q-"+r); return err == nil }},
			{"attachments", func(r string) bool { _, ok, _ := b.Attachments.GetAttachment(ctx, r, "att-"+r); return ok }},
			{"artifacts", func(r string) bool { _, err := b.Artifacts.GetArtifact(ctx, "art-"+r); return err == nil }},
			{"user_agent_prefs", func(r string) bool { _, ok := must2(b.AgentPrefs.GetAgentPrefs(ctx, r, "", "maya"))(t); return ok }},
			{"room_imports", func(r string) bool { _, ok, _ := b.RoomImports.GetRoomImport(ctx, r); return ok }},
			{"room_schedules", func(r string) bool { _, err := b.Schedules.GetSchedule(ctx, r, "sch-"+r); return err == nil }},
			{"notification_reply_tokens", func(r string) bool { _, ok, _ := b.ReplyTokens.GetNotificationReplyToken(ctx, "tok-"+r); return ok }},
			{"content_drafts", func(r string) bool { _, err := b.ContentComponents.GetContentDraft(ctx, "d-"+r); return err == nil }},
			{"content_room_shares", func(r string) bool {
				l, _ := b.ContentComponents.ListContentRoomShares(ctx, ContentRoomShareQuery{RoomID: r})
				return len(l) > 0
			}},
			{"content_component_uses", func(r string) bool {
				l, _ := b.ContentComponents.ListContentUses(ctx, ContentUseRoom, r)
				return len(l) > 0
			}},
			{"org_sync_default_rooms", func(r string) bool { ok, _ := b.OrgSync.DefaultRoomGranted(ctx, "maya", r); return ok }},
		}
		seed(gone)
		seed(kept)
		for _, p := range probes {
			if !p.check(gone) || !p.check(kept) {
				t.Fatalf("%s: seeded row not readable", p.name)
			}
		}
		if err := b.Room.DeleteRoom(ctx, gone); err != nil {
			t.Fatal(err)
		}
		for _, p := range probes {
			if p.check(gone) {
				t.Errorf("%s: the deleted room's row survived", p.name)
			}
			if !p.check(kept) {
				t.Errorf("%s: the other room's row was deleted", p.name)
			}
		}
		if err := b.Room.DeleteRoom(ctx, gone); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("second DeleteRoom = %v, want ErrRoomNotFound", err)
		}
	})
}
