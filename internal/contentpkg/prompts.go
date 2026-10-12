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
	"unicode"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Prompt packs (docs/prd/2026-10-12-experts-and-teams.md §4): a curated,
// versioned set of prompts with variables, used from the composer (picker,
// /slash commands, a small form for the variables). The prompts live in
// component.json, so a listing reads them from the stored manifest; the
// bundle holds nothing else but an optional README.md.

// Prompt is one prompt of a pack. Body may hold {{name}} placeholders, each
// a declared variable.
type Prompt struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	Body      string           `json:"body"`
	Variables []PromptVariable `json:"variables,omitempty"`
	Tags      []string         `json:"tags,omitempty"`
	// Slash is the composer command that inserts it ("/release-notes").
	Slash string `json:"slash,omitempty"`
	// Starter offers it among a room's starter prompts (an empty room's
	// examples above the composer) when the pack is turned on for the room.
	Starter bool `json:"starter,omitempty"`
}

// PromptVariable is one {{name}} of a prompt: a text field, or a select
// when it lists options.
type PromptVariable struct {
	Name    string   `json:"name"`
	Label   string   `json:"label,omitempty"`
	Default string   `json:"default,omitempty"`
	Options []string `json:"options,omitempty"`
}

// Prompt pack bounds.
const (
	maxPromptsInPack  = 100
	maxPromptTitle    = 120
	maxPromptBody     = 8000
	maxPromptVars     = 20
	maxVarLabel       = 80
	maxVarDefault     = 1000
	maxVarOptions     = 30
	maxVarOption      = 200
	maxSlashCommand   = 40 // after the "/"
	maxVarName        = 40
	promptPackMaxSize = 1 << 20
)

// PromptLimits is the prompt pack bundle profile: component.json (which
// holds the prompts) and an optional README.md.
var PromptLimits = tarbundle.Limits{MaxBytes: promptPackMaxSize, MaxFiles: 2}

