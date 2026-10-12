package contentshare

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// One announcement per member with the one dedup key, whichever path gave
// the copy: a share to two members and a later join notify each once.
func TestAnnounceOneKeyPerMember(t *testing.T) {
	src := store.ContentComponent{ID: "cc1", Name: "reviewer", Kind: "bnbloc.expert.package/v1"}
	direct := Announce(Share{Source: src, Version: "1.0.0", SharedBy: "maya"}, []string{"jack", "bob"})
	joined := Announce(Share{Source: src, Version: "1.0.0", SharedBy: "maya", ViaRoom: true}, []string{"jack"})
	if len(direct) != 2 || direct[0].DedupKey != joined[0].DedupKey || direct[0].DedupKey == direct[1].DedupKey {
		t.Fatalf("keys: direct %q %q, joined %q", direct[0].DedupKey, direct[1].DedupKey, joined[0].DedupKey)
	}
	if len(direct[0].Recipients) != 1 || direct[0].Recipients[0] != "jack" {
		t.Fatalf("recipients = %v", direct[0].Recipients)
	}
	if !strings.Contains(joined[0].Text, "joining a room") || strings.Contains(direct[0].Text, "joining a room") {
		t.Fatalf("texts: %q / %q", direct[0].Text, joined[0].Text)
	}
}

// TestShareAnnouncedOnlyHere: the content_shared notification is built only
// by Announce (2026-10-12 review: the gateway keyed it on the recipient
// list, realtime on the member, so a member could hear twice).
func TestShareAnnouncedOnlyHere(t *testing.T) {
	re := regexp.MustCompile(`Type:\s*wire\.NotificationContentShared`)
	root := filepath.Join("..", "..")
	for _, dir := range []string{"services", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if strings.HasPrefix(filepath.ToSlash(path), "../../internal/contentshare/") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if re.Match(raw) {
				t.Errorf("%s builds a content_shared notification; use contentshare.Announce", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
