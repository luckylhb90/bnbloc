package store

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// ChatStore and ChatIndex contract: appends, history and feed paging, read
// cursors, edits and deletes, message search and the search-index source
// reads behave the same on both backends.

// chatRow is one chat_message row of tid in room.
func chatRow(tid, room, channel string, seq int64, text string) RoomMessage {
	payload, _ := json.Marshal(map[string]any{"text": text, "userId": "maya"})
	return RoomMessage{TenantID: tid, RoomID: room, ChannelID: channel, Seq: seq, Kind: "chat_message", Author: "maya",
		Payload: payload, CreatedAtMs: seq * 10}
}

func seqsOf(msgs []RoomMessage) []int64 {
	out := []int64{}
	for _, m := range msgs {
		out = append(out, m.Seq)
	}
	return out
}

// A taken client message id or (room, seq) is reported, never silently
// ignored, and the original row stays authoritative; a batch is stored whole
// or not at all, each row under its own tenant.
func TestContractChatStoreAppend(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid, room := uniq("t-chat-"), uniq("room-")
		ctx := tenantCtx(tid)
		s := b.Chat
		row := func(seq int64, cmi string) RoomMessage {
			m := chatRow(tid, room, "", seq, "hi")
			m.ClientMsgID = cmi
			return m
		}
		stored := func() []int64 { return seqsOf(must(s.RoomFeed(ctx, room, 0, 100))(t)) }

		if err := s.AppendRoomMessage(ctx, row(1, "cm-1")); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendRoomMessage(ctx, row(2, "cm-1")); !errors.Is(err, ErrDuplicateClientMsg) {
			t.Fatalf("same client id at a new seq = %v, want ErrDuplicateClientMsg", err)
		}
		if seq, ok := must2(s.LookupClientMsg(ctx, room, "cm-1"))(t); !ok || seq != 1 {
			t.Fatalf("LookupClientMsg = %d %v, want the original seq 1", seq, ok)
		}
		if _, ok := must2(s.LookupClientMsg(ctx, room, ""))(t); ok {
			t.Fatal("LookupClientMsg of an empty id found one")
		}
		if err := s.AppendRoomMessage(ctx, row(1, "cm-2")); !errors.Is(err, ErrDuplicateSeq) {
			t.Fatalf("taken seq = %v, want ErrDuplicateSeq", err)
		}
		if err := s.AppendRoomMessage(ctx, row(2, "")); err != nil {
			t.Fatalf("append without client id: %v", err)
		}
		if err := s.AppendRoomMessage(bg(), row(9, "")); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("append without tenant = %v", err)
		}

		if err := s.AppendRoomMessages(ctx, []RoomMessage{row(3, "a"), row(4, ""), row(5, "b")}); err != nil {
			t.Fatal(err)
		}
		// Two rows repeat stored client ids: both are named with their stored
		// sequences, and the fresh row is not stored either.
		err := s.AppendRoomMessages(ctx, []RoomMessage{row(6, "a"), row(7, "c"), row(8, "b")})
		var conflict *ClientMsgConflict
		if !errors.As(err, &conflict) || !errors.Is(err, ErrDuplicateClientMsg) {
			t.Fatalf("batch with stored client ids = %v, want *ClientMsgConflict", err)
		}
		if len(conflict.Seqs) != 2 || conflict.Seqs["a"] != 3 || conflict.Seqs["b"] != 5 {
			t.Fatalf("conflict = %v, want a:3 b:5", conflict.Seqs)
		}
		if err := s.AppendRoomMessages(ctx, []RoomMessage{row(6, "c"), row(5, "d")}); !errors.Is(err, ErrDuplicateSeq) {
			t.Fatalf("batch with a taken seq = %v, want ErrDuplicateSeq", err)
		}
		if got := stored(); !slices.Equal(got, []int64{1, 2, 3, 4, 5}) {
			t.Fatalf("stored = %v; a refused batch stored rows", got)
		}
		other := row(6, "")
		other.RoomID = room + "-other"
		noTenant := row(6, "")
		noTenant.TenantID = ""
		for name, bad := range map[string][]RoomMessage{
			"two rooms":        {row(6, ""), other},
			"repeated id":      {row(6, "x"), row(7, "x")},
			"no tenant on row": {noTenant},
		} {
			if err := s.AppendRoomMessages(ctx, bad); err == nil {
				t.Fatalf("%s: accepted", name)
			}
		}
		// Each row is written under its own tenant.
		acme := row(6, "e")
		acme.TenantID = uniq("t-chat-acme-")
		if err := s.AppendRoomMessages(ctx, []RoomMessage{acme}); err != nil {
			t.Fatal(err)
		}
		if got := stored(); len(got) != 5 {
			t.Fatalf("another tenant's row is visible: %v", got)
		}
		if seq, ok := must2(s.LookupClientMsg(tenantCtx(acme.TenantID), room, "e"))(t); !ok || seq != 6 {
			t.Fatalf("other tenant lookup = %d %v", seq, ok)
		}
		if _, ok := must2(s.LookupClientMsg(ctx, room, "e"))(t); ok {
			t.Fatal("a client id of another tenant resolves here")
		}
		// MaxRoomSeq is keyed on the room alone.
		if mx := must(s.MaxRoomSeq(bg(), room))(t); mx != 6 {
			t.Fatalf("MaxRoomSeq = %d, want 6", mx)
		}
		if mx := must(s.MaxRoomSeq(bg(), uniq("nope-")))(t); mx != 0 {
			t.Fatalf("MaxRoomSeq of an empty room = %d", mx)
		}
	})
}

