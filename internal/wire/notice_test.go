package wire

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestNoticesCarryKind: every notice line is built by JobNotice, so each
// carries payload.notice and the job tags (2026-10-12 review: the bridge's
// refusals and activity's sweeper wrote notices without payload.notice, so
// the feed rendered them as the agent's own replies and agents read them
// back as their own speech). No source outside this package sets the
// "notice" payload key or the notice sequence itself.
func TestNoticesCarryKind(t *testing.T) {
	root := filepath.Join("..", "..")
	var problems []string
	for _, dir := range []string{"services", "internal", "core", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/wire/") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fset := token.NewFileSet()
			file := fset.AddFile(path, fset.Base(), len(src))
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
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
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