var (
	// placeholderRE matches a {{name}} placeholder (spaces inside the
	// braces allowed). Anything else between braces is literal text.
	placeholderRE = regexp.MustCompile(fmt.Sprintf(`\{\{\s*([A-Za-z_][A-Za-z0-9_]{0,%d})\s*\}\}`, maxVarName-1))
	varNameRE     = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z_][A-Za-z0-9_]{0,%d}$`, maxVarName-1))
	slashRE       = regexp.MustCompile(fmt.Sprintf(`^/[a-z0-9][a-z0-9-]{0,%d}$`, maxSlashCommand-1))
)

func init() {
	register(KindSpec{Kind: KindPrompt, Short: "prompt", Limits: PromptLimits, MemberUse: true,
		validate: validatePromptPack, fromForm: promptPackFromForm, fromImport: promptPackFromImport})
}

// PromptPlaceholders returns the distinct {{name}} placeholders of a body
// in order of first appearance.
func PromptPlaceholders(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholderRE.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ParsePromptRef parses an expert quick prompt's "pack/prompt-id".
func ParsePromptRef(ref string) (pack, id string, ok bool) {
	pack, id, ok = strings.Cut(strings.TrimSpace(ref), "/")
	return pack, id, ok && ValidName(pack) && ValidName(id)
}

// FindPrompt returns the prompt of a pack manifest by id.
func (m Manifest) FindPrompt(id string) (Prompt, bool) {
	for _, p := range m.Prompts {
		if p.ID == id {
			return p, true
		}
	}
	return Prompt{}, false
}

// validatePromptPack: the prompts in component.json, nothing but README.md
// beside it.
func validatePromptPack(p *Package) error {
	m := p.Manifest
	if len(m.QuickPrompts) > 0 || m.Welcome != "" || len(m.Skills) > 0 {
		return errors.New("a prompt pack holds prompts only (quick prompts, a welcome and skills belong to experts and skill packs)")
	}
	for _, f := range p.Bundle.Paths() {
		if f != ManifestFile && f != ReadmeFile {
			return fmt.Errorf("%s: a prompt pack holds only %s (with its prompts) and %s", f, ManifestFile, ReadmeFile)
		}
	}
	if len(m.Prompts) == 0 {
		return errors.New("a prompt pack lists at least one prompt (component.json prompts)")
	}
	if len(m.Prompts) > maxPromptsInPack {
		return fmt.Errorf("at most %d prompts in a pack", maxPromptsInPack)
	}
	ids := map[string]bool{}
	slashes := map[string]string{}
	for i, pr := range m.Prompts {
		at := fmt.Sprintf("prompt %d", i+1)
		if pr.ID != "" {
			at = fmt.Sprintf("prompt %s", pr.ID)
		}
		if !ValidName(pr.ID) {
			return fmt.Errorf("%s: id %q: use lower-case letters, digits and hyphens (at most %d)", at, pr.ID, maxName)
		}
		if ids[pr.ID] {
			return fmt.Errorf("prompt id %q is used twice", pr.ID)
		}
		ids[pr.ID] = true
		if strings.TrimSpace(pr.Title) == "" {
			return fmt.Errorf("%s needs a title", at)
		}
		if err := checkText(at+" title", pr.Title, maxPromptTitle, true); err != nil {
			return err
		}
		if strings.TrimSpace(pr.Body) == "" {
			return fmt.Errorf("%s needs a body", at)
		}
		if err := checkText(at+" body", pr.Body, maxPromptBody, false); err != nil {
			return err
		}
		if len(pr.Tags) > maxTags {
			return fmt.Errorf("%s: at most %d tags", at, maxTags)
		}
		for _, t := range pr.Tags {
			if len(t) > maxTag || !tagRE.MatchString(t) {
				return fmt.Errorf("%s: tag %q: use lower-case letters, digits, spaces, '_' and '-'", at, t)
			}
		}
		if pr.Slash != "" {
			if !slashRE.MatchString(pr.Slash) || len(pr.Slash) > maxSlashCommand+1 {
				return fmt.Errorf("%s: slash command %q: a '/' then lower-case letters, digits and hyphens", at, pr.Slash)
			}
			if other, dup := slashes[pr.Slash]; dup {
				return fmt.Errorf("slash command %s is used by %s and %s", pr.Slash, other, pr.ID)
			}
			slashes[pr.Slash] = pr.ID
		}
		if err := checkVariables(at, pr); err != nil {
			return err
		}
	}
	return nil
}

// checkVariables: every {{name}} in the body is declared, every declared
// variable is used, and each variable is well formed.
func checkVariables(at string, pr Prompt) error {
	if len(pr.Variables) > maxPromptVars {
		return fmt.Errorf("%s: at most %d variables", at, maxPromptVars)
	}
	declared := map[string]bool{}
	for _, v := range pr.Variables {
		if !varNameRE.MatchString(v.Name) {
			return fmt.Errorf("%s: variable name %q: letters, digits and '_', starting with a letter or '_'", at, v.Name)
		}
		if declared[v.Name] {
			return fmt.Errorf("%s: variable %q is declared twice", at, v.Name)
		}
		declared[v.Name] = true
		if err := checkText(at+" variable "+v.Name+" label", v.Label, maxVarLabel, true); err != nil {
			return err
		}
		if err := checkText(at+" variable "+v.Name+" default", v.Default, maxVarDefault, false); err != nil {
			return err
		}
		if len(v.Options) > maxVarOptions {
			return fmt.Errorf("%s: variable %s: at most %d options", at, v.Name, maxVarOptions)
		}
		opts := map[string]bool{}
		for _, o := range v.Options {
			if strings.TrimSpace(o) == "" {
				return fmt.Errorf("%s: variable %s has an empty option", at, v.Name)
			}
			if err := checkText(at+" variable "+v.Name+" option", o, maxVarOption, true); err != nil {
				return err
			}
			if opts[o] {
				return fmt.Errorf("%s: variable %s lists the option %q twice", at, v.Name, o)
			}
			opts[o] = true
		}
		if len(v.Options) > 0 && v.Default != "" && !opts[v.Default] {
			return fmt.Errorf("%s: variable %s: the default %q is not one of its options", at, v.Name, v.Default)
		}
	}
	used := map[string]bool{}
	for _, name := range PromptPlaceholders(pr.Body) {
		used[name] = true
		if !declared[name] {
			return fmt.Errorf("%s: {{%s}} is used in the body but not declared as a variable", at, name)
		}
	}
	for _, v := range pr.Variables {
		if !used[v.Name] {
			return fmt.Errorf("%s: variable %s is declared but not used as {{%s}} in the body", at, v.Name, v.Name)
		}
	}
	return nil
}

func promptPackFromForm(f Form, m *Manifest, _ map[string][]byte) error {
	if strings.TrimSpace(f.Instructions) != "" || len(f.QuickPrompts) > 0 || f.Welcome != "" || len(f.Skills) > 0 {
		return errors.New("a prompt pack holds prompts only (instructions, quick prompts, a welcome and skills belong to experts and skill packs)")
	}
	for _, p := range f.Prompts {
		p.ID, p.Title, p.Slash = strings.TrimSpace(p.ID), strings.TrimSpace(p.Title), strings.TrimSpace(p.Slash)
		p.Body = strings.TrimSpace(p.Body)
		m.Prompts = append(m.Prompts, p)
	}
	return nil
}

// ---------------------------------------------------------------- import

// promptPackFromImport reads prompts from the upload's markdown and json
// files (README.md stays the pack's README): a markdown file holds one
// prompt per heading, a json file a list of prompts or {prompts: [...]}.
// Placeholders in a body are declared as variables, unused declared ones
// dropped, ids and slash commands made unique.
func promptPackFromImport(files map[string][]byte, m *Manifest, _ map[string][]byte) error {
	names := make([]string, 0, len(files))
	for p := range files {
		names = append(names, p)
	}
	sort.Strings(names)
	var drafts []Prompt
	var meta importMeta
	for _, p := range names {
		if p == ReadmeFile {
			continue
		}
		data := files[p]
		if !utf8.Valid(data) {
			return fmt.Errorf("%s is not UTF-8 text", p)
		}
		var got []Prompt
		var fm importMeta
		var err error
		switch strings.ToLower(path.Ext(p)) {
		case ".json":
			got, fm, err = promptsFromJSON(data)
		case ".md", ".markdown", ".txt":
			got, fm = promptsFromMarkdown(string(data), strings.TrimSuffix(path.Base(p), path.Ext(p)))
		default:
			return fmt.Errorf("%s: a prompt import holds markdown (.md) or json files", p)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		meta.fill(fm)
		if meta.fileBase == "" {
			meta.fileBase = strings.TrimSuffix(path.Base(p), path.Ext(p))
		}
		drafts = append(drafts, got...)
	}
	if len(drafts) == 0 {
		return errors.New("no prompts found: import a markdown file with one prompt per heading, or a json list of prompts")
	}
	if len(drafts) > maxPromptsInPack {
		return fmt.Errorf("the import holds %d prompts; a pack holds at most %d", len(drafts), maxPromptsInPack)
	}
	m.Prompts = NormalizePrompts(drafts)
	m.Name = firstNonEmpty(m.Name, slugify(meta.name), slugify(meta.displayName), slugify(meta.fileBase))
	m.DisplayName = firstNonEmpty(meta.displayName, meta.name)
	m.Description = meta.description
	return nil
}

// importMeta is pack metadata found in an import (json fields, markdown
// frontmatter or its top heading).
type importMeta struct {
	name, displayName, description, fileBase string
}

func (m *importMeta) fill(o importMeta) {
	m.name = firstNonEmpty(m.name, o.name)
	m.displayName = firstNonEmpty(m.displayName, o.displayName)
	m.description = firstNonEmpty(m.description, o.description)
}

// importedPrompt is a prompt in a foreign json: the bnbloc fields plus the
// common aliases of other prompt tools.
type importedPrompt struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	Name      string           `json:"name"`
	Body      string           `json:"body"`
	Prompt    string           `json:"prompt"`
	Content   string           `json:"content"`
	Text      string           `json:"text"`
	Variables []PromptVariable `json:"variables"`
	Tags      []string         `json:"tags"`
	Slash     string           `json:"slash"`
	Command   string           `json:"command"`
	Starter   bool             `json:"starter"`
}

func promptsFromJSON(data []byte) ([]Prompt, importMeta, error) {
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\ufeff")))
	var list []importedPrompt
	var meta importMeta
	if bytes.HasPrefix(data, []byte("[")) {
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, meta, fmt.Errorf("not a json list of prompts: %w", err)
		}
	} else {
		var doc struct {
			Name        string           `json:"name"`
			DisplayName string           `json:"displayName"`
			Title       string           `json:"title"`
			Description string           `json:"description"`
			Prompts     []importedPrompt `json:"prompts"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, meta, fmt.Errorf("not json prompts: %w", err)
		}
		list = doc.Prompts
		meta = importMeta{name: doc.Name, displayName: firstNonEmpty(doc.DisplayName, doc.Title), description: doc.Description}
	}
	out := make([]Prompt, 0, len(list))
	for _, ip := range list {
		body := firstNonEmpty(ip.Body, ip.Prompt, ip.Content, ip.Text)
		if body == "" {
			continue
		}
		out = append(out, Prompt{ID: ip.ID, Title: firstNonEmpty(ip.Title, ip.Name), Body: body,
			Variables: ip.Variables, Tags: ip.Tags, Slash: firstNonEmpty(ip.Slash, ip.Command), Starter: ip.Starter})
	}
	return out, meta, nil
}

