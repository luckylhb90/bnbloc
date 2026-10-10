package store

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Source reads for an external search index: rows by key across tenants
// (soft-deleted included, a key naming the wrong tenant omitted) with
// their channel's kind, and the reindex walk in primary-key order.
func TestSearchSourceContract(t *testing.T) {
	for name, b := range teamBackends(t) {
		t.Run(name, func(t *testing.T) {
			run := strconv.FormatInt(time.Now().UnixNano(), 36)
			acme, globex := "acme-"+run, "globex-"+run
			r1, r2 := "src1-"+run, "src2-"+run
			actx := tenant.NewContext(context.Background(), acme)
			people, err := b.Channel.EnsurePeopleChannel(actx, r1, "maya", 1)
			if err != nil {
				t.Fatal(err)
			}
			put := func(tid, room, channel string, seq int64, text string) {
				t.Helper()
				payload, _ := json.Marshal(map[string]any{"text": text, "userId": "maya"})
				if err := b.Chat.AppendRoomMessages(context.Background(), []RoomMessage{{TenantID: tid, RoomID: room, ChannelID: channel, Seq: seq,
					Kind: "chat_message", Author: "maya", Payload: payload, CreatedAtMs: seq * 10}}); err != nil {
					t.Fatal(err)
				}
			}
			put(acme, r1, "", 1, "hello")
			put(acme, r1, people.ChannelID, 2, "people only")
			put(globex, r2, "", 1, "elsewhere")
			if ok, err := b.Chat.DeleteRoomMessage(actx, r1, 1, "maya", 99); err != nil || !ok {
				t.Fatalf("delete = %v %v", ok, err)
			}
			got, err := b.Chat.LoadSearchSources(context.Background(), []MessageKey{
				{TenantID: acme, RoomID: r1, Seq: 1}, {TenantID: acme, RoomID: r1, Seq: 2}, {TenantID: globex, RoomID: r2, Seq: 1}, {TenantID: acme, RoomID: r2, Seq: 1}, {TenantID: acme, RoomID: r1, Seq: 7},
			})
			if err != nil {
				t.Fatal(err)
			}
			by := map[string]SearchSource{}
			for _, s := range got {
				by[s.TenantID+"/"+s.RoomID+"/"+strconv.FormatInt(s.Seq, 10)] = s
			}
			if len(by) != 3 {
				t.Fatalf("loaded %v, want 3 rows (wrong-tenant and missing keys omitted)", by)
			}
			if s := by[acme+"/"+r1+"/1"]; !s.Deleted || s.Text() != "" || s.Author != "maya" {
				t.Fatalf("deleted row = %+v", s)
			}
			if s := by[acme+"/"+r1+"/2"]; s.Deleted || s.Text() != "people only" || s.ChannelID != people.ChannelID || s.CreatedAtMs != 20 {
				t.Fatalf("people row = %+v", s)
			}
			if name == "mysql" && by[acme+"/"+r1+"/2"].ChannelKind != ChannelKindPeople {
				t.Fatalf("people row channel kind = %q", by[acme+"/"+r1+"/2"].ChannelKind)
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
			if want := []string{acme + "/" + r1 + "/1", acme + "/" + r1 + "/2", globex + "/" + r2 + "/1"}; !equalStrings(walked, want) {
				t.Fatalf("walk = %v, want %v", walked, want)
			}
		})
	}
}
