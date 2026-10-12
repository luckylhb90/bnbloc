package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPeopleChatRuleOwned: whether an agent may read or write a channel is
// decided by ChannelAdmitsAgents only. A service comparing a channel's kind
// with ChannelKindPeople itself is a second copy (the handoff import and
// activity's reply ingest had none).
func TestPeopleChatRuleOwned(t *testing.T) {
	re := regexp.MustCompile(`[!=]=\s*store\.ChannelKindPeople|store\.ChannelKindPeople\s*[!=]=|\.IsPeople\(\)`)
	root := filepath.Join("..", "..")
	for _, dir := range []string{"services", "internal", "core"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			// The store, and the room service that creates, lists and
			// protects channels (its listing never names the people chat,
			// the people chat is never renamed or deleted), own the kind.
			if p := filepath.ToSlash(path); strings.HasPrefix(p, "../../internal/store/") || strings.HasPrefix(p, "../../services/room/") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if re.Match(raw) {
				t.Errorf("%s compares a channel's kind itself; use store.ChannelAdmitsAgents", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

type kindReader map[string]string

func (k kindReader) ChannelKind(_ context.Context, _, channelID string) (string, bool, error) {
	if channelID == "broken" {
		return "", false, errors.New("db down")
	}
	kind, ok := k[channelID]
	return kind, ok, nil
}

func TestChannelAdmitsAgents(t *testing.T) {
	ctx := context.Background()
	r := kindReader{"people": ChannelKindPeople, "general": ""}
	for _, c := range []struct {
		reader  ChannelKindReader
		channel string
		admits  bool
		err     bool
	}{
		{r, "general", true, false},
		{r, "people", false, false},
		{r, "", true, false},
		{nil, "people", true, false},
		{r, "broken", false, true},
	} {
		admits, err := ChannelAdmitsAgents(ctx, c.reader, "room-1", c.channel)
		if admits != c.admits || (err != nil) != c.err {
			t.Errorf("%q: admits=%v err=%v", c.channel, admits, err)
		}
	}
}
