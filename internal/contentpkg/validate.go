package contentpkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Package is a validated content package.
type Package struct {
	Manifest Manifest
	// SHA256 is the bundle archive's digest (the content address).
	SHA256 string
	Bundle *tarbundle.Bundle
	// Instructions is an expert's INSTRUCTIONS.md.
	Instructions string
	// Skills are an expert's embedded skills or a skill pack's skills.
	Skills []Skill
	// Scripts reports files that execute (a scripts/ dir or a script
	// extension); such a package must declare the scripts permission.
	Scripts bool
}

// Skill is one skill of a package: a directory holding SKILL.md.
type Skill struct {
	Name        string
	Description string
	// Dir is the skill's directory in the bundle ("<slug>" in a skill
	// pack, "skills/<slug>" in an expert), Files its files relative to it.
	Dir   string
	Files []string
}

// Options are the use-site inputs of validation.
type Options struct {
	// MCPTool looks up the MCP tool component a requires.mcp entry names,
	// as the saving member may use it. nil checks references by syntax
	// only: stored content is read and materialized without the platform's
	// catalog, so a component uninstalled later never makes it invalid (use
	// time reports the tool missing instead).
	MCPTool func(name string) (MCPToolInfo, bool)
}

// MCPToolInfo describes the MCP tool component a reference names.
type MCPToolInfo struct {
	// Transport is "stdio" or "http".
	Transport string
	// Host is an http tool's URL host, as mcp.http:<host> names it.
	Host string
	// Env are the variable names the component declares.
	Env []string
}

// Validate parses a bundle (verifying wantSHA256 when non-empty) and checks
// it against the shared envelope rules and its kind's rules.
func Validate(raw []byte, wantSHA256 string, opts Options) (*Package, error) {
	// Parse with the widest content profile first: the manifest names the
	// kind, whose own (equal or narrower) caps are checked below.
	b, err := tarbundle.Parse(raw, wantSHA256, BundleLimits())
	if err != nil {
		return nil, err
	}
	return ValidateBundle(b, opts)
}

