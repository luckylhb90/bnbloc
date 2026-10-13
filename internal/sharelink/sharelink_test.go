package sharelink

import (
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/digest"
)

// A minted token is valid, unique, made of the whole alphabet (rejection
// sampling over every character) and hashes stably to what the store
// keeps; Path, URL and PagePath carry it unchanged and nothing else is a
// token.
func TestTokenRoundTrip(t *testing.T) {
	seen := map[string]bool{}
	chars := map[rune]bool{}
	for i := 0; i < 2000; i++ {
		token, hash := New()
		if !Valid(token) || len(token) != TokenLen {
			t.Fatalf("minted %q is not a token", token)
		}
		if seen[token] {
			t.Fatalf("token %q minted twice", token)
		}
		seen[token] = true
		if hash != Hash(token) || !digest.Valid(hash) || strings.Contains(hash, token) {
			t.Fatalf("hash of %q = %q", token, hash)
		}
		for _, c := range token {
			chars[c] = true
		}
		if got, ok := strings.CutPrefix(Path(token), Prefix); !ok || got != token {
			t.Fatalf("Path(%q) = %q", token, Path(token))
		}
		if got, ok := strings.CutPrefix(PagePath(token), "/#/share/"); !ok || got != token {
			t.Fatalf("PagePath(%q) = %q", token, PagePath(token))
		}
	}
	if len(chars) != len(alphabet) {
		t.Fatalf("2000 tokens used %d of the %d characters", len(chars), len(alphabet))
	}
	token, _ := New()
	for _, bad := range []string{"", token[:TokenLen-1], token + "a", token[:TokenLen-1] + "-", token[:TokenLen-1] + "_",
		"bsh_0123456789abcdef0123456789abcdef_c2VjcmV0", strings.Repeat("é", TokenLen/2)} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
	if Hash("a") == Hash("b") || Hash(token) != digest.Of([]byte(token)) {
		t.Fatal("Hash is not the token's sha256")
	}
}

// A link is short: under a typical public origin it is at most 55
// characters ("about 50", docs/prd/2026-10-13-short-share-links.md).
func TestLinkIsShort(t *testing.T) {
	token, _ := New()
	for _, origin := range []string{"https://bnbloc.example.com", "https://links.acme-corp.io", "https://app.bnbloc.io"} {
		link := URL(origin, token)
		if link != origin+"/s/"+token || len(link) > 55 {
			t.Errorf("URL(%q) = %q, %d characters (at most 55)", origin, link, len(link))
		}
	}
}

// TestShareLinkSpelledOnce: the link's path ("/s/") and the share page it
// opens ("#/share/") are spelled by this package alone in Go; in the web
// the share page's route is spelled by its hash router alone
// (web/src/lib/hashRoute.ts), and no web code builds a share link: the
// create answer carries it (wire.ShareCreated.URL).
func TestShareLinkSpelledOnce(t *testing.T) {
	linkPath := regexp.MustCompile("[\"'`]/s/|/s/[\"'`$]")
	page := regexp.MustCompile(`#/share/`)
	const owner = "internal/sharelink/sharelink.go"
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(rel string, src []byte) {
		if rel == owner {
			return
		}
		if linkPath.Match(src) || page.Match(src) {
			t.Errorf("%s spells a share link or its page; internal/sharelink is the one owner", rel)
		}
	})
	const webPage = "web/src/lib/hashRoute.ts"
	archtest.Scan(t, archtest.Opts{Lang: archtest.TS, Roots: []string{"web/src"}}, func(rel string, src []byte) {
		if linkPath.Match(src) {
			t.Errorf("%s builds a share link; the server's create answer carries it (wire.ShareCreated.URL)", rel)
		}
		if rel != webPage && page.Match(src) {
			t.Errorf("%s spells the share page's route; %s owns it (shareHash, shareTokenFromHash)", rel, webPage)
		}
	})
}
