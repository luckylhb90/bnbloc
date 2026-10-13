package roomlog

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"strings"
	"text/template"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Session guidance: what every agent must know about a bnbloc room before
// it reasons about anyone else's work (docs/bugs/2026-10-12-agent-misreads-
// other-agents-work.md), and how to use the room's tools when it has them.
// An agent that did not know each agent has its own workspace looked for
// another agent's files in its own, found none, and declared them never
// written. The guidance is stated once per session, here and only here
// (one go:embed template rendered with the session's facts): the bridge
// hands it to a new session (its agent profile on a core with agentProfile,
// else its first prompt), and .room/README.md keeps the same text in the
// pull model's workspace, written only when it changes. The per-turn prompt
// never repeats it, and no other package words it (the room tools' MCP
// instructions point here).

//go:embed guidance.tmpl
var guidanceSource string

var guidanceTemplate = template.Must(template.New("guidance").Option("missingkey=error").Parse(guidanceSource))

// Guidance is the facts a session's guidance is rendered with.
type Guidance struct {
	// Self is the agent the session drives ("maya/codex", AgentSpeaker).
	Self string
	// HasTools: the session has the bnbloc-room tools.
	HasTools bool
	// HasRoomFiles: bnbloc writes the room's records into the session's
	// workspace (.room/, the pull model).
	HasRoomFiles bool
	// TeamLead: the session wears a team expert (its default rule for
	// leading; the team's SOP may override it).
	TeamLead bool
}

// guidanceData is what the template reads: the facts, the limits and the
// scaffolding markers, each from its owner's constant.
type guidanceData struct {
	Guidance
	MaxFanOut int
	MaxDepth  int
	Now       string
	Records   string
	Fence     string
}

// SessionGuidance is the guidance a session is told once, when the bridge
// creates it: the agent profile's instructions on a core advertising
// agentProfile, else the head of the session's first prompt (the bridge's
// sessionGuidance decides which). Never part of a per-turn prompt.
func SessionGuidance(g Guidance) string {
	var b strings.Builder
	if err := guidanceTemplate.Execute(&b, guidanceData{
		Guidance: g, MaxFanOut: wire.MaxDispatchFanOut, MaxDepth: wire.MaxDispatchDepth,
		Now: strings.TrimSpace(nowMarker("<sender>")), Records: recordsMark, Fence: fenceMark,
	}); err != nil {
		panic("roomlog: session guidance template: " + err.Error()) // a static template bug
	}
	return strings.TrimSpace(b.String()) + "\n"
}

// ReadmeFile is .room/README.md: the session's guidance, kept in the
// workspace beside the records it describes.
func ReadmeFile(g Guidance) NamedFile {
	return NamedFile{Name: ".room/README.md", Content: "# How this room works (read before judging anyone's work)\n\n" + SessionGuidance(g)}
}

// AppSDKPath is where a pull workspace holds the interactive apps' SDK in
// demo mode (docs/prd/2026-10-13-interactive-apps-self-test.md): every
// call answered in the page's own memory, nothing sent. An agent's
// self-test of an app serves it as the page's SDK; the interactive-apps
// skill names it within the room records.
const AppSDKPath = core.RoomContextDir + "app-sdk/sdk.js"

// AppSDKUpdate is the demo-mode SDK js as a file when the workspace does
// not hold it yet (st.AppSDKSum, like ReadmeUpdate): written at a
// workspace's first pass and again only when the SDK changes.
func AppSDKUpdate(st CtxState, js []byte) (f NamedFile, sum string, changed bool) {
	h := sha256.Sum256(js)
	sum = hex.EncodeToString(h[:])
	return NamedFile{Name: AppSDKPath, Content: string(js)}, sum, sum != st.AppSDKSum
}

// ReadmeUpdate is .room/README.md when the session's workspace does not hold
// its current content yet: st.ReadmeSum is the sum of the README last written
// there (empty for a fresh session). sum is what to record in the state once
// the file is written. The README is static per session, so a session writes
// it at its first pass and again only when its wording changes, never every
// turn.
func ReadmeUpdate(st CtxState, g Guidance) (f NamedFile, sum string, changed bool) {
	f = ReadmeFile(g)
	h := sha256.Sum256([]byte(f.Content))
	sum = hex.EncodeToString(h[:])
	return f, sum, sum != st.ReadmeSum
}
