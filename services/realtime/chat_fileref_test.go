package realtimehttp

import (
	"errors"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A message about a room file (the workbench's Ask AI panel) carries the
// file, completed with its version and name, in its payload and on the
// agent job; a file of another room, a version it lacks and a web app are
// refused.
func TestChatFileRefValidatedAndCarried(t *testing.T) {
	svc, _, _, _ := expertFixture(t)
	arts := store.NewMemoryArtifacts()
	svc.Artifacts = arts
	put := func(id, room, kind string, versions int) {
		if _, err := arts.PutArtifact(dctx(), store.Artifact{ArtifactID: id, TenantID: tenant.Default, RoomID: room, Kind: kind, Title: "work/out/" + id + ".pptx", FileName: id + ".pptx", CreatedBy: "jack"}); err != nil {
			t.Fatal(err)
		}
		for v := 0; v < versions; v++ {
			if _, _, err := arts.PublishArtifactVersion(dctx(), id, store.ArtifactVersion{Content: "v", AuthorUserID: "jack"}, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	put("deck", "room-1", store.ArtifactKindFile, 3)
	put("other", "room-2", store.ArtifactKindFile, 1)
	put("site", "room-1", store.ArtifactKindSite, 0)

	posted, err := svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "maya", Text: "shorten slide 4's title", Recipient: toJackCodex(), FileRef: &wire.FileRef{ArtifactID: "deck"}})
	if err != nil {
		t.Fatal(err)
	}
	want := wire.FileRef{ArtifactID: "deck", Version: 3, Name: "deck.pptx"}
	if j := posted.Job; j == nil || j.FileRef == nil || *j.FileRef != want {
		t.Fatalf("job file ref = %+v", posted.Job)
	}
	feed, err := svc.Chat.RoomFeed(dctx(), "room-1", 0, 10)
	if err != nil || len(feed) == 0 {
		t.Fatalf("feed = %v, %v", feed, err)
	}
	if got := string(feed[len(feed)-1].Payload); !strings.Contains(got, `"fileRef":{"artifactId":"deck","version":3,"name":"deck.pptx"}`) {
		t.Fatalf("payload = %s", got)
	}
	if posted, err = svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "maya", Text: "as it was", Recipient: toJackCodex(), FileRef: &wire.FileRef{ArtifactID: "deck", Version: 2}}); err != nil || posted.Job.FileRef.Version != 2 {
		t.Fatalf("explicit version = %+v, %v", posted.Job, err)
	}
	for name, ref := range map[string]*wire.FileRef{
		"another room's": {ArtifactID: "other"},
		"unknown":        {ArtifactID: "gone"},
		"version beyond": {ArtifactID: "deck", Version: 4},
		"negative":       {ArtifactID: "deck", Version: -1},
		"a web app":      {ArtifactID: "site"},
		"no id":          {},
	} {
		if _, err := svc.PostChat(dctx(), ChatPost{RoomID: "room-1", Author: "maya", Text: "x", Recipient: toJackCodex(), FileRef: ref}); !errors.As(err, new(errBadFileRef)) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