// History is per channel ("" is the default channel, not a wildcard), the
// newest page below a cursor, ascending; the feed spans every channel; both
// skip deleted rows and page with the shared size rule.
func TestContractChatStoreHistoryAndFeed(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid, room := uniq("t-hist-"), uniq("room-")
		ctx := tenantCtx(tid)
		s := b.Chat
		// Appended out of sequence order: reads order by seq.
		for _, m := range []RoomMessage{chatRow(tid, room, "ch_a", 3, "c"), chatRow(tid, room, "ch_a", 1, "a"), chatRow(tid, room, "ch_b", 2, "b"),
			chatRow(tid, room, "", 4, "d"), chatRow(tid, room, "ch_a", 5, "e")} {
			if err := s.AppendRoomMessages(ctx, []RoomMessage{m}); err != nil {
				t.Fatal(err)
			}
		}
		hist := func(ch string, before int64, limit int) []int64 {
			return seqsOf(must(s.RoomHistory(ctx, room, ch, before, limit))(t))
		}
		// Each channel's oldest stored sequence: what retention has kept.
		if got := must(s.RoomRetainedFrom(ctx, room))(t); !maps.Equal(got, map[string]int64{"ch_a": 1, "ch_b": 2, "": 4}) {
			t.Fatalf("RoomRetainedFrom = %v", got)
		}
		for _, c := range []struct {
			ch     string
			before int64
			limit  int
			want   []int64
		}{
			{"ch_a", 0, 0, []int64{1, 3, 5}},
			{"ch_a", 0, 2, []int64{3, 5}},
			{"ch_a", 5, 1, []int64{3}},
			{"ch_a", 1, 10, []int64{}},
			{"ch_b", 0, 10, []int64{2}},
			{"", 0, 10, []int64{4}},
			{"ch_none", 0, 10, []int64{}},
		} {
			if got := hist(c.ch, c.before, c.limit); !slices.Equal(got, c.want) {
				t.Fatalf("RoomHistory(%q, before %d, limit %d) = %v, want %v", c.ch, c.before, c.limit, got, c.want)
			}
		}
		if got := must(s.RoomHistory(ctx, room, "ch_none", 0, 10))(t); got == nil {
			t.Fatal("an empty history is nil, want []")
		}
		feed := func(after int64, limit int) []int64 { return seqsOf(must(s.RoomFeed(ctx, room, after, limit))(t)) }
		if got := feed(0, 2); !slices.Equal(got, []int64{1, 2}) {
			t.Fatalf("feed page 1 = %v", got)
		}
		if got := feed(2, 0); !slices.Equal(got, []int64{3, 4, 5}) {
			t.Fatalf("feed after 2 = %v", got)
		}

		// Edits change the text and stamp the time; deletes hide the row from
		// history, feed and lookups, and a deleted row cannot be edited.
		if ok, err := s.EditRoomMessage(ctx, room, 3, "maya", "edited", 77); err != nil || !ok {
			t.Fatalf("edit = %v %v", ok, err)
		}
		m, ok := must2(s.GetRoomMessage(ctx, room, 3))(t)
		if !ok || MessageText(m.Kind, m.Payload) != "edited" || m.EditedAtMs == nil || *m.EditedAtMs != 77 || m.CreatedAtMs != 30 || m.ChannelID != "ch_a" {
			t.Fatalf("edited message = %+v %v", m, ok)
		}
		revisions := func(after int64) ([]MessageRevision, int64) {
			revs, latest, err := s.RoomRevisions(ctx, room, after, 0)
			if err != nil {
				t.Fatal(err)
			}
			return revs, latest
		}
		if revs, latest := revisions(0); latest != 77 || !slices.Equal(revs, []MessageRevision{{ChannelID: "ch_a", Seq: 3, AtMs: 77}}) {
			t.Fatalf("revisions after an edit = %+v (latest %d)", revs, latest)
		}
		if ok, err := s.EditRoomMessage(ctx, room, 3, "jack", "hijack", 78); err != nil || ok {
			t.Fatalf("edit by another member = %v %v", ok, err)
		}
		if ok, err := s.DeleteRoomMessage(ctx, room, 3, "jack", 79); err != nil || ok {
			t.Fatalf("delete by another member = %v %v", ok, err)
		}
		for i := 0; i < 2; i++ {
			if ok, err := s.DeleteRoomMessage(ctx, room, 3, "maya", 80); err != nil || !ok {
				t.Fatalf("delete #%d by the author = %v %v", i, ok, err)
			}
		}
		if ok, err := s.DeleteRoomMessage(ctx, room, 99, "maya", 80); err != nil || ok {
			t.Fatalf("delete of a missing message = %v %v", ok, err)
		}
		if ok, err := s.EditRoomMessage(ctx, room, 3, "maya", "again", 81); err != nil || ok {
			t.Fatalf("edit of a deleted message = %v %v", ok, err)
		}
		if _, ok := must2(s.GetRoomMessage(ctx, room, 3))(t); ok {
			t.Fatal("GetRoomMessage returned a deleted message")
		}
		// The deletion is the message's last revision (a repeated delete
		// does not move it); nothing is revised after it.
		if revs, latest := revisions(0); latest != 80 || !slices.Equal(revs, []MessageRevision{{ChannelID: "ch_a", Seq: 3, Deleted: true, AtMs: 80}}) {
			t.Fatalf("revisions after a delete = %+v (latest %d)", revs, latest)
		}
		if revs, latest := revisions(80); len(revs) != 0 || latest != 80 {
			t.Fatalf("revisions after the newest = %+v (latest %d)", revs, latest)
		}
		if got := hist("ch_a", 0, 0); !slices.Equal(got, []int64{1, 5}) {
			t.Fatalf("history after delete = %v", got)
		}
		if got := feed(0, 0); !slices.Equal(got, []int64{1, 2, 4, 5}) {
			t.Fatalf("feed after delete = %v", got)
		}
		if mx := must(s.MaxRoomSeq(bg(), room))(t); mx != 5 {
			t.Fatalf("MaxRoomSeq = %d (deleted rows count)", mx)
		}

		// Another tenant reads nothing of the room.
		octx := tenantCtx(uniq("t-hist-other-"))
		if got := must(s.RoomHistory(octx, room, "ch_a", 0, 0))(t); len(got) != 0 {
			t.Fatalf("cross-tenant history = %v", seqsOf(got))
		}
		if got := must(s.RoomFeed(octx, room, 0, 0))(t); len(got) != 0 {
			t.Fatalf("cross-tenant feed = %v", seqsOf(got))
		}
		if _, ok := must2(s.GetRoomMessage(octx, room, 1))(t); ok {
			t.Fatal("cross-tenant GetRoomMessage")
		}
		if ok, _ := s.DeleteRoomMessage(octx, room, 1, "maya", 90); ok {
			t.Fatal("cross-tenant delete")
		}
	})
}

