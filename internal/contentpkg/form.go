package contentpkg

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Form is a content component saved from the Library form (or by a
// builder): the manifest fields plus the kind's text, laid out into files by
// the kind. Version is assigned by the caller (the store's next version).
type Form struct {
	Kind         string        `json:"kind"`
	Name         string        `json:"name"`
	DisplayName  string        `json:"displayName,omitempty"`
	Description  string        `json:"description,omitempty"`
	Category     string        `json:"category,omitempty"`
	Tags         []string      `json:"tags,omitempty"`
	Icon         string        `json:"icon,omitempty"`
	Requires     Requires      `json:"requires,omitempty"`
	Instructions string        `json:"instructions,omitempty"`
	QuickPrompts []QuickPrompt `json:"quickPrompts,omitempty"`
	Welcome      string        `json:"welcome,omitempty"`
	Skills       []FormSkill   `json:"skills,omitempty"`
	Prompts      []Prompt      `json:"prompts,omitempty"`

	// Team makes an expert a team (its roster).
	Team *TeamSpec `json:"team,omitempty"`

	// An assist's task (Instructions is its system prompt).
	Output  string             `json:"output,omitempty"`
	Fields  []wire.AssistField `json:"fields,omitempty"`
	Context []string           `json:"context,omitempty"`
	Gate    *wire.AssistGate   `json:"gate,omitempty"`

	// A room package's setup (room.go); its welcome and quick prompts are
	// the fields above, its room expert and packs Requires.
	Channels  []string                   `json:"channels,omitempty"`
	Schedules []wire.RoomPackageSchedule `json:"schedules,omitempty"`
	Apps      []string                   `json:"apps,omitempty"`
	Sources   []wire.RoomSourceAsk       `json:"sources,omitempty"`
}

// FormSkill is one skill written in the form: its SKILL.md is generated
// from the name, description and body.
type FormSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

// Build lays a form out into a bundle at version and validates it. base,
// when set, is the version the form edits: its README and the extra files
// of the skills the form keeps (scripts, references) carry over, since the
// form only edits text.
func Build(f Form, version string, opts Options, base *Package) (*Package, []byte, error) {
	spec, ok := Lookup(f.Kind)
	if !ok {
		return nil, nil, fmt.Errorf("unknown kind %q", f.Kind)
	}
	if base != nil && base.Manifest.Kind != spec.Kind {
		return nil, nil, fmt.Errorf("a %s cannot become a %s", base.Manifest.Kind, spec.Kind)
	}
	if spec.Kind != KindAssist && (f.Output != "" || len(f.Fields) > 0 || len(f.Context) > 0 || f.Gate != nil) {
		return nil, nil, errAssistFields
	}
	if spec.Kind != KindRoom && len(f.Channels)+len(f.Schedules)+len(f.Apps)+len(f.Sources) > 0 {
		return nil, nil, errRoomFields
	}
	m := Manifest{
		Kind: spec.Kind, Name: strings.TrimSpace(f.Name), Version: version,
		DisplayName: strings.TrimSpace(f.DisplayName), Description: strings.TrimSpace(f.Description),
		Category: strings.TrimSpace(f.Category), Tags: f.Tags, Icon: strings.TrimSpace(f.Icon), Requires: f.Requires,
	}
	files := map[string][]byte{}
	if base != nil {
		keep := map[string]bool{}
		for _, sk := range f.Skills {
			keep[sk.Name] = true
		}
		for _, sk := range base.Skills {
			if !keep[sk.Name] {
				continue
			}
			for _, rel := range sk.Files {
				if rel != "SKILL.md" {
					data, _ := base.Bundle.File(sk.Dir + "/" + rel)
					files[skillDir(spec.Kind, sk.Name)+"/"+rel] = data
				}
			}
		}
		if data, ok := base.Bundle.File(ReadmeFile); ok {
			files[ReadmeFile] = data
		}
	}
	if err := spec.fromForm(f, &m, files); err != nil {
		return nil, nil, err
	}
	return Pack(m, files, opts)
}

// skillDir is where a kind keeps a skill's directory.
func skillDir(kind, slug string) string {
	if kind == KindExpert {
		return expertSkillsDir + "/" + slug
	}
	return slug
}

// Pack writes m as component.json beside files, archives them and
// validates the result.
func Pack(m Manifest, files map[string][]byte, opts Options) (*Package, []byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	all := make(map[string][]byte, len(files)+1)
	for k, v := range files {
		all[k] = v
	}
	all[ManifestFile] = append(raw, '\n')
	limits := BundleLimits()
	if spec, ok := kinds[m.Kind]; ok {
		limits = spec.Limits
	}
	bundle, err := tarbundle.PackFiles(all, limits)
	if err != nil {
		return nil, nil, err
	}
	pkg, err := Validate(bundle, "", opts)
	if err != nil {
		return nil, nil, err
	}
	return pkg, bundle, nil
}

// SkillMarkdown renders a SKILL.md from its frontmatter fields and body.
func SkillMarkdown(name, description, body string) []byte {
	desc := strings.Join(strings.Fields(description), " ")
	return []byte("---\nname: " + name + "\ndescription: " + yamlScalar(desc) + "\n---\n\n" + strings.TrimLeft(body, "\n"))
}

// yamlScalar quotes a value when plain YAML would misread it.
func yamlScalar(s string) string {
	if s == "" || strings.ContainsAny(s, ":#'\"{}[]&*!|>%@`") || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "?") {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
	}
	return s
}

func expertFromForm(f Form, m *Manifest, files map[string][]byte) error {
	if len(f.Prompts) > 0 {
		return fmt.Errorf("prompts belong to prompt packs")
	}
	m.QuickPrompts = f.QuickPrompts
	m.Welcome = strings.TrimSpace(f.Welcome)
	m.Team = f.Team
	files[InstructionsFile] = []byte(strings.TrimSpace(f.Instructions) + "\n")
	for _, s := range f.Skills {
		files[skillDir(KindExpert, s.Name)+"/SKILL.md"] = SkillMarkdown(s.Name, s.Description, s.Body)
	}
	return nil
}

func skillPackFromForm(f Form, m *Manifest, files map[string][]byte) error {
	if strings.TrimSpace(f.Instructions) != "" || len(f.QuickPrompts) > 0 || f.Welcome != "" || f.Team != nil {
		return fmt.Errorf("instructions, quick prompts, a welcome and a team belong to experts")
	}
	if len(f.Prompts) > 0 {
		return fmt.Errorf("prompts belong to prompt packs")
	}
	for _, s := range f.Skills {
		m.Skills = append(m.Skills, s.Name)
		files[skillDir(KindSkill, s.Name)+"/SKILL.md"] = SkillMarkdown(s.Name, s.Description, s.Body)
	}
	return nil
}
