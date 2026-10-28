package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestPeopleChatRuleOwned: whether an agent may read or write a channel is
// decided by ChannelAdmitsAgents only. A service comparing a channel's kind
// with ChannelKindPeople itself, or reading a channel's NoAgents setting, is
// a second copy (the handoff import and activity's reply ingest had none).
func TestPeopleChatRuleOwned(t *testing.T) {
	re := regexp.MustCompile(`[!=]=\s*store\.ChannelKindPeople|store\.ChannelKindPeople\s*[!=]=|\.IsPeople\(\)|\.NoAgents\b`)
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "core"}}, func(rel string, src []byte) {
		// The store, and the room service that creates, lists and
		// protects channels (its listing never names the people chat,
		// the people chat is never renamed or deleted), own the kind.
		if strings.HasPrefix(rel, "internal/store/") || strings.HasPrefix(rel, "services/room/") {
			return
		}
		if re.Match(src) {
			t.Errorf("%s compares a channel's kind itself; use store.ChannelAdmitsAgents", rel)
		}
	})
}

type admissionReader map[string]ChannelAdmission

func (k admissionReader) ChannelAdmission(_ context.Context, _, channelID string) (ChannelAdmission, bool, error) {
	if channelID == "broken" {
		return ChannelAdmission{}, false, errors.New("db down")
	}
	a, ok := k[channelID]
	return a, ok, nil
}

// TestChannelAdmitsAgents: agents are refused the people chat and a channel
// whose settings bar them (NoAgents), admitted everywhere else; a failed
// read refuses with the error (fail closed).
func TestChannelAdmitsAgents(t *testing.T) {
	ctx := context.Background()
	r := admissionReader{
		"people":  {Kind: ChannelKindPeople},
		"general": {},
		"quiet":   {NoAgents: true},
	}
	for _, c := range []struct {
		reader  ChannelAdmissionReader
		channel string
		admits  bool
		err     bool
	}{
		{r, "general", true, false},
		{r, "people", false, false},
		{r, "quiet", false, false},
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