// Page sizes: an unset limit is the default page, never more than the max.
func TestContractChatStorePageLimits(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid, room := uniq("t-page-"), uniq("room-")
		ctx := tenantCtx(tid)
		n := MaxRoomFeedPage + 5
		rows := make([]RoomMessage, 0, n)
		for seq := int64(1); seq <= int64(n); seq++ {
			rows = append(rows, chatRow(tid, room, "", seq, "x"))
		}
		if err := b.Chat.AppendRoomMessages(ctx, rows); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ limit, history, feed int }{
			{0, DefaultRoomHistoryPage, DefaultRoomFeedPage},
			{-3, DefaultRoomHistoryPage, DefaultRoomFeedPage},
			{7, 7, 7},
			{n * 2, MaxRoomHistoryPage, MaxRoomFeedPage},
		} {
			h := must(b.Chat.RoomHistory(ctx, room, "", 0, c.limit))(t)
			if len(h) != c.history || h[len(h)-1].Seq != int64(n) {
				t.Fatalf("RoomHistory(limit %d) = %d rows ending at %d, want %d ending at %d", c.limit, len(h), h[len(h)-1].Seq, c.history, n)
			}
			if f := must(b.Chat.RoomFeed(ctx, room, 0, c.limit))(t); len(f) != c.feed || f[0].Seq != 1 {
				t.Fatalf("RoomFeed(limit %d) = %d rows, want %d", c.limit, len(f), c.feed)
			}
		}
	})
}

