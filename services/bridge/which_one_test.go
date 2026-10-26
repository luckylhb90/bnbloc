package bridge

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// A session, an agent, a workspace or a core is chosen by its key, never as "the first of a
// list" or "the latest": code in the bridge that acts on a thing it already named holds its
// key (sessionKey, driveKey) and uses it (AGENTS.md "Refer to a thing by its identity",
// design-review lens 7b). Picking [0] of a list of agents, targets, cores or sessions, or the
// most recently driven one, is a choice nobody was shown: it needs a `decision:` comment
// naming where the owner decided it (a PRD "Decisions" entry), or a key instead.
//
// This is a fence around the spellings seen so far, not a proof: a new spelling needs a new
// line here, and the Decision gate (AGENTS.md "Workflow") is what catches the ones nobody has
// written down yet.
var firstOfThings = regexp.MustCompile(`\b(?:agents?|targets?|cores?|sessions?|workspaces?|rooms?|recipients?|hosts?)\s*\[\s*0\s*\]`)

func TestNoFirstOfAListAsTheTarget(t *testing.T) {
	var found []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services/bridge", "services/gateway", "services/room", "services/activity"}}, func(rel string, src []byte) {
		for i, line := range strings.Split(string(src), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "//") || strings.Contains(line, "decision:") {
				continue
			}
			if firstOfThings.MatchString(line) {
				found = append(found, rel+":"+strconv.Itoa(i+1)+": "+trim)
			}
		}
	})
	if len(found) > 0 {
		t.Fatalf("the first of a list chosen as the target (use the key; or add a `decision: <PRD §>` comment where the owner decided it):\n%s", strings.Join(found, "\n"))
	}
}
