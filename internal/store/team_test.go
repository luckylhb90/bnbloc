package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Contract tests for the team-feature stores (prompt library, message tasks,
// message/artifact search). Each runs against the memory backend and, when
// BNBLOC_TEST_MYSQL_DSN is set, the MySQL backend (incl. the ngram FULLTEXT
// index), so both behave identically.

func teamBackends(t *testing.T) map[string]*Backends {
	t.Helper()
	out := map[string]*Backends{}
	mem, err := NewBackends("")
	if err != nil {
		t.Fatalf("memory backends: %v", err)
	}
	out["memory"] = mem
	if dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN"); dsn != "" {
		my, err := NewBackends(dsn)
		if err != nil {
			t.Fatalf("mysql backends: %v", err)
		}
		t.Cleanup(func() { _ = my.DB.Close() })
		out["mysql"] = my
	}
	return out
}

// uniqueTenant isolates a run from other runs sharing the test database.
func uniqueTenant(t *testing.T) context.Context {
	t.Helper()
	return tenantCtx("tt-" + strconv.FormatInt(time.Now().UnixNano(), 36))
}

func TestTeamPromptStoreContract(t *testing.T) {
	for name, b := range teamBackends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := uniqueTenant(t)
			s := b.Prompts
			// prompt_id is a global key: suffix the fixture ids per run.
			run := "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			mk := func(id, author, title, body, vis string, tags []string, at int64) {
				t.Helper()
				if _, err := s.CreatePrompt(ctx, wire.Prompt{PromptID: id + run, Author: author, Title: title, Body: body, Tags: tags, Visibility: vis, CreatedAtMs: at, UpdatedAtMs: at}); err != nil {
					t.Fatalf("create %s: %v", id, err)
				}
			}
			mk("p1", "alice", "Release notes", "Summarise 50% of the diff", wire.PromptTenant, []string{"writing", "ops"}, 1)
			mk("p2", "alice", "Private plan", "secret body", wire.PromptPrivate, nil, 2)
			mk("p3", "bob", "部署清单", "检查回滚步骤", wire.PromptTenant, []string{"ops"}, 3)

			ids := func(ps []wire.Prompt) []string {
				out := []string{}
				for _, p := range ps {
					out = append(out, strings.TrimSuffix(p.PromptID, run))
				}
				return out
			}
			list := func(q PromptQuery) []string {
				t.Helper()
				ps, err := s.ListPrompts(ctx, q)
				if err != nil {
					t.Fatalf("list %+v: %v", q, err)
				}
				return ids(ps)
			}
			if got := list(PromptQuery{Viewer: "bob"}); !equalStrings(got, []string{"p3", "p1"}) {
				t.Fatalf("bob sees %v, want [p3 p1]", got)
			}
			if got := list(PromptQuery{Viewer: "alice", MineOnly: true}); !equalStrings(got, []string{"p2", "p1"}) {
				t.Fatalf("alice mine %v", got)
			}
			if got := list(PromptQuery{Viewer: "alice", Tag: "ops"}); !equalStrings(got, []string{"p3", "p1"}) {
				t.Fatalf("tag ops %v", got)
			}
			if got := list(PromptQuery{Viewer: "alice", Text: "50%"}); !equalStrings(got, []string{"p1"}) {
				t.Fatalf("text 50%% (LIKE metachar) %v", got)
			}
			if got := list(PromptQuery{Viewer: "alice", Text: "回滚"}); !equalStrings(got, []string{"p3"}) {
				t.Fatalf("CJK text %v", got)
			}
			if got := list(PromptQuery{Viewer: "bob", Text: "secret"}); len(got) != 0 {
				t.Fatalf("bob found private prompt %v", got)
			}

			title := "Release notes v2"
			if _, err := s.UpdatePrompt(ctx, "p1"+run, "bob", PromptPatch{Title: &title}, 10); !errors.Is(err, ErrPromptForbidden) {
				t.Fatalf("bob update = %v, want forbidden", err)
			}
			if _, err := s.UpdatePrompt(ctx, "p2"+run, "bob", PromptPatch{Title: &title}, 10); !errors.Is(err, ErrPromptNotFound) {
				t.Fatalf("bob update private = %v, want not found", err)
			}
			tags := []string{"docs"}
			p, err := s.UpdatePrompt(ctx, "p1"+run, "alice", PromptPatch{Title: &title, Tags: &tags}, 10)
			if err != nil || p.Title != title || !equalStrings(p.Tags, tags) || p.UpdatedAtMs != 10 {
				t.Fatalf("alice update = %+v, %v", p, err)
			}
			if p, err := s.UsePrompt(ctx, "p1"+run, "bob"); err != nil || p.UseCount != 1 {
				t.Fatalf("use = %+v, %v", p, err)
			}
			if _, err := s.UsePrompt(ctx, "p2"+run, "bob"); !errors.Is(err, ErrPromptNotFound) {
				t.Fatalf("use private = %v", err)
			}
			if err := s.DeletePrompt(ctx, "p1"+run, "bob", false); !errors.Is(err, ErrPromptForbidden) {
				t.Fatalf("bob delete = %v", err)
			}
			if err := s.DeletePrompt(ctx, "p2"+run, "bob", true); !errors.Is(err, ErrPromptNotFound) {
				t.Fatalf("moderator delete private = %v", err)
			}
			if err := s.DeletePrompt(ctx, "p1"+run, "bob", true); err != nil {
				t.Fatalf("moderator delete tenant prompt: %v", err)
			}
			if err := s.DeletePrompt(ctx, "p2"+run, "alice", false); err != nil {
				t.Fatalf("author delete: %v", err)
			}
			if got := list(PromptQuery{Viewer: "alice"}); !equalStrings(got, []string{"p3"}) {
				t.Fatalf("after deletes %v", got)
			}
			// Another tenant sees nothing.
			if ps, _ := s.ListPrompts(tenantCtx("other-"+name), PromptQuery{Viewer: "bob"}); len(ps) != 0 {
				t.Fatalf("cross-tenant leak: %v", ids(ps))
			}
		})
	}
}