// Read cursors are per (room, channel, user), monotonic, and the unread
// count stops at MaxUnreadCount however long the backlog.
func TestContractChatStoreUnread(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid, room := uniq("t-unread-"), uniq("room-")
		ctx := tenantCtx(tid)
		s := b.Chat
		rows := []RoomMessage{chatRow(tid, room, "ch_a", 1, "a"), chatRow(tid, room, "ch_b", 2, "b"), chatRow(tid, room, "ch_a", 3, "c")}
		for seq := int64(10); seq < 10+MaxUnreadCount+20; seq++ {
			rows = append(rows, chatRow(tid, room, "", seq, "x"))
		}
		if err := s.AppendRoomMessages(ctx, rows); err != nil {
			t.Fatal(err)
		}
		unread := func(ch, user string) int { return must(s.UnreadCount(ctx, room, ch, user))(t) }
		if n := unread("", "maya"); n != MaxUnreadCount {
			t.Fatalf("unread backlog = %d, want the cap %d", n, MaxUnreadCount)
		}
		if err := s.MarkRead(ctx, room, "", "maya", 10+MaxUnreadCount+14, 1); err != nil {
			t.Fatal(err)
		}
		if n := unread("", "maya"); n != 5 {
			t.Fatalf("unread after reading all but 5 = %d", n)
		}
		if err := s.MarkRead(ctx, room, "ch_a", "maya", 1, 1); err != nil {
			t.Fatal(err)
		}
		if a, bb, other := unread("ch_a", "maya"), unread("ch_b", "maya"), unread("ch_a", "jack"); a != 1 || bb != 1 || other != 2 {
			t.Fatalf("unread ch_a/ch_b/jack = %d/%d/%d, want 1/1/2", a, bb, other)
		}
		// A stale cursor never moves back.
		if err := s.MarkRead(ctx, room, "ch_a", "maya", 3, 2); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRead(ctx, room, "ch_a", "maya", 1, 3); err != nil {
			t.Fatal(err)
		}
		if n := unread("ch_a", "maya"); n != 0 {
			t.Fatalf("unread after a stale mark-read = %d, want 0", n)
		}
		// Who read a channel lately (an @here): by the last read's time.
		if err := s.MarkRead(ctx, room, "ch_a", "jack", 1, 2); err != nil {
			t.Fatal(err)
		}
		if got := must(s.ChannelReadersSince(ctx, room, "ch_a", 3))(t); !slices.Equal(got, []string{"maya"}) {
			t.Fatalf("readers since 3 = %v, want [maya]", got)
		}
		if got := must(s.ChannelReadersSince(ctx, room, "ch_b", 0))(t); len(got) != 0 {
			t.Fatalf("readers of an unread channel = %v", got)
		}
		// Deleted messages are not unread.
		if ok, err := s.DeleteRoomMessage(ctx, room, 2, "maya", 4); err != nil || !ok {
			t.Fatalf("delete = %v %v", ok, err)
		}
		if n := unread("ch_b", "maya"); n != 0 {
			t.Fatalf("unread of a deleted message = %d", n)
		}
		if n := must(s.UnreadCount(tenantCtx(uniq("t-unread-other-")), room, "ch_a", "maya"))(t); n != 0 {
			t.Fatalf("cross-tenant unread = %d", n)
		}
	})
}

