package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// Content in effect for a turn (docs/prd/2026-10-12-experts-and-teams.md):
// the expert the chat picked, the skill packs its owner enabled for the
// room and for all their agents, and the packs the expert requires. The
// gateway resolves them for the job (GET /v1/content-components/effective,
// authorized by this bridge holding the job's lease); the bridge downloads
// each bundle under the job's grant, verifies it against its sha256 with
// the shared validator, and turns it into the session's agent profile.
//
// A core advertising agentProfile receives the profile on session create
// and maps it natively. Any other core gets the fallback: the expert's
// instructions and a summary of the skills prefixed to the session's first
// prompt, the skill files written under .agents/skills/ when the core
// accepts workspace files. The profile's content checksum is part of the
// session binding: a turn whose content differs starts a new session.

// fallbackSkillsDir is where the bridge writes skill files for a core
// without agentProfile (one of core.AgentConfigPaths).
const fallbackSkillsDir = ".agents/skills"

// Fallback prompt bounds for skills inlined into the prefix (a core that
// cannot receive files): per skill body and in total.
const (
	maxInlineSkillBody = 8 << 10
	maxInlineSkills    = 32 << 10
)

// jobContent is what one job's turn wears.
type jobContent struct {
	expert *wire.ContentRef
	// roomExpert is the room's expert, worn under the chat's own.
	roomExpert *wire.ContentRef
	// team is the roster when the expert is a team (team.go).
	team  *roomtools.Team
	packs []wire.ContentRef
	// missing are requirements no usable package satisfies.
	missing      []string
	instructions string
	skills       []corepkg.ProfileSkill
	summaries    []skillSummary
	// sum is the content checksum of the profile (instructions + skill
	// files); "" for no content.
	sum string
	// foreign names content that is not the agent owner's own and declares
	// scripts or stdio MCP, or requires a stdio MCP tool: it forces a
	// sandboxed drive.
	foreign []string
	// mcp are the MCP servers of the tools the content requires
	// (mcp_tools.go).
	mcp jobMCP
}

type skillSummary struct {
	name        string
	description string
	body        string
}

// empty reports no content in effect.
func (c *jobContent) empty() bool {
	return c == nil || (strings.TrimSpace(c.instructions) == "" && len(c.skills) == 0)
}

// profile is the agent profile of the content and its MCP tools (nil when
// there is neither).
func (c *jobContent) profile() *corepkg.AgentProfile {
	if c.empty() && (c == nil || len(c.mcp.servers) == 0) {
		return nil
	}
	return &corepkg.AgentProfile{
		Instructions: c.instructions, Skills: append([]corepkg.ProfileSkill(nil), c.skills...),
		MCPServers: append([]corepkg.MCPServer(nil), c.mcp.servers...),
	}
}

// contentSum is the session binding checksum ("" for none): the profile's
// content checksum, plus the MCP tool set's non-secret identity (a tool
// added, upgraded or newly configured starts a new session; values never
// enter it).
func (c *jobContent) contentSum() string {
	if c == nil {
		return ""
	}
	return c.mcp.bindingSum(c.sum)
}

// expertLabel names the expert for notices and the prompt prefix.
func expertLabel(r *wire.ContentRef) string {
	if r == nil {
		return ""
	}
	name := r.DisplayName
	if name == "" {
		name = r.Name
	}
	return name + " (v" + r.Version + ")"
}