func TestTeamMessageTaskStoreContract(t *testing.T) {
	for name, b := range teamBackends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := uniqueTenant(t)
			room := "tasks-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			s := b.MessageTasks
			if _, found, err := s.GetMessageTask(ctx, room, 5); err != nil || found {
				t.Fatalf("get unmarked = %v %v", found, err)
			}
			got, err := s.PutMessageTask(ctx, wire.MessageTask{RoomID: room, ChannelID: "ch1", Seq: 5, Status: wire.TaskOpen, Title: "Ship it", MessageAuthor: "alice", CreatedBy: "bob", CreatedAtMs: 1})
			if err != nil || got.Title != "Ship it" || got.Status != wire.TaskOpen {
				t.Fatalf("put = %+v %v", got, err)
			}
			// A later put changes only status/done fields.
			got, err = s.PutMessageTask(ctx, wire.MessageTask{RoomID: room, Seq: 5, Status: wire.TaskDone, Title: "ignored", CreatedBy: "carol", CreatedAtMs: 9, DoneBy: "alice", DoneAtMs: 7})
			if err != nil || got.Status != wire.TaskDone || got.DoneBy != "alice" || got.DoneAtMs != 7 || got.Title != "Ship it" || got.CreatedBy != "bob" || got.ChannelID != "ch1" {
				t.Fatalf("toggle = %+v %v", got, err)
			}
			got, err = s.PutMessageTask(ctx, wire.MessageTask{RoomID: room, Seq: 5, Status: wire.TaskOpen})
			if err != nil || got.DoneBy != "" || got.DoneAtMs != 0 {
				t.Fatalf("reopen = %+v %v", got, err)
			}
			if _, err := s.PutMessageTask(ctx, wire.MessageTask{RoomID: room, Seq: 2, Status: wire.TaskOpen, Title: "Earlier", CreatedBy: "bob", CreatedAtMs: 2}); err != nil {
				t.Fatal(err)
			}
			list, err := s.ListMessageTasks(ctx, room)
			if err != nil || len(list) != 2 || list[0].Seq != 2 || list[1].Seq != 5 {
				t.Fatalf("list = %+v %v", list, err)
			}
			if removed, err := s.DeleteMessageTask(ctx, room, 5); err != nil || !removed {
				t.Fatalf("delete = %v %v", removed, err)
			}
			if removed, _ := s.DeleteMessageTask(ctx, room, 5); removed {
				t.Fatal("second delete reported removed")
			}
			if list, _ := s.ListMessageTasks(tenantCtx("other-"+name), room); len(list) != 0 {
				t.Fatalf("cross-tenant leak: %+v", list)
			}
		})
	}
}