var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)

// promptsFromMarkdown splits a markdown file into prompts: one per
// second-level heading when it has any (a top "# " heading then names the
// pack, the text under it describes it), else one per first-level heading,
// else the whole file is one prompt. Lines "id:", "slash:", "tags:" and
// "starter:" (yes/no)
// right under a prompt's heading set those fields. Headings inside code
// fences do not split.
func promptsFromMarkdown(text, fileBase string) ([]Prompt, importMeta) {
	fm, body := ParseFrontmatter(text)
	meta := importMeta{name: fm["name"], displayName: firstNonEmpty(fm["displayName"], fm["title"]), description: fm["description"]}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	type heading struct {
		line, level int
		title       string
	}
	var heads []heading
	fence := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if h := headingRE.FindStringSubmatch(l); h != nil && len(h[1]) <= 2 {
			heads = append(heads, heading{line: i, level: len(h[1]), title: strings.TrimSpace(h[2])})
		}
	}
	split := 0
	for _, h := range heads {
		if h.level == 2 {
			split = 2
			break
		}
	}
	if split == 0 && len(heads) > 0 {
		split = 1
	}
	if split == 0 {
		b := strings.TrimSpace(body)
		if b == "" {
			return nil, meta
		}
		return []Prompt{{Title: firstNonEmpty(meta.displayName, firstLine(b), fileBase), Body: b}}, meta
	}
	var out []Prompt
	var cur *Prompt
	var buf []string
	flush := func() {
		if cur == nil {
			return
		}
		cur.Body = strings.TrimSpace(strings.Join(buf, "\n"))
		if cur.Body != "" {
			out = append(out, *cur)
		}
		cur, buf = nil, nil
	}
	hi := 0
	var intro []string
	for i, l := range lines {
		if hi < len(heads) && heads[hi].line == i {
			h := heads[hi]
			hi++
			if h.level == split {
				flush()
				cur = &Prompt{Title: h.title}
				continue
			}
			if cur == nil && split == 2 && h.level == 1 {
				meta.displayName = firstNonEmpty(meta.displayName, h.title)
				continue
			}
		}
		if cur == nil {
			intro = append(intro, l)
			continue
		}
		if len(buf) == 0 || allBlank(buf) {
			if k, v, ok := promptMetaLine(l); ok {
				switch k {
				case "id":
					cur.ID = v
				case "slash":
					cur.Slash = v
				case "tags":
					cur.Tags = splitTags(v)
				case "starter":
					cur.Starter = isYes(v)
				}
				continue
			}
		}
		buf = append(buf, l)
	}
	flush()
	if split == 2 {
		meta.description = firstNonEmpty(meta.description, strings.TrimSpace(strings.Join(intro, " ")))
	}
	return out, meta
}