// UnreadRooms counts rooms, not messages, with a live unread message in a live
// channel: the same notion as UnreadCount, over many rooms in one query.
func TestContractChatStoreUnreadRooms(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-unread-rooms-")
		ctx := tenantCtx(tid)
		s := b.Chat
		a, bb, c, quiet := uniq("room-a-"), uniq("room-b-"), uniq("room-c-"), uniq("room-q-")
		for _, room := range []string{a, bb, c, quiet} {
			for _, ch := range []string{"ch_1", "ch_2"} {
				if err := b.Channel.PutChannel(ctx, RoomChannel{ChannelID: room + ch, RoomID: room, Name: ch, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
					t.Fatal(err)
				}
			}
		}
		put := func(room, ch string, seq int64) RoomMessage { return chatRow(tid, room, room+ch, seq, "x") }
		// A batch is one room's.
		for room, rows := range map[string][]RoomMessage{
			a:  {put(a, "ch_1", 1), put(a, "ch_1", 2), put(a, "ch_2", 3)}, // two channels with news
			bb: {put(bb, "ch_2", 1)},                                      // one message
			c:  {put(c, "ch_1", 1)},                                       // one message, deleted below
		} {
			if err := b.Chat.AppendRoomMessages(ctx, rows); err != nil {
				t.Fatalf("append %s: %v", room, err)
			}
		}
		if ok, err := s.DeleteRoomMessage(ctx, c, 1, "maya", 9); err != nil || !ok {
			t.Fatalf("delete = %v %v", ok, err)
		}
		rooms := []string{a, bb, c, quiet}
		count := func(user string, ids []string, max int) int { return must(s.UnreadRooms(ctx, user, ids, max))(t) }
		// Rooms, not messages and not channels: a has three messages in two channels.
		if n := count("jack", rooms, 100); n != 2 {
			t.Fatalf("unread rooms = %d, want 2 (a and b; c's only message is deleted, quiet has none)", n)
		}
		// Reading a room's channels clears it, one channel at a time.
		if err := s.MarkRead(ctx, a, a+"ch_1", "jack", 2, 1); err != nil {
			t.Fatal(err)
		}
		if n := count("jack", rooms, 100); n != 2 {
			t.Fatalf("after reading one of a's channels = %d, want 2 (ch_2 is still unread)", n)
		}
		if err := s.MarkRead(ctx, a, a+"ch_2", "jack", 3, 2); err != nil {
			t.Fatal(err)
		}
		if n := count("jack", rooms, 100); n != 1 {
			t.Fatalf("after reading all of a = %d, want 1", n)
		}
		// Each user has their own cursor; only the asked-for rooms count; the cap stops the count.
		if n := count("maya", rooms, 100); n != 2 {
			t.Fatalf("another user's unread rooms = %d, want 2", n)
		}
		if n := count("maya", []string{bb}, 100); n != 1 {
			t.Fatalf("one asked-for room = %d, want 1", n)
		}
		if n := count("maya", rooms, 1); n != 1 {
			t.Fatalf("capped at 1 = %d", n)
		}
		if n := count("maya", nil, 100); n != 0 {
			t.Fatalf("no rooms = %d", n)
		}
		// Another tenant sees none of it.
		if n := must(s.UnreadRooms(tenantCtx(uniq("t-other-")), "maya", rooms, 100))(t); n != 0 {
			t.Fatalf("cross-tenant unread rooms = %d", n)
		}
		// Agrees with UnreadCount, room by room.
		for _, room := range rooms {
			per := 0
			for _, ch := range []string{"ch_1", "ch_2"} {
				per += must(s.UnreadCount(ctx, room, room+ch, "jack"))(t)
			}
			one := count("jack", []string{room}, 100)
			if (per > 0) != (one == 1) {
				t.Fatalf("room %s: UnreadCount says %d, UnreadRooms says %d", room, per, one)
			}
		}
	})
}

