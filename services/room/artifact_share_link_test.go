package room

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func shareBox(t *testing.T) *sealbox.Box {
	t.Helper()
	box, err := sealbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// A link's creator copies it again while it works: the token is stored sealed
// (never in clear), only the creator reads it back and is told it can, and
// everyone else, a dead link and a link without a sealed token get the same 404.
func TestShareLinkCopyAgain(t *testing.T) {
	e, _, now := sharesRoom(t)
	e.svc.ShareBox = shareBox(t)
	e.must(e.roomSrv, http.MethodPost, "/v1/rooms/files/members", "alice", map[string]any{"userId": "dave", "role": "member"})
	art := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "content": "x"})
	linkPath := func(shareID string) string { return sharesPath(art.ArtifactID) + "/" + shareID + "/link" }

	code, rep := e.share(http.MethodPost, sharesPath(art.ArtifactID), "bob", map[string]any{})
	if code != http.StatusCreated || !rep.Share.Copyable {
		t.Fatalf("create = %d %+v (the creator is told the link can be copied again)", code, rep.Share)
	}
	stored, err := e.svc.Artifacts.GetArtifactShare(testCtx(t), rep.Share.ShareID)
	if err != nil || stored.TokenSealed == "" || strings.Contains(stored.TokenSealed, rep.Token) {
		t.Fatalf("stored = %+v %v (the token is sealed, never in clear)", stored, err)
	}
	// Bound to its tenant and share: moved to another share it does not open.
	if _, err := e.svc.ShareBox.Open(stored.TokenSealed, shareSealAD(tenant.Default, "another")); err == nil {
		t.Fatal("a sealed token opened under another share id")
	}

	if code, got := e.share(http.MethodGet, linkPath(rep.Share.ShareID), "bob", nil); code != http.StatusOK || got.Token != rep.Token {
		t.Fatalf("the creator's copy = %d %+v, want the token", code, got)
	}
	for user, want := range map[string]int{
		"alice": http.StatusNotFound,  // the room's owner: may revoke, may not read
		"dave":  http.StatusNotFound,  // another member
		"carol": http.StatusForbidden, // not in the room
	} {
		if code, got := e.share(http.MethodGet, linkPath(rep.Share.ShareID), user, nil); code != want || got.Token != "" {
			t.Fatalf("%s reads the link = %d %+v, want %d and no token", user, code, got, want)
		}
	}
	// An org admin outside the room is refused too (she may revoke, not read).
	req, _ := http.NewRequest(http.MethodGet, e.roomSrv.URL+linkPath(rep.Share.ShareID), nil)
	svcauth.Key("").Assert(req, "olga", tenant.Default, "admin")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode == http.StatusOK {
		t.Fatalf("an org admin reads the link: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	// The answer is never cached.
	req, _ = http.NewRequest(http.MethodGet, e.roomSrv.URL+linkPath(rep.Share.ShareID), nil)
	svcauth.Key("").Assert(req, "bob", tenant.Default, "member")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the link's Cache-Control: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}

	// Only the creator's own listing says a link can be copied.
	_, mine := e.share(http.MethodGet, sharesPath(art.ArtifactID), "bob", nil)
	_, theirs := e.share(http.MethodGet, sharesPath(art.ArtifactID), "alice", nil)
	if len(mine.Shares) != 1 || !mine.Shares[0].Copyable || len(theirs.Shares) != 1 || theirs.Shares[0].Copyable {
		t.Fatalf("copyable: creator %+v, another member %+v", mine.Shares, theirs.Shares)
	}

	// Revoking clears the sealed token and ends the route.
	if code, _ := e.share(http.MethodDelete, sharesPath(art.ArtifactID)+"/"+rep.Share.ShareID, "bob", nil); code != http.StatusNoContent {
		t.Fatalf("revoke = %d", code)
	}
	if stored, _ = e.svc.Artifacts.GetArtifactShare(testCtx(t), rep.Share.ShareID); stored.TokenSealed != "" {
		t.Fatal("a revoked link keeps its sealed token")
	}
	if code, _ := e.share(http.MethodGet, linkPath(rep.Share.ShareID), "bob", nil); code != http.StatusNotFound {
		t.Fatalf("a revoked link's copy = %d, want 404", code)
	}

	// An expired link is refused too.
	_, short := e.share(http.MethodPost, sharesPath(art.ArtifactID), "bob", map[string]any{"expiresInHours": 1})
	*now += 2 * hourMs
	if code, _ := e.share(http.MethodGet, linkPath(short.Share.ShareID), "bob", nil); code != http.StatusNotFound {
		t.Fatalf("an expired link's copy = %d, want 404", code)
	}
}

// Without a key a link is created as before, shown once, and cannot be copied
// again; a link whose key changed is a 404, never a 500 or a wrong token.
func TestShareLinkWithoutKey(t *testing.T) {
	e, _, _ := sharesRoom(t)
	art := e.createArtifact("alice", map[string]any{"kind": "markdown", "title": "Spec", "content": "x"})
	code, rep := e.share(http.MethodPost, sharesPath(art.ArtifactID), "bob", map[string]any{})
	if code != http.StatusCreated || rep.Token == "" || rep.Share.Copyable {
		t.Fatalf("create without a key = %d %+v", code, rep.Share)
	}
	if stored, _ := e.svc.Artifacts.GetArtifactShare(testCtx(t), rep.Share.ShareID); stored.TokenSealed != "" {
		t.Fatal("a token was stored without a key")
	}
	path := sharesPath(art.ArtifactID) + "/" + rep.Share.ShareID + "/link"
	if code, _ := e.share(http.MethodGet, path, "bob", nil); code != http.StatusNotFound {
		t.Fatalf("copy without a key = %d, want 404", code)
	}
	// A link sealed under one key does not open under another.
	e.svc.ShareBox = shareBox(t)
	_, sealed := e.share(http.MethodPost, sharesPath(art.ArtifactID), "bob", map[string]any{})
	other, err := sealbox.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e.svc.ShareBox = other
	if code, got := e.share(http.MethodGet, sharesPath(art.ArtifactID)+"/"+sealed.Share.ShareID+"/link", "bob", nil); code != http.StatusNotFound || got.Token != "" {
		t.Fatalf("a link sealed under another key = %d %+v, want 404", code, got)
	}
}