func allBlank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// isYes reads a yes/no meta value ("starter: yes").
func isYes(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "true", "y", "1", "on":
		return true
	}
	return false
}

// promptMetaLine reads "id: x", "slash: /x", "tags: a, b" or "starter: yes"
// under a heading.
func promptMetaLine(l string) (string, string, bool) {
	k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
	if !ok {
		return "", "", false
	}
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case "id", "slash", "tags", "starter":
		return k, strings.TrimSpace(v), true
	}
	return "", "", false
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' }) {
		if t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#")); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); l != "" {
			return l
		}
	}
	return ""
}

// NormalizePrompts makes imported or saved prompts valid where that is
// mechanical: ids from titles (unique), titles bounded to one line, tags
// lower-cased (invalid ones dropped), slash commands normalized (invalid
// or repeated ones dropped), placeholders declared as variables (a
// declared variable keeps its label, default and options) and unused
// declared variables dropped.
func NormalizePrompts(in []Prompt) []Prompt {
	out := make([]Prompt, 0, len(in))
	ids := map[string]bool{}
	slashes := map[string]bool{}
	for _, p := range in {
		p.Body = strings.TrimSpace(p.Body)
		p.Title = oneLine(firstNonEmpty(p.Title, firstLine(p.Body)), maxPromptTitle)
		id := slugify(firstNonEmpty(p.ID, p.Title))
		if id == "" {
			id = "prompt"
		}
		p.ID = uniqueID(id, ids)
		var tags []string
		seenTag := map[string]bool{}
		for _, t := range p.Tags {
			t = strings.ToLower(strings.TrimSpace(t))
			if t != "" && len(t) <= maxTag && tagRE.MatchString(t) && !seenTag[t] && len(tags) < maxTags {
				seenTag[t] = true
				tags = append(tags, t)
			}
		}
		p.Tags = tags
		if s := strings.ToLower(strings.TrimSpace(p.Slash)); s != "" {
			if !strings.HasPrefix(s, "/") {
				s = "/" + s
			}
			if slashRE.MatchString(s) && !slashes[s] {
				slashes[s] = true
				p.Slash = s
			} else {
				p.Slash = ""
			}
		}
		p.Variables = DeclarePlaceholders(p.Body, p.Variables)
		out = append(out, p)
	}
	return out
}