// Message search, one filter at a time: the caller's rooms only, the text
// as a case-insensitive substring, live rows of the ctx tenant, the
// created_at cursor, the page size and the newest-first order.
func TestContractChatIndexSearch(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-search-")
		ctx := tenantCtx(tid)
		s := b.Chat
		r1, r2, r3 := uniq("room-"), uniq("room-"), uniq("room-")
		put := func(tenantID, room string, seq, at int64, text string) {
			t.Helper()
			m := chatRow(tenantID, room, "", seq, text)
			m.CreatedAtMs = at
			if err := s.AppendRoomMessages(ctx, []RoomMessage{m}); err != nil {
				t.Fatal(err)
			}
		}
		put(tid, r1, 1, 100, "deploy the api")
		put(tid, r1, 2, 200, "lunch?")
		put(tid, r1, 3, 300, "DEPLOY done")
		put(tid, r2, 3, 300, "deploy again")
		put(tid, r3, 1, 400, "deploy elsewhere")
		put(uniq("t-search-other-"), r1, 9, 500, "deploy secret")
		if ok, err := s.DeleteRoomMessage(ctx, r1, 1, "maya", 1); err != nil || !ok {
			t.Fatalf("delete = %v %v", ok, err)
		}
		put(tid, r1, 4, 50, "deploy old")
		search := func(q MessageSearch) []string {
			t.Helper()
			ms, err := s.SearchRoomMessages(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if ms == nil {
				t.Fatalf("search %+v answered nil, want []", q)
			}
			out := []string{}
			for _, m := range ms {
				out = append(out, m.RoomID+"/"+strconv.FormatInt(m.Seq, 10))
			}
			return out
		}
		key := func(room string, seq int) string { return room + "/" + strconv.Itoa(seq) }
		for _, c := range []struct {
			name string
			q    MessageSearch
			want []string
		}{
			{"rooms + text (deleted and other tenants excluded)", MessageSearch{RoomIDs: []string{r1}, Text: "deploy"}, []string{key(r1, 3), key(r1, 4)}},
			{"several rooms, ties broken by seq then room", MessageSearch{RoomIDs: []string{r1, r2}, Text: "Deploy"}, []string{maxKey(key(r1, 3), key(r2, 3)), minKey(key(r1, 3), key(r2, 3)), key(r1, 4)}},
			{"before cursor", MessageSearch{RoomIDs: []string{r1, r2, r3}, Text: "deploy", BeforeMs: 300}, []string{key(r1, 4)}},
			{"page size", MessageSearch{RoomIDs: []string{r1, r2, r3}, Text: "deploy", Limit: 1}, []string{key(r3, 1)}},
			{"other text", MessageSearch{RoomIDs: []string{r1}, Text: "lunch"}, []string{key(r1, 2)}},
			{"no match", MessageSearch{RoomIDs: []string{r1}, Text: "nothing"}, []string{}},
			{"too short", MessageSearch{RoomIDs: []string{r1}, Text: "d"}, []string{}},
			{"no rooms", MessageSearch{Text: "deploy"}, []string{}},
		} {
			if got := search(c.q); !slices.Equal(got, c.want) {
				t.Fatalf("%s: %v, want %v", c.name, got, c.want)
			}
		}
	})
}