// ValidateBundle checks an already parsed bundle.
func ValidateBundle(b *tarbundle.Bundle, opts Options) (*Package, error) {
	rawManifest, ok := b.File(ManifestFile)
	if !ok {
		return nil, fmt.Errorf("package has no %s", ManifestFile)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(rawManifest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	spec, ok := kinds[m.Kind]
	if !ok {
		return nil, fmt.Errorf("%s: unknown kind %q", ManifestFile, m.Kind)
	}
	if len(b.Paths()) > spec.Limits.MaxFiles || b.Size() > spec.Limits.MaxBytes {
		return nil, fmt.Errorf("package exceeds the %s limits (%d files, %d bytes)", spec.Short, spec.Limits.MaxFiles, spec.Limits.MaxBytes)
	}
	if err := validateEnvelope(m, opts); err != nil {
		return nil, err
	}
	p := &Package{Manifest: m, SHA256: b.SHA256, Bundle: b}
	for _, f := range b.Paths() {
		if !spec.Site && isScript(f) {
			p.Scripts = true
			break
		}
	}
	if p.Scripts && !m.declares(PermScripts) {
		return nil, fmt.Errorf("the package ships scripts but does not declare the %q permission", PermScripts)
	}
	if err := spec.validate(p); err != nil {
		return nil, err
	}
	return p, nil
}

// ScriptFiles lists the bundle's files that execute (a scripts/ or bin/
// directory, or a script extension), sorted: what an approver reviews.
func (p *Package) ScriptFiles() []string {
	out := []string{}
	if !p.Scripts {
		return out
	}
	for _, f := range p.Bundle.Paths() {
		if isScript(f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func (m Manifest) declares(perm string) bool {
	for _, p := range m.Requires.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// Permissions returns the declared permissions (sorted).
func (m Manifest) Permissions() []string {
	out := append([]string(nil), m.Requires.Permissions...)
	sort.Strings(out)
	return out
}

// NeedsSandbox reports whether content that is not the member's own forces
// a sandboxed drive: it declares scripts or a stdio MCP server.
func (m Manifest) NeedsSandbox() bool {
	return m.declares(PermScripts) || m.declares(PermMCPStdio)
}

func validateEnvelope(m Manifest, opts Options) error {
	if !ValidName(m.Name) {
		return fmt.Errorf("name %q: use lower-case letters, digits and hyphens (at most %d)", m.Name, maxName)
	}
	if !semver.Valid(m.Version) {
		return fmt.Errorf("version %q is not a semantic version (major.minor.patch)", m.Version)
	}
	if err := checkText("displayName", m.DisplayName, maxDisplayName, true); err != nil {
		return err
	}
	if err := checkText("description", m.Description, maxDescription, false); err != nil {
		return err
	}
	if m.Category != "" && (!ValidName(m.Category) || len(m.Category) > maxCategory) {
		return fmt.Errorf("category %q: use lower-case letters, digits and hyphens", m.Category)
	}
	if len(m.Tags) > maxTags {
		return fmt.Errorf("at most %d tags", maxTags)
	}
	for _, t := range m.Tags {
		if len(t) > maxTag || !tagRE.MatchString(t) {
			return fmt.Errorf("tag %q: use lower-case letters, digits, spaces, '_' and '-'", t)
		}
	}
	if err := checkText("icon", m.Icon, maxIcon, true); err != nil {
		return err
	}
	if m.Team != nil {
		if m.Kind != KindExpert {
			return fmt.Errorf("a team belongs to an expert")
		}
		if err := validateTeam(*m.Team); err != nil {
			return err
		}
	}
	r := m.Requires
	if len(r.Skills)+len(r.Prompts)+len(r.MCP)+len(r.Permissions) > maxRequires {
		return fmt.Errorf("at most %d requirements", maxRequires)
	}
	for _, s := range r.Skills {
		if err := checkReference("requires.skills", s); err != nil {
			return err
		}
	}
	for _, s := range r.Prompts {
		if err := checkReference("requires.prompts", s); err != nil {
			return err
		}
	}
	mcpSeen := map[string]bool{}
	for _, t := range r.MCP {
		if err := pkgmeta.ValidMCPToolName(t.Name); err != nil {
			return fmt.Errorf("requires.mcp: %w", err)
		}
		if mcpSeen[t.Name] {
			return fmt.Errorf("requires.mcp: %q is named twice", t.Name)
		}
		mcpSeen[t.Name] = true
		for _, e := range t.Env {
			if !pkgmeta.ValidEnvName(e) {
				return fmt.Errorf("requires.mcp %s: invalid env name %q", t.Name, e)
			}
		}
		if err := checkMCPReference(m, t, opts); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, p := range r.Permissions {
		if seen[p] {
			return fmt.Errorf("permission %q declared twice", p)
		}
		seen[p] = true
		switch {
		case p == PermScripts:
		case p == PermMCPStdio:
			if len(r.MCP) == 0 {
				return fmt.Errorf("permission %q needs requires.mcp naming the MCP tool", p)
			}
		case strings.HasPrefix(p, PermMCPHTTPPrefix):
			host := strings.TrimPrefix(p, PermMCPHTTPPrefix)
			if !hostRE.MatchString(host) || len(host) > 253 {
				return fmt.Errorf("permission %q: invalid host", p)
			}
			if len(r.MCP) == 0 {
				return fmt.Errorf("permission %q needs requires.mcp naming the MCP tool", p)
			}
		default:
			return fmt.Errorf("unknown permission %q (want %s, %s or %s<host>)", p, PermScripts, PermMCPStdio, PermMCPHTTPPrefix)
		}
	}
	return nil
}

// checkMCPReference holds a requires.mcp entry to the MCP tool component
// it names, when the caller can look components up (a save): MCP servers
// come only from those platform-signed components, so a reference must name
// one, only its declared variables, and the package must declare the
// permission the tool's transport needs (mcp.stdio, or mcp.http:<host>) so
// its card shows what runs.
func checkMCPReference(m Manifest, t MCPRequirement, opts Options) error {
	if opts.MCPTool == nil {
		return nil
	}
	info, ok := opts.MCPTool(t.Name)
	if !ok {
		return fmt.Errorf("requires.mcp: %q is not an MCP tool component available to you (MCP tools are published by the platform)", t.Name)
	}
	declared := map[string]bool{}
	for _, e := range info.Env {
		declared[e] = true
	}
	for _, e := range t.Env {
		if !declared[e] {
			return fmt.Errorf("requires.mcp %s: %q is not one of its variables (%s)", t.Name, e, strings.Join(info.Env, ", "))
		}
	}
	switch info.Transport {
	case "stdio":
		if !m.declares(PermMCPStdio) {
			return fmt.Errorf("requires.mcp %s: a stdio MCP tool needs the %q permission", t.Name, PermMCPStdio)
		}
	case "http":
		if want := PermMCPHTTPPrefix + info.Host; !m.declares(want) {
			return fmt.Errorf("requires.mcp %s: an http MCP tool needs the %q permission", t.Name, want)
		}
	}
	return nil
}

func checkReference(field, s string) error {
	ref, err := ParseReference(s)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if ref.Range != "" && !semver.ValidRange(ref.Range) {
		return fmt.Errorf("%s: invalid version range in %q", field, s)
	}
	return nil
}

func checkText(field, s string, max int, singleLine bool) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s is not UTF-8 text", field)
	}
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s is longer than %d characters", field, max)
	}
	if singleLine && strings.ContainsAny(s, "\r\n") {
		return fmt.Errorf("%s must be one line", field)
	}
	return nil
}

// scriptExts are file extensions that execute.
var scriptExts = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".py": true, ".js": true, ".mjs": true,
	".cjs": true, ".ts": true, ".rb": true, ".pl": true, ".php": true, ".ps1": true, ".bat": true,
	".cmd": true, ".exe": true, ".bin": true, ".jar": true, ".lua": true, ".go": true,
}

// isScript reports whether a bundle file is executable content: it lies in
// a scripts/ directory or has a script extension.
func isScript(p string) bool {
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if seg == "scripts" || seg == "bin" {
			return true
		}
	}
	return scriptExts[strings.ToLower(path.Ext(p))]
}

// readText returns a bundle file as UTF-8 text.
func readText(b *tarbundle.Bundle, p string, max int) (string, error) {
	raw, ok := b.File(p)
	if !ok {
		return "", fmt.Errorf("package has no %s", p)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("%s is not UTF-8 text", p)
	}
	if len(raw) > max {
		return "", fmt.Errorf("%s is larger than %d bytes", p, max)
	}
	return string(raw), nil
}

// skillAt validates one skill directory dir (holding SKILL.md) and returns
// it with its files. The SKILL.md frontmatter must name the skill slug and
// describe it (the agent loads the skill from that description).
func skillAt(b *tarbundle.Bundle, dir, slug string) (Skill, error) {
	if !ValidName(slug) {
		return Skill{}, fmt.Errorf("skill %q: use lower-case letters, digits and hyphens", slug)
	}
	body, err := readText(b, dir+"/SKILL.md", maxInstructions)
	if err != nil {
		return Skill{}, fmt.Errorf("skill %s: %w", slug, err)
	}
	fm, _ := ParseFrontmatter(body)
	if fm["name"] != slug {
		return Skill{}, fmt.Errorf("skill %s: SKILL.md frontmatter name must be %q", slug, slug)
	}
	desc := strings.TrimSpace(fm["description"])
	if desc == "" {
		return Skill{}, fmt.Errorf("skill %s: SKILL.md frontmatter needs a description", slug)
	}
	if utf8.RuneCountInString(desc) > maxSkillDesc {
		return Skill{}, fmt.Errorf("skill %s: description is longer than %d characters", slug, maxSkillDesc)
	}
	s := Skill{Name: slug, Description: desc, Dir: dir}
	prefix := dir + "/"
	for _, f := range b.Paths() {
		if strings.HasPrefix(f, prefix) {
			s.Files = append(s.Files, strings.TrimPrefix(f, prefix))
		}
	}
	return s, nil
}

// ProfileSkills returns the package's skills as agent profile skills (their
// files read from the bundle).
func (p *Package) ProfileSkills() []core.ProfileSkill {
	out := make([]core.ProfileSkill, 0, len(p.Skills))
	for _, s := range p.Skills {
		ps := core.ProfileSkill{Name: s.Name}
		for _, f := range s.Files {
			data, _ := p.Bundle.File(s.Dir + "/" + f)
			ps.Files = append(ps.Files, core.ProfileFile{Path: f, Content: data})
		}
		out = append(out, ps)
	}
	return out
}
