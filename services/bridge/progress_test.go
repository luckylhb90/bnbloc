package bridge

import (
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// Every member of the room sees a turn's progress titles: what looks like a
// secret is masked before it leaves the bridge, and ordinary steps read as
// they were.
func TestProgressTitlesAreRedacted(t *testing.T) {
	hex := strings.Repeat("a1b2c3d4", 5)
	random := "Zx9Qw3Er7Ty1Ui5Op2As8Df4Gh6Jk0LmNb"
	for _, tc := range []struct{ in, secret string }{
		{"export OPENAI_API_KEY=abc123secretvalue && npm test", "abc123secretvalue"},
		{`curl -H "Authorization: Bearer tok.en-VALUE_123" https://api.example.com`, "tok.en-VALUE_123"},
		{"curl -u bob:hunter2pass https://x", "hunter2pass"},
		{"git clone https://bob:hunter2pass@example.com/x", "hunter2pass"},
		{`write config.json {"password": "p@ss w0rd"}`, "p@ss w0rd"},
		{"psql --password s3cr3t-pw -h db", "s3cr3t-pw"},
		{"login --api-key=k-8f7e6d", "k-8f7e6d"},
		{"echo DB_SECRET: 'quoted secret'", "quoted secret"},
		{"Basic dXNlcjpwYXNzd29yZA== sent", "dXNlcjpwYXNzd29yZA=="},
		{"use sk-proj-abcdefghijklmnop1234", "sk-proj-abcdefghijklmnop1234"},
		{"token ghp_abcdefghijklmnopqrstuvwxyz0123", "ghp_abcdefghijklmnopqrstuvwxyz0123"},
		{"verify eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2QT4f", "eyJhbGciOiJIUzI1"},
		{"sign with " + hex, hex},
		{"set header X " + random, random},
	} {
		got := jobActivity([]corepkg.TurnActivity{{Kind: "tool", Title: tc.in}})[0].Title
		if strings.Contains(got, tc.secret) || !strings.Contains(got, redacted) {
			t.Errorf("redact(%q) = %q; want %q masked", tc.in, got, tc.secret)
		}
	}
	for _, plain := range []string{
		"go test ./services/bridge/... -run TestBorrowerTakesASeatOfTheirOwn",
		"Read web/src/views/team/QuotaRequestsPanel.tsx",
		"grep -rn tokenBudget internal/roomlog",
		"Edit services/bridge/session_restore.go",
		"npm run build -- --mode production",
		"git log --oneline -5",
		`git commit --author="Bob Lee" -m "keep the session id"`,
	} {
		if got := redactTitle(plain); got != plain {
			t.Errorf("redact(%q) = %q; want it unchanged", plain, got)
		}
	}
}
