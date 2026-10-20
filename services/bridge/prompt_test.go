package bridge

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden prompts in testdata")

func goldenFile(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || got != string(want) {
		t.Fatalf("%s differs from %s (%v; run with -update after reviewing):\n%s", name, path, err, got)
	}
}

// promptContent is an expert with one skill pack, as a core without
// agentProfile wears it.
func promptContent() *jobContent {
	return &jobContent{
		expert:       &wire.ContentRef{Name: "reviewer", DisplayName: "Reviewer", Version: "1.0.0"},
		instructions: "You review diffs for correctness.",
		skills:       []corepkg.ProfileSkill{{Name: "checklist"}},
		summaries:    []skillSummary{{name: "checklist", description: "Use when reviewing a diff.", body: "Run the tests first."}},
	}
}

// The content's prompt prefix on a core without agentProfile is pinned:
// skill files written into the workspace, and inlined where it takes none.
func TestPromptPrefixGolden(t *testing.T) {
	goldenFile(t, "prompt_prefix_files.txt", promptContent().promptPrefix(true))
	goldenFile(t, "prompt_prefix_inline.txt", promptContent().promptPrefix(false))
}

// tokens is the budget unit: characters / 4, rounded up.
func tokens(s string) int { return (len(s) + 3) / 4 }

// TestPromptBudgets bounds every fixed model-facing surface. The per-turn
// ones ride every turn; the session prefix (guidance + tools/list) rides
// every request of a session as a cached prefix. A per-turn or guidance
// budget that no longer holds is a decision: trim the text, or raise the
// budget in review. The room tools' budgets are never raised (below).
func TestPromptBudgets(t *testing.T) {
	entry := roomlog.Entry{Seq: 1, Speaker: "maya", Text: "x", To: "maya/codex"}
	line := roomlog.Line(entry)
	now := roomlog.Now{From: "jack/codex for maya"}
	att := []roomlog.Attachment{{Path: ".room/attachments/att_1.png", Filename: "shot.png", ContentType: "image/png"}}
	undelivered := []roomlog.UndeliveredAttachment{{Filename: "brand.png", ContentType: "image/png", Size: 1708534, Reason: undeliveredTooLarge(2 << 20)}}
	ch := roomlog.GuidanceChannel("ch_0123456789abcdef")
	guidance := roomlog.SessionGuidance(roomlog.Guidance{Self: "maya/codex", HasTools: true, HasRoomFiles: true, Channel: ch})
	team := roomlog.SessionGuidance(roomlog.Guidance{Self: "maya/codex", HasTools: true, HasRoomFiles: true, TeamLead: true, Channel: ch})
	var tools bytes.Buffer
	enc := json.NewEncoder(&tools)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"tools": roomtools.DefaultRegistry().Descriptors()}); err != nil {
		t.Fatal(err)
	}
	toolsList := strings.TrimSpace(tools.String())
	for _, c := range []struct {
		name   string
		text   string
		budget int
	}{
		// Per turn, without the message's own text.
		{"pull turn, profile core", roomlog.ComposePointer(now, false), 60},
		{"pull turn, core without profile", roomlog.ComposePointer(now, true), 60},
		{"push follow-up, nothing new", roomlog.ComposeDelta(nil, now, roomlog.Options{}), 60},
		{"push follow-up framing (one new line excluded)", strings.Replace(roomlog.ComposeDelta([]roomlog.Entry{entry}, now, roomlog.Options{}), line, "", 1), 60},
		{"push first framing (one line excluded)", strings.Replace(roomlog.Compose([]roomlog.Entry{entry}, now, roomlog.Options{}), line, "", 1), 60},
		{"attachments block, one file", strings.Replace(roomlog.ComposePointer(roomlog.Now{From: now.From, Attachments: att}, false), roomlog.ComposePointer(now, false), "", 1), 50},
		{"undelivered block, one file", strings.Replace(roomlog.ComposePointer(roomlog.Now{From: now.From, Undelivered: undelivered}, false), roomlog.ComposePointer(now, false), "", 1), 70},
		// The one line of a request about a room file (file_edit.go).
		{"edit line", strings.Replace(roomlog.ComposePointer(roomlog.Now{From: now.From, Edit: &roomlog.EditTarget{
			Path: "edits/Q3 board deck.pptx", Source: ".room/edits/artf_0123456789abcdef01234567/Q3 board deck.pptx", Name: "Q3 board deck.pptx", Version: 12,
		}}, false), roomlog.ComposePointer(now, false), "", 1), 60},
		// Once per session (cached prefix). +40 on 2026-10-14 for the two
		// rules that keep work out of /tmp and many images out of the
		// conversation (docs/prd/2026-10-14-context-overflow-recovery.md).
		// -50 on 2026-10-16: tools/list lists each room tool in brief and room_help
		// gives the full instructions on demand (roomtools/brief.go), so the
		// guidance no longer repeats the tools' rules; one line says the person
		// sees none of the tools or files; +30 for the line that timed work is a
		// schedule, not a timer in a page or the agent's own scheduler; +60 on
		// 2026-10-16 for the notice form of schedules (fixed words, or an AI writing
		// the body) beside the AI task.
		{"session guidance (tools, .room/)", guidance, 745},
		{"session guidance (tools, .room/, team lead)", team, 825},
		// The room tools only shrink (docs/prd/2026-10-20-fewer-sharper-room-tools.md
		// §2.3): a new tool or action fits these budgets by merging or
		// shortening, never by raising them. Each is the size measured at its
		// last cut, rounded up to the nearest 10. 2026-10-20: the five content
		// tools became one components tool (list get try save publish) and
		// room_setup's brief one sentence and its actions, details in
		// room_help: tools/list 2,351 -> 1,787, session prefix 3,090 -> 2,520.
		{"tools/list", toolsList, 1790},
		{"session prefix (guidance + tools/list)", guidance + toolsList, 2520},
	} {
		got := tokens(c.text)
		t.Logf("%-48s %5d tokens (budget %d)", c.name, got, c.budget)
		if got > c.budget {
			t.Errorf("%s is %d tokens, over its budget of %d:\n%s", c.name, got, c.budget, c.text)
		}
	}
}