// maxKey and minKey order two search keys the way the newest-first tie-break
// does (room id descending at equal created_at and seq).
func maxKey(a, b string) string {
	if a > b {
		return a
	}
	return b
}

func minKey(a, b string) string {
	if a < b {
		return a
	}
	return b
}

// Source reads for an external search index: rows by key across tenants
// (soft-deleted included, a key naming the wrong tenant omitted) with their
// channel's kind, by (room, seq), and the reindex walk in primary-key order.
func TestContractChatIndexSearchSources(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		acme, globex := uniq("t-acme-"), uniq("t-globex-")
		run := uniq("")
		r1, r2 := "src1-"+run, "src2-"+run
		actx := tenantCtx(acme)
		people, err := b.Channel.EnsurePeopleChannel(actx, r1, "maya", 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range []RoomMessage{chatRow(acme, r1, people.ChannelID, 2, "people only"), chatRow(acme, r1, "", 1, "hello"), chatRow(globex, r2, "", 1, "elsewhere")} {
			if err := b.Chat.AppendRoomMessages(context.Background(), []RoomMessage{m}); err != nil {
				t.Fatal(err)
			}
		}
		if ok, err := b.Chat.DeleteRoomMessage(actx, r1, 1, "maya", 99); err != nil || !ok {
			t.Fatalf("delete = %v %v", ok, err)
		}
		got, err := b.Chat.LoadSearchSources(context.Background(), []MessageKey{
			{TenantID: globex, RoomID: r2, Seq: 1}, {TenantID: acme, RoomID: r1, Seq: 2}, {TenantID: acme, RoomID: r1, Seq: 1},
			{TenantID: acme, RoomID: r2, Seq: 1}, {TenantID: acme, RoomID: r1, Seq: 7},
		})
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, s := range got {
			keys = append(keys, s.TenantID+"/"+s.RoomID+"/"+strconv.FormatInt(s.Seq, 10))
		}
		if want := []string{acme + "/" + r1 + "/1", acme + "/" + r1 + "/2", globex + "/" + r2 + "/1"}; !slices.Equal(keys, want) {
			t.Fatalf("loaded %v, want %v (by room and seq; wrong-tenant and missing keys omitted)", keys, want)
		}
		if s := got[0]; !s.Deleted || s.Text() != "" || s.Author != "maya" {
			t.Fatalf("deleted row = %+v", s)
		}
		if s := got[1]; s.Deleted || s.Text() != "people only" || s.ChannelID != people.ChannelID || s.CreatedAtMs != 20 {
			t.Fatalf("people row = %+v", s)
		}
		if b.DB != nil && got[1].ChannelKind != ChannelKindPeople {
			t.Fatalf("people row channel kind = %q", got[1].ChannelKind)
		}
		if empty := must(b.Chat.LoadSearchSources(context.Background(), nil))(t); empty == nil || len(empty) != 0 {
			t.Fatalf("no keys = %#v, want []", empty)
		}
		// The walk resumes from a cursor just before this run's rooms.
		cursor := searchCursor{roomID: r1, seq: 0}.String()
		var walked []string
		for i := 0; i < 10; i++ {
			rows, bt, err := b.Chat.ScanSearchSources(context.Background(), cursor, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range rows {
				if s.RoomID == r1 || s.RoomID == r2 {
					walked = append(walked, s.TenantID+"/"+s.RoomID+"/"+strconv.FormatInt(s.Seq, 10))
				}
			}
			cursor = bt.Cursor
			if bt.Done || (len(rows) > 0 && rows[len(rows)-1].RoomID > r2) {
				break
			}
		}
		if want := []string{acme + "/" + r1 + "/1", acme + "/" + r1 + "/2", globex + "/" + r2 + "/1"}; !slices.Equal(walked, want) {
			t.Fatalf("walk = %v, want %v", walked, want)
		}
		if _, _, err := b.Chat.ScanSearchSources(context.Background(), "bogus", 2); !errors.Is(err, ErrInvalidSearchCursor) {
			t.Fatalf("bogus cursor = %v", err)
		}
	})
}
