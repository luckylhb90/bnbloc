package contentpkg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Import turns an upload (a tar, tar.gz or zip archive, or one text file)
// of an existing format into a content package:
//
//   - a bnbloc package (component.json at the root) imports as is;
//   - otherwise the kind reads its foreign formats (KindSpec.fromImport):
//     a skill directory (SKILL.md at the root) or a directory of skills
//     (<name>/SKILL.md, or skills/<name>/SKILL.md); a Claude Code-style
//     plugin (.claude-plugin/plugin.json: its skills/<name>/ become the
//     skills, and for an expert its first agents/<name>.md becomes the
//     instructions); markdown or json files of prompts.
//
// kind is the target kind ("expert", "skill", "prompt"); name overrides the
// generated name; version is the version to write. A manifest is generated
// for every format but the first, declaring the scripts permission when a
// skill ships scripts.
func Import(upload []byte, kind, name, version string, opts Options) (*Package, []byte, error) {
	spec, ok := Lookup(kind)
	if !ok {
		return nil, nil, fmt.Errorf("unknown kind %q", kind)
	}
	files, err := readUpload(upload)
	if err != nil {
		return nil, nil, err
	}
	files = stripCommonRoot(files)
	if raw, ok := files[ManifestFile]; ok {
		return importPackage(raw, files, spec, name, version, opts)
	}
	if spec.fromImport == nil {
		return nil, nil, fmt.Errorf("import does not create %s packages", spec.Short)
	}
	m := Manifest{Kind: spec.Kind, Name: strings.TrimSpace(name), Version: version}
	out := map[string][]byte{}
	if err := spec.fromImport(files, &m, out); err != nil {
		return nil, nil, err
	}
	if m.Name == "" {
		return nil, nil, errors.New("the import has no name: pass one")
	}
	if r, ok := files["README.md"]; ok {
		out[ReadmeFile] = r
	}
	m.DisplayName = oneLine(m.DisplayName, maxDisplayName)
	m.Description = oneLine(m.Description, maxDescription)
	for p := range out {
		if isScript(p) {
			m.Requires.Permissions = []string{PermScripts}
			break
		}
	}
	return Pack(m, out, opts)
}

// pluginOf reads a Claude Code-style plugin's manifest, when the upload is
// one.
func pluginOf(files map[string][]byte) (pluginManifest, error) {
	var plugin pluginManifest
	if raw, ok := files[".claude-plugin/plugin.json"]; ok {
		if err := json.Unmarshal(raw, &plugin); err != nil {
			return plugin, fmt.Errorf(".claude-plugin/plugin.json: %w", err)
		}
	}
	return plugin, nil
}

// skillPackFromImport: the upload's skills become the pack's.
func skillPackFromImport(files map[string][]byte, m *Manifest, out map[string][]byte) error {
	plugin, err := pluginOf(files)
	if err != nil {
		return err
	}
	skills, err := findSkills(files)
	if err != nil {
		return err
	}
	if len(skills) == 0 {
		return errors.New("no skills found: a skill is a directory with a SKILL.md")
	}
	for _, s := range skills {
		m.Skills = append(m.Skills, s.slug)
		for rel, data := range s.files {
			out[s.slug+"/"+rel] = data
		}
	}
	m.Name = firstNonEmpty(m.Name, slugify(plugin.Name), skills[0].slug)
	m.Description = firstNonEmpty(plugin.Description, skills[0].description)
	if len(skills) > 1 && plugin.Description == "" {
		m.Description = fmt.Sprintf("%d skills: %s", len(skills), skillNames(skills))
	}
	return nil
}

// expertFromImport: instructions from a plugin agent or an instructions
// file, the upload's skills embedded.
func expertFromImport(files map[string][]byte, m *Manifest, out map[string][]byte) error {
	plugin, err := pluginOf(files)
	if err != nil {
		return err
	}
	skills, err := findSkills(files)
	if err != nil {
		return err
	}
	instructions, meta, err := findInstructions(files)
	if err != nil {
		return err
	}
	out[InstructionsFile] = []byte(strings.TrimSpace(instructions) + "\n")
	for _, s := range skills {
		for rel, data := range s.files {
			out[expertSkillsDir+"/"+s.slug+"/"+rel] = data
		}
	}
	m.Name = firstNonEmpty(m.Name, slugify(meta["name"]), slugify(plugin.Name))
	m.DisplayName = firstNonEmpty(meta["name"], plugin.Name)
	m.Description = firstNonEmpty(meta["description"], plugin.Description)
	return nil
}

// importPackage re-validates an uploaded bnbloc package, rewriting only its
// version (each save is a new version of the caller's component) and name
// when given.
func importPackage(raw []byte, files map[string][]byte, spec KindSpec, name, version string, opts Options) (*Package, []byte, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if m.Kind != spec.Kind {
		return nil, nil, fmt.Errorf("the package is a %s, not a %s", m.Kind, spec.Kind)
	}
	if name != "" {
		m.Name = name
	}
	m.Version = version
	delete(files, ManifestFile)
	return Pack(m, files, opts)
}

type pluginManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type foundSkill struct {
	slug        string
	description string
	files       map[string][]byte // relative to the skill dir
}