// promptPrefix is the fallback for a core without agentProfile: the
// expert's instructions and the skills, pointing at their files when they
// were written into the workspace (skillsWritten), else inlined.
func (c *jobContent) promptPrefix(skillsWritten bool) string {
	if c.empty() {
		return ""
	}
	var b strings.Builder
	if strings.TrimSpace(c.instructions) != "" {
		if c.expert != nil {
			b.WriteString("[Expert: " + expertLabel(c.expert) + ". Follow these instructions for this conversation.]\n\n")
		}
		b.WriteString(strings.TrimSpace(c.instructions))
		b.WriteString("\n\n")
	}
	if len(c.summaries) > 0 {
		if skillsWritten {
			b.WriteString("[Skills: load a skill when its description matches the task; its files are in " + fallbackSkillsDir + "/<name>/, start with SKILL.md.]\n")
			for _, s := range c.summaries {
				b.WriteString("- " + s.name + ": " + s.description + "\n")
			}
		} else {
			b.WriteString("[Skills: use a skill when its description matches the task.]\n")
			budget := maxInlineSkills
			for _, s := range c.summaries {
				b.WriteString("\n## Skill " + s.name + "\n" + s.description + "\n")
				body := strings.TrimSpace(s.body)
				if len(body) > maxInlineSkillBody {
					body = body[:maxInlineSkillBody] + "\n…"
				}
				if len(body) > budget {
					continue
				}
				budget -= len(body)
				if body != "" {
					b.WriteString(body + "\n")
				}
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}

// contentBundles caches verified content bundles by sha256 (shared by
// every job: versions are immutable).
var contentBundles = struct {
	once  sync.Once
	cache *tarbundle.Cache
}{}

func contentBundleCache() *tarbundle.Cache {
	contentBundles.once.Do(func() { contentBundles.cache = tarbundle.NewCache(64 << 20) })
	return contentBundles.cache
}

// resolveJobContent asks the gateway what the job's turn wears and loads
// every package. A job naming an expert that resolves to none is an error
// the caller reports (the requester picked it; running without it would
// silently answer as someone else).
func (b *Bridge) resolveJobContent(ctx context.Context, job bridgeJob, owner string) (*jobContent, error) {
	q := url.Values{"job": {job.JobID}, "room": {job.RoomID}}
	raw, err := b.gatewayGET(ctx, "/v1/content-components/effective?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("resolve experts and skills: %w", err)
	}
	var eff wire.ContentEffective
	if err := json.Unmarshal(raw, &eff); err != nil {
		return nil, fmt.Errorf("resolve experts and skills: %w", err)
	}
	if job.ExpertID != "" && eff.Expert == nil {
		return nil, &contentUnavailableError{expert: firstNonEmpty(job.ExpertName, job.ExpertID)}
	}
	c := &jobContent{expert: eff.Expert, roomExpert: eff.RoomExpert, packs: eff.SkillPacks, missing: eff.Missing}
	refs := eff.SkillPacks
	if eff.Expert != nil {
		refs = append([]wire.ContentRef{*eff.Expert}, refs...)
	}
	if eff.RoomExpert != nil {
		refs = append([]wire.ContentRef{*eff.RoomExpert}, refs...)
	}
	roomInstructions := ""
	seen := map[string]bool{}
	for _, ref := range refs {
		pkg, err := b.contentPackage(ctx, job, ref)
		if err != nil {
			return nil, fmt.Errorf("load %s %s: %w", ref.Kind, ref.Name, err)
		}
		if ref.NeedsSandbox && !sameUserID(ref.Owner, owner) {
			c.foreign = append(c.foreign, firstNonEmpty(ref.DisplayName, ref.Name))
		}
		switch ref.Source {
		case wire.ContentSourceExpert:
			c.instructions = pkg.Instructions
		case wire.ContentSourceRoomExpert:
			roomInstructions = pkg.Instructions
		}
		profileSkills := pkg.ProfileSkills()
		for i, s := range pkg.Skills {
			if seen[s.Name] {
				slog.Warn("bnbloc bridge skill name collision; keeping the first",
					"event", "bnbloc.bridge.skill_collision", "skill", s.Name, "component", ref.Name)
				continue
			}
			seen[s.Name] = true
			c.skills = append(c.skills, profileSkills[i])
			body := ""
			if raw, ok := pkg.Bundle.File(s.Dir + "/SKILL.md"); ok {
				_, body = contentpkg.ParseFrontmatter(string(raw))
			}
			c.summaries = append(c.summaries, skillSummary{name: s.Name, description: s.Description, body: body})
		}
	}
	// The room's expert comes first: the chat's own expert is worn on top.
	if r := strings.TrimSpace(roomInstructions); r != "" {
		if strings.TrimSpace(c.instructions) != "" {
			c.instructions = r + "\n\n" + c.instructions
		} else {
			c.instructions = r
		}
	}
	if eff.Team != nil {
		if err := b.teamContent(ctx, job, c, eff.Team); err != nil {
			return nil, err
		}
	}
	if !c.empty() {
		c.sum = c.profile().ContentSum()
	}
	mcp, unmet, stdioForeign := b.resolveMCP(eff.MCP, refs, owner, sameUserID(job.RequesterUserID, owner))
	c.mcp = mcp
	c.missing = append(c.missing, unmet...)
	for _, name := range stdioForeign {
		if !slices.Contains(c.foreign, name) {
			c.foreign = append(c.foreign, name)
		}
	}
	if len(c.missing) > 0 {
		slog.Info("bnbloc bridge content requirements unmet",
			"event", "bnbloc.bridge.content_missing", "job", job.JobID, "missing", c.missing)
	}
	return c, nil
}

// contentPackage downloads one bundle under the job's grant and validates
// it against its pinned sha256 (cached by digest).
func (b *Bridge) contentPackage(ctx context.Context, job bridgeJob, ref wire.ContentRef) (*contentpkg.Package, error) {
	bundle, err := contentBundleCache().GetOrLoad(ref.SHA256, func() (*tarbundle.Bundle, error) {
		q := url.Values{"job": {job.JobID}, "room": {job.RoomID}}
		raw, err := b.gatewayGET(ctx, "/v1/artifacts/"+ref.BundleKey+"?"+q.Encode())
		if err != nil {
			return nil, err
		}
		return tarbundle.Parse(raw, ref.SHA256, tarbundle.ContentLimits)
	})
	if err != nil {
		return nil, err
	}
	return contentpkg.ValidateBundle(bundle, contentpkg.Options{})
}

// contentUnavailableError: the job's expert no longer resolves.
type contentUnavailableError struct{ expert string }

func (e *contentUnavailableError) Error() string {
	return "the expert " + e.expert + " is no longer available"
}

// writeFallbackSkills writes the skill files under .agents/skills/ for a
// core without agentProfile that accepts workspace files; it reports
// whether every file was written.
func (b *Bridge) writeFallbackSkills(ctx context.Context, coreName, workspaceID string, c *jobContent) bool {
	if c.empty() || len(c.skills) == 0 {
		return false
	}
	entry, ok := b.coreEntry(coreName)
	if !ok || !entry.endpoint.OK() {
		return false
	}
	w, ok := corepkg.As[corepkg.WorkspaceFileBytesWriter](ctx, entry.provider, entry.endpoint, corepkg.CapabilityWriteFile)
	if !ok {
		return false
	}
	for _, s := range c.skills {
		for _, f := range s.Files {
			rel := fallbackSkillsDir + "/" + s.Name + "/" + f.Path
			if err := w.WriteWorkspaceFileBytes(ctx, entry.endpoint, workspaceID, rel, f.Content); err != nil {
				slog.Warn("bnbloc bridge skill file write failed; inlining skills",
					"event", "bnbloc.bridge.skill_write_failed", "core", coreName, "path", rel, "error", err)
				return false
			}
		}
	}
	return true
}

// resetAgentConfig removes the bnbloc-owned agent-config paths from a
// sandbox workdir before a fresh session is created in it: the workdir is
// reused across sessions of a (room, core, provider), so a profile's files
// are re-materialized from scratch whenever the content changes.
func resetAgentConfig(workdir string) {
	if strings.TrimSpace(workdir) == "" {
		return
	}
	for _, p := range corepkg.AgentConfigPaths {
		target := filepath.Join(workdir, filepath.FromSlash(strings.TrimSuffix(p, "/")))
		if err := os.RemoveAll(target); err != nil {
			slog.Warn("bnbloc bridge agent config reset failed",
				"event", "bnbloc.bridge.agent_config_reset_failed", "path", target, "error", err)
		}
	}
}

// agentConfigDenyWrite lists a workdir's agent-config paths for the host
// confinement's DenyWrite: the agent must not edit the role it wears.
func agentConfigDenyWrite(workdir string) []string {
	out := make([]string, 0, len(corepkg.AgentConfigPaths))
	for _, p := range corepkg.AgentConfigPaths {
		out = append(out, filepath.Join(workdir, filepath.FromSlash(strings.TrimSuffix(p, "/"))))
	}
	sort.Strings(out)
	return out
}

// bnblocOwnedPath reports a workspace path bnbloc writes for the agent (the
// .room/ context and the agent-config paths): never a deliverable, never
// site content.
func bnblocOwnedPath(rel string) bool {
	return strings.HasPrefix(rel, corepkg.RoomContextDir) || corepkg.IsAgentConfigPath(rel)
}

// jobContentOrRefuse resolves the job's content. A job that names an expert
// it cannot wear is refused with a room notice (nothing runs). Any other
// resolution failure drives the turn without content (logged): room and
// member skill packs are best-effort, never a reason to drop a turn.
func (b *Bridge) jobContentOrRefuse(ctx context.Context, job bridgeJob, owner, coreName string) (*jobContent, bool) {
	content, err := b.resolveJobContent(ctx, job, owner)
	if err == nil {
		if b.refuseTeamWithoutTools(ctx, job, owner, coreName, content) {
			return nil, false
		}
		return content, true
	}
	if job.ExpertID == "" {
		slog.Warn("bnbloc bridge content resolution failed; driving without skill packs",
			"event", "bnbloc.bridge.content_unresolved", "job", job.JobID, "error", err)
		return &jobContent{}, true
	}
	text := "This message asked for an expert that could not be loaded: " + err.Error() + ". Nothing ran."
	var unavailable *contentUnavailableError
	if errors.As(err, &unavailable) {
		text = "The expert " + unavailable.expert + " is no longer available to " + job.RequesterUserID + ". Nothing ran."
	}
	b.postNotice(ctx, job.ref(), job.RoomID, job.ChannelID, "content-unavailable", map[string]any{
		"text":        text,
		"ownerUserId": owner,
		"provider":    job.Provider,
		"failReason":  "content_unavailable",
	})
	b.reportJobStatus(ctx, job.JobID, store.AgentJobFailed, store.FailureRunnerFailed, map[string]any{"core": coreName, "provider": job.Provider, "error": text})
	return nil, false
}
