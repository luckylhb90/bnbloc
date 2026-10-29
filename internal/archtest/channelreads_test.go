package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// TestChannelReadsAskTheRule (docs/prd/2026-10-28-channels-and-threads.md
// §6 pre-mortem 4): a members-only section leaks through the first read
// path that forgets it, so every function in a service that reads a
// channel's content (its messages, its tasks, its schedules, its runs and
// jobs, its deliverables, its unread count, the room's channel list) either
// asks the one rule (store.MayReadChannel, through its service's owner of
// it) in the same function, or is listed below with the reason it may skip
// it. A new read path fails here until it does one or the other.
func TestChannelReadsAskTheRule(t *testing.T) {
	// The store reads that return a channel's content or a channel listing.
	reads := map[string]bool{
		"RoomHistory": true, "RoomFeed": true, "RoomRevisions": true, "UnreadCount": true,
		"ListMessageTasks": true, "GetMessageTask": true, "ListSchedules": true,
		"ListAgentRuns": true, "ListAgentJobs": true, "GetAgentRun": true,
		"ListArtifacts": true, "ArtifactAnchors": true, "GetArtifact": true, "ListApps": true,
		"SearchArtifacts": true, "Search": true, "GetRoomMessage": true,
		"ListChannels": true,
	}
	// What asking the rule looks like in each service (its owner of it).
	asks := []string{
		"MayReadChannel", "mayRead", "MaySeeSection", "MayKnowSection", "readerAccess", "memberAccess",
		"roomArtifact", "artifactChannels", "knownSections", "newReadableIn", "memberMayRead",
		"memberMayReadChannel", "channelReaders", "searchReaders", "newReaderFilter", "readable",
	}
	// Functions that read without asking, and why.
	exempt := map[string]string{
		// realtime
		"services/realtime/continue.go:Continue":           "the job of the decider's own card (the gateway admitted the card)",
		"services/realtime/dispatch.go:checkDispatchChain": "walks the dispatch chain's job ids for its depth, never content",
		"services/realtime/room_events.go:causeRoom":       "reads a job's room and client id to name an event's cause",
		"services/realtime/handoff.go:handleInternalFeed":  "the room service's handoff export only, which filters by the exporter's access (handoff_export.go)",
		"services/realtime/service.go:messageChannel":      "reads a message's channel id for its caller to check (edit, delete)",
		"services/realtime/threads.go:replyRef":            "reads the quoted message only when it is of the channel the poster may read (PostChat asked) or that thread's root",
		"services/realtime/threads.go:openPostThread":      "the forum post PostChat just stored, in a channel PostChat checked its author may read",
		// room
		"services/room/appdata.go:serveApp":                 "the app the request's signed app scope names (its ticket or its agent's job)",
		"services/room/channels.go:handleGetPeopleChat":     "the people chat is no section's",
		"services/room/my_apps.go:appProducer":              "the producer of an app the listing admitted",
		"services/room/appnotify.go:handleAppNotifyTargets": "the app the request's app scope names (serveApp admitted it)",
		"services/room/artifact_shares.go:resolveShare":     "a share link its maker published beyond the room (sharing a deliverable further is its author's act, C13)",
		// activity
		"services/activity/runs.go:replyRoomSeq":      "the reply sequence of a run the route admitted",
		"services/activity/team_plan.go:agentCost":    "an agent's cost sample (numbers, never content)",
		"services/activity/tool_use.go:handleToolUse": "tool names and counts across runs, never content (an organization admin's view)",
	}
	var problems []string
	used := map[string]bool{}
	Scan(t, Opts{Lang: Go, Roots: []string{"services/realtime", "services/room", "services/activity"}}, func(rel string, src []byte) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			read, asked := "", false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
						name := sel.Sel.Name
						if reads[name] && read == "" {
							read = name
						}
						for _, a := range asks {
							if name == a {
								asked = true
							}
						}
					} else if id, ok := n.Fun.(*ast.Ident); ok {
						for _, a := range asks {
							if id.Name == a {
								asked = true
							}
						}
					}
				case *ast.Ident:
					for _, a := range asks {
						if n.Name == a {
							asked = true
						}
					}
				}
				return true
			})
			if read == "" || asked {
				continue
			}
			key := rel + ":" + fn.Name.Name
			if _, ok := exempt[key]; ok {
				used[key] = true
				continue
			}
			problems = append(problems, key+" reads "+read)
		}
	})
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("a channel read that asks no store.MayReadChannel (or say why it may not, in this test):\n  %s", strings.Join(problems, "\n  "))
	}
	var stale []string
	for k := range exempt {
		if !used[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("exemptions that no longer match a channel read (remove them):\n  %s", strings.Join(stale, "\n  "))
	}
}