// findSkills locates skill directories: SKILL.md at the root (one skill),
// skills/<x>/SKILL.md (a plugin or a skills folder) or <x>/SKILL.md.
func findSkills(files map[string][]byte) ([]foundSkill, error) {
	dirs := map[string]bool{}
	for p := range files {
		if path.Base(p) != "SKILL.md" {
			continue
		}
		dir := path.Dir(p)
		switch {
		case dir == ".":
			dirs[""] = true
		case strings.Count(dir, "/") == 0, strings.HasPrefix(dir, "skills/") && strings.Count(dir, "/") == 1:
			dirs[dir] = true
		}
	}
	if dirs[""] && len(dirs) > 1 {
		return nil, errors.New("the archive mixes a root SKILL.md with skill directories")
	}
	keys := make([]string, 0, len(dirs))
	for d := range dirs {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	var out []foundSkill
	seen := map[string]bool{}
	for _, dir := range keys {
		prefix := ""
		if dir != "" {
			prefix = dir + "/"
		}
		fm, _ := ParseFrontmatter(string(files[prefix+"SKILL.md"]))
		slug := slugify(fm["name"])
		if slug == "" {
			slug = slugify(path.Base(dir))
		}
		if slug == "" {
			return nil, fmt.Errorf("%sSKILL.md has no name", prefix)
		}
		if seen[slug] {
			return nil, fmt.Errorf("two skills are named %q", slug)
		}
		seen[slug] = true
		s := foundSkill{slug: slug, description: strings.TrimSpace(fm["description"]), files: map[string][]byte{}}
		for p, data := range files {
			if dir != "" && !strings.HasPrefix(p, prefix) {
				continue
			}
			if dir == "" && (p == "README.md" || strings.HasPrefix(p, ".claude-plugin/")) {
				continue
			}
			s.files[strings.TrimPrefix(p, prefix)] = data
		}
		// The skill's own name must match its directory in the package.
		if fm["name"] != slug {
			body := string(s.files["SKILL.md"])
			_, rest := ParseFrontmatter(body)
			if strings.TrimSpace(s.description) == "" {
				return nil, fmt.Errorf("skill %s: SKILL.md frontmatter needs a description", slug)
			}
			s.files["SKILL.md"] = SkillMarkdown(slug, s.description, rest)
		}
		out = append(out, s)
	}
	return out, nil
}

// findInstructions picks an expert's instructions: the plugin's first
// agents/<name>.md (its frontmatter gives the name and description), else a
// root INSTRUCTIONS.md, AGENTS.md or CLAUDE.md.
func findInstructions(files map[string][]byte) (string, map[string]string, error) {
	var agents []string
	for p := range files {
		if strings.HasPrefix(p, "agents/") && strings.Count(p, "/") == 1 && strings.HasSuffix(p, ".md") {
			agents = append(agents, p)
		}
	}
	sort.Strings(agents)
	if len(agents) > 0 {
		fm, body := ParseFrontmatter(string(files[agents[0]]))
		if fm["name"] == "" {
			fm["name"] = strings.TrimSuffix(path.Base(agents[0]), ".md")
		}
		if strings.TrimSpace(body) != "" {
			return body, fm, nil
		}
	}
	for _, p := range []string{InstructionsFile, "AGENTS.md", "CLAUDE.md"} {
		if raw, ok := files[p]; ok && strings.TrimSpace(string(raw)) != "" {
			fm, body := ParseFrontmatter(string(raw))
			return body, fm, nil
		}
	}
	return "", nil, errors.New("an expert needs instructions: add INSTRUCTIONS.md (or AGENTS.md / CLAUDE.md), or import a plugin with an agent")
}

// readUpload reads an upload: an archive, or a single text file (a
// markdown or json file of prompts), named by its content.
func readUpload(raw []byte) (map[string][]byte, error) {
	if tarbundle.IsArchive(raw) || !utf8.Valid(raw) {
		// Every content kind's archive is read within the widest content
		// profile; the kind's own caps apply to the package built from it.
		return tarbundle.ReadArchive(raw, tarbundle.ContentLimits)
	}
	if len(raw) > promptPackMaxSize {
		return nil, fmt.Errorf("the file exceeds %d bytes", promptPackMaxSize)
	}
	t := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\ufeff")))
	if len(t) == 0 {
		return nil, errors.New("the file is empty")
	}
	if t[0] == '{' || t[0] == '[' {
		return map[string][]byte{"prompts.json": raw}, nil
	}
	return map[string][]byte{"prompts.md": raw}, nil
}

// stripCommonRoot removes one directory every file shares ("my-skill/…"),
// unless that directory is itself meaningful (a single skill folder keeps
// its SKILL.md at the new root either way).
func stripCommonRoot(files map[string][]byte) map[string][]byte {
	root := ""
	for p := range files {
		first, _, ok := strings.Cut(p, "/")
		if !ok {
			return files
		}
		if root == "" {
			root = first
		} else if first != root {
			return files
		}
	}
	if root == "" || root == "skills" || root == "agents" || root == ".claude-plugin" {
		return files
	}
	out := make(map[string][]byte, len(files))
	for p, data := range files {
		out[strings.TrimPrefix(p, root+"/")] = data
	}
	return out
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// slugify maps a free-form name to a component name.
func slugify(s string) string {
	out := strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

func skillNames(skills []foundSkill) string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.slug)
	}
	return strings.Join(names, ", ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// oneLine collapses whitespace and bounds the length (generated display
// fields come from free-form frontmatter).
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}
