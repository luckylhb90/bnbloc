package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// docs/bugs/2026-10-13-attachments-dropped-silently.md: a message's
// attachment that cannot reach the agent's workspace is named in the turn's
// prompt with why, and in one notice to the room; it is never dropped
// silently. A core that refuses the write (a capped request body) and a
// core that states a smaller limit (never sent) are both reported; a file
// that fits arrives as before.
func TestUndeliveredAttachmentsAreNamed(t *testing.T) {
	g := newPullGateway(t)
	g.attachments["att_small"] = testAttachment{name: "shot.png", contentType: "image/png", data: []byte("tiny png")}
	g.attachments["att_big"] = testAttachment{name: "brand.png", contentType: "image/png", data: make([]byte, 3000)}
	c := newFSCore(t)
	c.maxBody = 1000
	b := fsBridge(t, g, c, "")

	drive := func(id string, atts ...string) {
		t.Helper()
		d := testDrive(b, jobRef{ID: id, RequesterUserID: "maya", MessageSeq: 7}, "r1", "maya", "core-a", "agent-a", "t", "use the brand", "general")
		d.job.AttachmentIDs = atts
		if _, err := b.driveClaim(context.Background(), d, nil); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}

	// A fresh session: the small file is written and opened, the big one
	// refused by the core and named.
	drive("job-1", "att_small", "att_big")
	ws := filepath.Join(c.root, "ws1")
	if got, err := os.ReadFile(filepath.Join(ws, ".room/attachments/att_small.png")); err != nil || string(got) != "tiny png" {
		t.Fatalf("delivered file = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".room/attachments/att_big.png")); err == nil {
		t.Fatal("the refused file is in the workspace")
	}
	c.mu.Lock()
	prompt := c.sessions[0].turns[0]
	c.mu.Unlock()
	for _, want := range []string{
		"- .room/attachments/att_small.png (original name \"shot.png\", image/png)",
		"did NOT reach this workspace",
		"- \"brand.png\" (image/png, 3 KB): this agent's runtime refused it (",
		"invalid json body",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	notices := attachmentNotices(g)
	if len(notices) != 1 || !strings.Contains(notices[0], `"text":"Could not give agent-a the attachment brand.png (3 KB): this agent's runtime refused it (`) ||
		!strings.Contains(notices[0], `"jobId":"job-1"`) || !strings.Contains(notices[0], `"sourceSeq":7`) ||
		!strings.Contains(notices[0], `"agentSessionId":"`+wire.AttachmentNoticeSessionID("job-1")+`"`) {
		t.Fatalf("notices = %q", notices)
	}

	// The core now states its limit: the big file is not even fetched, and
	// the follow-up turn (the same session) says why.
	c.mu.Lock()
	c.maxBody, c.maxInput = 0, protocol.WriteFileBodyBytes(999)
	c.mu.Unlock()
	g.mu.Lock()
	fetched := g.downloads["att_big"]
	g.mu.Unlock()
	drive("job-2", "att_big")
	g.mu.Lock()
	refetched := g.downloads["att_big"]
	g.mu.Unlock()
	if refetched != fetched {
		t.Fatalf("a file over the stated limit was downloaded (%d → %d)", fetched, refetched)
	}
	c.mu.Lock()
	turns := c.sessions[len(c.sessions)-1].turns
	followUp := turns[len(turns)-1]
	c.mu.Unlock()
	if !strings.Contains(followUp, "- \"brand.png\" (image/png, 3 KB): too large for this agent's runtime (it accepts files up to 999 bytes)") {
		t.Fatalf("follow-up prompt:\n%s", followUp)
	}
	if n := attachmentNotices(g); len(n) != 2 || !strings.Contains(n[1], "too large for this agent's runtime") {
		t.Fatalf("notices = %q", n)
	}

	// Every file fits: no block, no notice.
	drive("job-3", "att_small")
	c.mu.Lock()
	turns = c.sessions[len(c.sessions)-1].turns
	last := turns[len(turns)-1]
	c.mu.Unlock()
	if strings.Contains(last, "did NOT reach") || len(attachmentNotices(g)) != 2 {
		t.Fatalf("a delivered file was reported undelivered:\n%s", last)
	}
}

// attachmentNotices are the attachment-undelivered notices posted so far.
func attachmentNotices(g *pullGateway) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, r := range g.reports {
		if strings.Contains(r, `"`+wire.NoticeAttachmentUndelivered+`"`) {
			out = append(out, r)
		}
	}
	return out
}

// An attachment is written with its type's extension, keeping the name's
