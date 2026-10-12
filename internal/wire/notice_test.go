package wire

import (
	"go/scanner"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestNoticesCarryKind: every notice line is built by JobNotice, so each
// carries payload.notice and the job tags (2026-10-12 review: the bridge's
// refusals and activity's sweeper wrote notices without payload.notice, so
// the feed rendered them as the agent's own replies and agents read them
// back as their own speech). No source outside this package sets the
// "notice" payload key or the notice sequence itself.
func TestNoticesCarryKind(t *testing.T) {
	var problems []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "core", "cmd"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "internal/wire/") {
			return
		}
		fset := token.NewFileSet()
		file := fset.AddFile(rel, fset.Base(), len(src))
		var sc scanner.Scanner
		sc.Init(file, src, nil, 0)
		for {
			pos, tok, lit := sc.Scan()
			if tok == token.EOF {
				break
			}
			if (tok == token.STRING && lit == `"notice"`) || (tok == token.IDENT && lit == "JobNoticeSeq") {
				problems = append(problems, rel+":"+fset.Position(pos).String()+": "+lit)
			}
		}
	})
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("a notice built by hand (use wire.JobNotice):\n  %s", strings.Join(problems, "\n  "))
	}
}

func TestJobNoticeShape(t *testing.T) {
	in := JobNotice{RoomID: "room-1", ChannelID: "ch", JobID: "j1", SourceSeq: 7, Kind: NoticeJobFailed,
		Text: "failed", OwnerUserID: "maya", Provider: "codex", FailReason: "turn_timeout", Extra: map[string]any{"notice": "spoofed"}}.Report()
	if in.AgentSessionID != JobNoticeSessionID("j1") || len(in.Messages) != 1 || in.Messages[0].Seq != JobNoticeSeq {
		t.Fatalf("batch = %+v", in)
	}
	p := in.Messages[0].Payload.(map[string]any)
	for k, want := range map[string]any{"notice": NoticeJobFailed, "role": "assistant", "text": "failed", "jobId": "j1",
		"sourceSeq": int64(7), "ownerUserId": "maya", "provider": "codex", "failReason": "turn_timeout", "messageId": "job-failed-job-j1"} {
		if p[k] != want {
			t.Errorf("payload[%s] = %v, want %v", k, p[k], want)
		}
	}
}
