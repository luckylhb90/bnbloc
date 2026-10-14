package bridge

import (
	"context"
	"log/slog"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A running turn's progress (docs/prd/2026-10-13-live-turns.md §3): reflow
// relays the core's turn activity (TurnStatus.StartedAtMs, Activity) to the
// room as agent_job_progress, through activity (POST
// /v1/bridge/jobs/{jobId}/progress). Transient and best-effort: a failed
// relay never touches the turn.

// progressEvery bounds how often one turn's progress is relayed.
const progressEvery = 2 * time.Second

// progressRelay is what one turn last relayed, and when.
type progressRelay struct {
	last   wire.JobProgressInput
	sentAt time.Time
}

// relayProgress reports ts's activity for job when it changed and the
// last report is progressEvery old.
func (b *Bridge) relayProgress(ctx context.Context, jobID string, ts corepkg.TurnStatus, p *progressRelay) {
	if jobID == "" || (ts.StartedAtMs == 0 && len(ts.Activity) == 0) {
		return
	}
	in := wire.JobProgressInput{BridgeID: b.bridgeID(), StartedAtMs: ts.StartedAtMs, Activity: jobActivity(ts.Activity)}
	if reflect.DeepEqual(in, p.last) || time.Since(p.sentAt) < progressEvery {
		return
	}
	p.last, p.sentAt = in, time.Now()
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := b.gatewayPOST(rctx, "/v1/bridge/jobs/"+url.PathEscape(jobID)+"/progress", in); err != nil {
		slog.Debug("bnbloc bridge progress relay failed", "event", "bnbloc.bridge.progress_failed", "job", jobID, "error", err)
	}
}

// jobActivity maps a core's turn activity onto the wire, held to its
// bounds (the newest MaxJobActivity steps, titles redacted and cut).
func jobActivity(in []corepkg.TurnActivity) []wire.JobActivity {
	if len(in) > wire.MaxJobActivity {
		in = in[len(in)-wire.MaxJobActivity:]
	}
	out := make([]wire.JobActivity, 0, len(in))
	for _, a := range in {
		title := redactTitle(strings.TrimSpace(a.Title))
		if utf8.RuneCountInString(title) > wire.MaxJobActivityTitle {
			title = string([]rune(title)[:wire.MaxJobActivityTitle-1]) + "…"
		}
		out = append(out, wire.JobActivity{Kind: a.Kind, Title: title, AtMs: a.AtMs})
	}
	return out
}

// Progress titles are what the core says a step does — often the command
// it runs — and every member of the room sees them
// (docs/prd/2026-10-14-multi-member-rooms.md §2b). redactTitle is the one
// owner of masking what looks like a secret in one before it leaves the
// bridge: a value assigned to a secret-looking name (KEY=…, "token": …,
// --password …, an auth header), a Bearer/Basic credential, URL user
// info and -u user:password, well-known token shapes, and long hex or random-looking runs.
// The name stays, the value reads [redacted].
const redacted = "[redacted]"

var (
	// secretName is a name whose value is a secret.
	secretName = `[A-Za-z0-9_.-]*(?i:key|token|secret|passw(?:or)?d|pwd|authori[sz]ation|credential|cookie)[A-Za-z0-9_.-]*`
	// secretValue is the value it is given: quoted, or up to a separator,
	// with an optional auth scheme before it.
	secretValue = `(?:(?i:bearer|basic|token)\s+)?(?:"[^"]*"|'[^']*'|[^\s"',;&)]+)`

	titleRedactions = []struct {
		re   *regexp.Regexp
		with string
	}{
		// scheme://user:password@host
		{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s/@:]+:[^\s/@]+@`), "${1}" + redacted + "@"},
		// curl -u user:password, --user user:password
		{regexp.MustCompile(`((?:^|\s)(?:-u|--user)(?:=|\s+)[^\s:]+:)\S+`), "${1}" + redacted},
		// NAME=value, NAME: value, "name": "value"
		{regexp.MustCompile(`(["']?` + secretName + `["']?\s*[:=]\s*)` + secretValue), "${1}" + redacted},
		// --password value, --api-key=value
		{regexp.MustCompile(`(--?` + secretName + `(?:=|\s+))` + secretValue), "${1}" + redacted},
		// Bearer / Basic credentials anywhere
		{regexp.MustCompile(`((?i:bearer|basic)\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}" + redacted},
		// JWTs and well-known token prefixes
		{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), redacted},
		{regexp.MustCompile(`\b(?:(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|xox[abposr]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})`), redacted},
		// long hex (keys, digests)
		{regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`), redacted},
	}
	// randomRun is a candidate base64/random run; randomLooking decides.
	randomRun = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
)

func redactTitle(title string) string {
	for _, r := range titleRedactions {
		title = r.re.ReplaceAllString(title, r.with)
	}
	return randomRun.ReplaceAllStringFunc(title, func(run string) string {
		for _, seg := range strings.Split(run, "/") {
			if randomLooking(seg) {
				return redacted
			}
		}
		return run
	})
}

// randomLooking reports whether a run reads as random (a key, not a name):
// 32 characters or more mixing upper and lower case with several digits.
func randomLooking(s string) bool {
	if len(s) < 32 {
		return false
	}
	var upper, lower, digits int
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
			upper++
		case c >= 'a' && c <= 'z':
			lower++
		case c >= '0' && c <= '9':
			digits++
		}
	}
	return upper > 0 && lower > 0 && digits >= 3
}
