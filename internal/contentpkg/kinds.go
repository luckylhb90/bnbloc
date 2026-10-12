package contentpkg

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Expert files.
const (
	// InstructionsFile is an expert's always-on part: persona and method.
	InstructionsFile = "INSTRUCTIONS.md"
	// expertSkillsDir holds an expert's embedded skills
	// (skills/<slug>/SKILL.md).
	expertSkillsDir = "skills"
	// ReadmeFile is an optional human-facing description, any kind.
	ReadmeFile = "README.md"
)

func init() {
	register(KindSpec{Kind: KindExpert, Short: "expert", Limits: tarbundle.ContentLimits,
		validate: validateExpert, fromForm: expertFromForm, fromImport: expertFromImport})
	register(KindSpec{Kind: KindSkill, Short: "skill", Limits: tarbundle.ContentLimits, MemberUse: true,
		validate: validateSkillPack, fromForm: skillPackFromForm, fromImport: skillPackFromImport})
}

// validateExpert: INSTRUCTIONS.md (non-empty), optional embedded skills under
// skills/<slug>/, quick prompts and welcome; nothing else at the top level
// but component.json and README.md.
func validateExpert(p *Package) error {
	m := p.Manifest
	if len(m.Skills) > 0 {
		return fmt.Errorf("an expert lists no skills field; embed skills under %s/<name>/ or require skill packs", expertSkillsDir)
	}
	if len(m.Prompts) > 0 {
		return fmt.Errorf("an expert lists no prompts field; require prompt packs (requires.prompts) and point quick prompts at them")
	}
	text, err := readText(p.Bundle, InstructionsFile, maxInstructions)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s is empty", InstructionsFile)
	}
	p.Instructions = text
	if len(m.QuickPrompts) > maxQuickPrompts {
		return fmt.Errorf("at most %d quick prompts", maxQuickPrompts)
	}
	for i, q := range m.QuickPrompts {
		if err := checkQuickPrompt(m, i, q); err != nil {
			return err
		}
	}
	if err := checkText("welcome", m.Welcome, maxWelcome, false); err != nil {
		return err
	}
	slugs := map[string]bool{}
	for _, f := range p.Bundle.Paths() {
		switch {
		case f == ManifestFile, f == InstructionsFile, f == ReadmeFile:
		case strings.HasPrefix(f, expertSkillsDir+"/"):
			rest := strings.TrimPrefix(f, expertSkillsDir+"/")
			slug, _, ok := strings.Cut(rest, "/")
			if !ok {
				return fmt.Errorf("%s: a skill lives in %s/<name>/SKILL.md", f, expertSkillsDir)
			}
			slugs[slug] = true
		default:
			return fmt.Errorf("%s: an expert holds only %s, %s, %s and %s/<name>/", f, ManifestFile, InstructionsFile, ReadmeFile, expertSkillsDir)
		}
	}
	names := sortedKeys(slugs)
	if len(names) > maxSkillsInPack {
		return fmt.Errorf("at most %d embedded skills", maxSkillsInPack)
	}
	for _, slug := range names {
		s, err := skillAt(p.Bundle, expertSkillsDir+"/"+slug, slug)
		if err != nil {
			return err
		}
		p.Skills = append(p.Skills, s)
	}
	return nil
}

// checkQuickPrompt: a quick prompt is a title and its text, or a reference
// "pack/prompt-id" to a prompt of a prompt pack the expert requires (its
// title then defaults to the prompt's).
func checkQuickPrompt(m Manifest, i int, q QuickPrompt) error {
	if err := checkText("quick prompt title", q.Title, maxQuickTitle, true); err != nil {
		return err
	}
	if q.Ref == "" {
		if strings.TrimSpace(q.Title) == "" || strings.TrimSpace(q.Prompt) == "" {
			return fmt.Errorf("quick prompt %d needs a title and a prompt", i+1)
		}
		return checkText("quick prompt", q.Prompt, maxQuickPrompt, false)
	}
	if q.Prompt != "" {
		return fmt.Errorf("quick prompt %d: give a prompt or a ref to a pack's prompt, not both", i+1)
	}
	pack, _, ok := ParsePromptRef(q.Ref)
	if !ok {
		return fmt.Errorf("quick prompt %d: ref %q: want pack-name/prompt-id", i+1, q.Ref)
	}
	for _, r := range m.Requires.Prompts {
		if ref, err := ParseReference(r); err == nil && ref.Name == pack {
			return nil
		}
	}
	return fmt.Errorf("quick prompt %d: ref %q: name the prompt pack %s in requires.prompts", i+1, q.Ref, pack)
}

// validateSkillPack: manifest skills lists every <slug>/SKILL.md of the
// pack, and nothing else lies at the top level but component.json and
// README.md.
func validateSkillPack(p *Package) error {
	m := p.Manifest
	if len(m.QuickPrompts) > 0 || m.Welcome != "" {
		return fmt.Errorf("quick prompts and a welcome belong to experts")
	}
	if len(m.Prompts) > 0 {
		return fmt.Errorf("prompts belong to prompt packs")
	}
	if len(m.Skills) == 0 {
		return fmt.Errorf("a skill pack lists its skills (component.json skills)")
	}
	if len(m.Skills) > maxSkillsInPack {
		return fmt.Errorf("at most %d skills in a pack", maxSkillsInPack)
	}
	listed := map[string]bool{}
	for _, slug := range m.Skills {
		if listed[slug] {
			return fmt.Errorf("skill %q listed twice", slug)
		}
		listed[slug] = true
	}
	present := map[string]bool{}
	for _, f := range p.Bundle.Paths() {
		if f == ManifestFile || f == ReadmeFile {
			continue
		}
		slug, _, ok := strings.Cut(f, "/")
		if !ok || !listed[slug] {
			return fmt.Errorf("%s: a skill pack holds only %s, %s and the listed <skill>/ directories", f, ManifestFile, ReadmeFile)
		}
		present[slug] = true
	}
	for _, slug := range m.Skills {
		if !present[slug] {
			return fmt.Errorf("skill %q is listed but has no %s/SKILL.md", slug, slug)
		}
		s, err := skillAt(p.Bundle, slug, slug)
		if err != nil {
			return err
		}
		p.Skills = append(p.Skills, s)
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParseFrontmatter reads a SKILL.md / agent .md YAML frontmatter block:
// "key: value" lines between a leading "---" and the next "---", with
// quoted values and ">"/"|" block scalars. It returns the keys and the body
// after the block (the whole text when there is none).
func ParseFrontmatter(text string) (map[string]string, string) {
	out := map[string]string{}
	t := strings.TrimPrefix(text, "\ufeff")
	t = strings.ReplaceAll(t, "\r\n", "\n")
	if !strings.HasPrefix(t, "---\n") {
		return out, text
	}
	rest := t[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return out, text
	}
	block := rest[:end]
	body := strings.TrimPrefix(rest[end+4:], "\n")
	lines := strings.Split(block, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, ">") || strings.HasPrefix(val, "|") {
			folded := strings.HasPrefix(val, ">")
			var parts []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || strings.TrimSpace(lines[i+1]) == "") {
				i++
				parts = append(parts, strings.TrimSpace(lines[i]))
			}
			sep := "\n"
			if folded {
				sep = " "
			}
			val = strings.TrimSpace(strings.Join(parts, sep))
		} else if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
			if strings.HasPrefix(line[strings.Index(line, ":")+1:], " \"") {
				val = strings.ReplaceAll(val, `\"`, `"`)
			}
		}
		if utf8.ValidString(val) {
			out[key] = val
		}
	}
	return out, body
}