func TestTeamMessageSearchContract(t *testing.T) {
	for name, b := range teamBackends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := uniqueTenant(t)
			suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
			r1, r2 := "srch1-"+suffix, "srch2-"+suffix
			chat := func(room string, seq int64, author, text string, at int64) {
				t.Helper()
				payload, _ := json.Marshal(map[string]any{"userId": author, "text": text})
				if err := b.Chat.AppendRoomMessage(ctx, RoomMessage{RoomID: room, Seq: seq, Kind: "chat_message", Author: author, Payload: payload, CreatedAtMs: at}); err != nil {
					t.Fatalf("append: %v", err)
				}
			}
			chat(r1, 1, "alice", "The deploy window is Friday", 100)
			chat(r1, 2, "alice", "data pipeline is ready", 200)
			chat(r2, 1, "carol", "deploy freeze", 300)
			chat(r1, 3, "bob", "部署窗口在周五", 400)
			agent, _ := json.Marshal(map[string]any{"messages": []any{
				map[string]any{"payload": map[string]any{"text": "I updated the deploy script", "ownerUserId": "alice", "provider": "codex"}},
			}})
			if err := b.Chat.AppendRoomMessage(ctx, RoomMessage{RoomID: r1, Seq: 4, Kind: "agent_messages", Payload: agent, CreatedAtMs: 500}); err != nil {
				t.Fatal(err)
			}
			seqs := func(q MessageSearch) []int64 {
				t.Helper()
				ms, err := b.Chat.SearchRoomMessages(ctx, q)
				if err != nil {
					t.Fatalf("search %+v: %v", q, err)
				}
				out := []int64{}
				for _, m := range ms {
					out = append(out, m.Seq)
				}
				return out
			}
			if got := seqs(MessageSearch{RoomIDs: []string{r1}, Text: "DEPLOY"}); !equalInts(got, []int64{4, 1}) {
				t.Fatalf("deploy in r1 = %v, want [4 1] (newest first, r2 excluded)", got)
			}
			// "data" is all stopword-bearing bigrams under the default list:
			// the index must be built with stopwords disabled.
			if got := seqs(MessageSearch{RoomIDs: []string{r1}, Text: "data"}); !equalInts(got, []int64{2}) {
				t.Fatalf("data = %v, want [2]", got)
			}
			if got := seqs(MessageSearch{RoomIDs: []string{r1}, Text: "窗口"}); !equalInts(got, []int64{3}) {
				t.Fatalf("CJK = %v, want [3]", got)
			}
			if got := seqs(MessageSearch{RoomIDs: []string{r1, r2}, Text: "deploy", BeforeMs: 300, Limit: 5}); !equalInts(got, []int64{1}) {
				t.Fatalf("before cursor = %v, want [1]", got)
			}
			if got := seqs(MessageSearch{RoomIDs: []string{r1}, Text: "d"}); len(got) != 0 {
				t.Fatalf("short query = %v, want none", got)
			}
			// Edits re-index; deletes drop out.
			if ok, err := b.Chat.EditRoomMessage(ctx, r1, 1, "alice", "window moved to Monday", 600); err != nil || !ok {
				t.Fatalf("edit = %v %v", ok, err)
			}
			if got := seqs(MessageSearch{RoomIDs: []string{r1}, Text: "monday"}); !equalInts(got, []int64{1}) {
				t.Fatalf("edited text = %v, want [1]", got)
			}
			if ok, err := b.Chat.DeleteRoomMessage(ctx, r1, 1, "alice", 700); err != nil || !ok {
				t.Fatalf("delete = %v %v", ok, err)
			}
			if got := seqs(MessageSearch{RoomIDs: []string{r1}, Text: "monday"}); len(got) != 0 {
				t.Fatalf("deleted message still found: %v", got)
			}
			if _, found, _ := b.Chat.GetRoomMessage(ctx, r1, 1); found {
				t.Fatal("GetRoomMessage returned a deleted message")
			}
			m, found, err := b.Chat.GetRoomMessage(ctx, r1, 4)
			if err != nil || !found || MessageAuthor(m) != "alice/codex" || MessageText(m.Kind, m.Payload) != "I updated the deploy script" {
				t.Fatalf("get agent message = %+v %v %v", m, found, err)
			}

			// Artifact title search, scoped to the given rooms.
			for _, a := range []Artifact{
				{ArtifactID: "a1-" + suffix, RoomID: r1, Kind: ArtifactKindMarkdown, Title: "Deploy runbook", CreatedBy: "alice", CreatedAtMs: 1, UpdatedAtMs: 1},
				{ArtifactID: "a2-" + suffix, RoomID: r2, Kind: ArtifactKindMarkdown, Title: "Deploy plan", CreatedBy: "carol", CreatedAtMs: 2, UpdatedAtMs: 2},
			} {
				if _, err := b.Artifacts.PutArtifact(ctx, a); err != nil {
					t.Fatal(err)
				}
			}
			arts, err := b.Artifacts.SearchArtifacts(ctx, []string{r1}, "runbook", 10)
			if err != nil || len(arts) != 1 || arts[0].RoomID != r1 {
				t.Fatalf("artifact search = %+v %v", arts, err)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
