package activity

import (
	"net/http"
	"sort"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The room tool use read (docs/prd/2026-10-20-fewer-sharper-room-tools.md
// §2.3): GET /v1/tenant/jobs/tool-calls?from=&to= sums the room tool calls
// the organization's runs recorded (wire.RunDiagTools, the bridge's report
// of each call's tool, action and outcome) by tool and action over the runs
// created in [from, to): how often each was called, refused and failed.
// Behind the gateway's manage_org gate like every /v1/tenant/* path. The
// listing filters (owner, core, provider, status) narrow it the same way.

// defaultToolUseRange is the range a read without from covers.
const defaultToolUseRange = 7 * 24 * time.Hour

func (s *Service) handleToolUse(w http.ResponseWriter, r *http.Request) {
	if s.Runs == nil {
		httpx.Unavailable(w, "run records")
		return
	}
	q, err := parseRunQuery(r)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if q.ToMs == 0 {
		q.ToMs = s.clock().UnixMilli() + 1
	}
	if q.FromMs == 0 {
		q.FromMs = q.ToMs - defaultToolUseRange.Milliseconds()
	}
	if q.FromMs >= q.ToMs {
		httpx.Error(w, "from must be before to", http.StatusBadRequest)
		return
	}
	q.Limit = store.MaxAgentRunsPage
	q.Before = store.AgentRunCursor{}
	out := wire.RoomToolUseReply{FromMs: q.FromMs, ToMs: q.ToMs, Tools: []wire.RoomToolUse{}}
	rows := map[[2]string]*wire.RoomToolUse{}
	oldest := int64(0)
pages:
	for {
		jobs, err := s.Runs.ListAgentRuns(r.Context(), q)
		if err != nil {
			obs.ServerError(w, "read tool use failed", err)
			httpx.Error(w, "read tool use failed", http.StatusInternalServerError)
			return
		}
		for _, j := range jobs {
			if out.Runs == wire.MaxToolUseRuns {
				out.Truncated, out.OldestMs = true, oldest
				break pages
			}
			out.Runs++
			oldest = j.CreatedAtMs
			tools, ok := store.RunDiag[wire.AgentRunTools](j, wire.RunDiagTools)
			if !ok {
				continue
			}
			out.Dropped += tools.CallsDropped
			for _, c := range tools.Calls {
				k := [2]string{c.Tool, c.Action}
				row := rows[k]
				if row == nil {
					row = &wire.RoomToolUse{Tool: c.Tool, Action: c.Action}
					rows[k] = row
				}
				row.Calls++
				switch c.Outcome {
				case wire.ToolCallRefused:
					row.Refused++
				case wire.ToolCallError:
					row.Errors++
				}
			}
		}
		if len(jobs) < q.PageSize() {
			break
		}
		q.Before = store.CursorOf(jobs[len(jobs)-1])
	}
	for _, row := range rows {
		out.Tools = append(out.Tools, *row)
	}
	sort.Slice(out.Tools, func(i, k int) bool {
		a, b := out.Tools[i], out.Tools[k]
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		if a.Tool != b.Tool {
			return a.Tool < b.Tool
		}
		return a.Action < b.Action
	})
	httpx.WriteJSON(w, http.StatusOK, out)
}