// DeclarePlaceholders returns the variables of a body: each {{name}} in
// order, with the declared variable's fields when given (else a label from
// the name), dropping declared variables the body does not use.
func DeclarePlaceholders(body string, declared []PromptVariable) []PromptVariable {
	byName := map[string]PromptVariable{}
	for _, v := range declared {
		byName[v.Name] = v
	}
	var out []PromptVariable
	for _, name := range PromptPlaceholders(body) {
		v, ok := byName[name]
		if !ok {
			v = PromptVariable{Name: name}
		}
		if strings.TrimSpace(v.Label) == "" {
			v.Label = variableLabel(name)
		}
		out = append(out, v)
	}
	return out
}

// variableLabel turns a variable name into a label ("target_audience" ->
// "Target audience").
func variableLabel(name string) string {
	words := strings.Fields(strings.ReplaceAll(name, "_", " "))
	s := strings.Join(words, " ")
	if s == "" {
		return name
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// UniquePromptID returns an id for a new prompt titled title that no
// prompt of the pack uses yet.
func UniquePromptID(title string, existing []Prompt) string {
	taken := map[string]bool{}
	for _, p := range existing {
		taken[p.ID] = true
	}
	id := slugify(title)
	if id == "" {
		id = "prompt"
	}
	return uniqueID(id, taken)
}

func uniqueID(id string, taken map[string]bool) string {
	cand := id
	for n := 2; taken[cand]; n++ {
		suffix := fmt.Sprintf("-%d", n)
		base := id
		if len(base)+len(suffix) > 63 {
			base = strings.Trim(base[:63-len(suffix)], "-")
		}
		cand = base + suffix
	}
	taken[cand] = true
	return cand
}
