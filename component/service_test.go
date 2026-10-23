package component

import (
	"strings"
	"testing"
	"time"
)

// A signature covers the method, the path and query, the identity and the
// body: changing any of them fails it, and so does an old timestamp.
func TestSignedRequestCoversEveryPart(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	sig := Sign("s3cret", now, SignedRequest("POST", "/turn/egress/request?a=1", "turn", []byte("body")))
	if err := Verify(sig, "s3cret", SignedRequest("POST", "/turn/egress/request?a=1", "turn", []byte("body")), now, time.Minute); err != nil {
		t.Fatal(err)
	}
	for name, signed := range map[string][]byte{
		"method":   SignedRequest("GET", "/turn/egress/request?a=1", "turn", []byte("body")),
		"path":     SignedRequest("POST", "/turn/facts?a=1", "turn", []byte("body")),
		"query":    SignedRequest("POST", "/turn/egress/request?a=2", "turn", []byte("body")),
		"identity": SignedRequest("POST", "/turn/egress/request?a=1", "other", []byte("body")),
		"body":     SignedRequest("POST", "/turn/egress/request?a=1", "turn", []byte("bodx")),
	} {
		if Verify(sig, "s3cret", signed, now, time.Minute) == nil {
			t.Errorf("a changed %s still verifies", name)
		}
	}
	if Verify(sig, "s3cret", SignedRequest("POST", "/turn/egress/request?a=1", "turn", []byte("body")), now.Add(time.Hour), time.Minute) != ErrSignatureExpired {
		t.Error("an old signature verifies")
	}
}

func TestEgressHostsFactsAndCardsBounds(t *testing.T) {
	for h, ok := range map[string]bool{"wiki.example.com": true, "api.github.com": true, "localhost": false, "WIKI.example.com": false,
		"10.0.0.1": false, "wiki.example.com:443": false, "-a.example.com": false, "": false} {
		if ValidEgressHost(h) != ok {
			t.Errorf("ValidEgressHost(%q) = %v", h, !ok)
		}
	}
	if err := CheckFacts(Facts{Line: "wiki, issues", Files: map[string]string{"INDEX.md": "x", "systems/wiki.md": "y"}}); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]Facts{
		"two lines":  {Line: "a\nb"},
		"long line":  {Line: strings.Repeat("x", MaxFactsLine+1)},
		"dotdot":     {Files: map[string]string{"../x.md": ""}},
		"hidden":     {Files: map[string]string{".x": ""}},
		"too large":  {Files: map[string]string{"a.md": strings.Repeat("x", MaxFactsBytes+1)}},
		"absolute":   {Files: map[string]string{"/etc/passwd": ""}},
		"odd letter": {Files: map[string]string{"a b.md": ""}},
	} {
		if CheckFacts(f) == nil {
			t.Errorf("facts with %s pass", name)
		}
	}
	good := Card{Title: "Connect the wiki", Text: "Your agent tried to read the wiki.", Actions: []CardAction{
		{Kind: CardOpen, Label: "Connect", Page: "connections", Query: "system=wiki"}, {Kind: CardAskAgain, Label: "Ask again"}}}
	if err := CheckCard(good); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Card{
		"no title":      {},
		"unknown kind":  {Title: "t", Actions: []CardAction{{Kind: "run", Label: "x"}}},
		"no page":       {Title: "t", Actions: []CardAction{{Kind: CardOpen, Label: "x"}}},
		"too many":      {Title: "t", Actions: []CardAction{{Kind: CardAskAgain, Label: "a"}, {Kind: CardAskAgain, Label: "b"}, {Kind: CardAskAgain, Label: "c"}, {Kind: CardAskAgain, Label: "d"}}},
		"label missing": {Title: "t", Actions: []CardAction{{Kind: CardAskAgain}}},
	} {
		if CheckCard(c) == nil {
			t.Errorf("a card with %s passes", name)
		}
	}
}
