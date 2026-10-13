package contentpkg

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Assists (docs/prd/2026-10-13-ai-assist.md §3): a task the assist model
// runs behind a manual entry point. INSTRUCTIONS.md is the task's system
// prompt; the manifest names its output (streamed text, or a JSON object
// of the declared fields) and the context it may receive. An assist never
// reaches an agent: it is not materialized into a workspace and is no
// content use of a room or member.

// Text bounds of an assist.
const (
	// maxAssistInstructions bounds the system prompt (every call sends it).
	maxAssistInstructions = 16 << 10
	maxAssistFields       = 12
	maxAssistFieldName    = 40
	maxAssistFieldDesc    = 300
)

// AssistLimits is an assist's bundle profile: component.json,
// INSTRUCTIONS.md and README.md.
var AssistLimits = tarbundle.Limits{MaxBytes: 64 << 10, MaxFiles: 3}

// assistFieldRE is a JSON reply's field name (camelCase or snake_case, as
// the web fills form fields by them).
var assistFieldRE = regexp.MustCompile(fmt.Sprintf(`^[a-zA-Z][a-zA-Z0-9_]{0,%d}$`, maxAssistFieldName-1))

var errAssistFields = errors.New("an output, fields and a context belong to an assist")

func init() {
	register(KindSpec{Kind: KindAssist, Short: "assist", Limits: AssistLimits,
		validate: validateAssist, fromForm: assistFromForm})
}

// validateAssist: INSTRUCTIONS.md (non-empty, bounded), an output, the
// fields of a JSON output (none for text), a context of known names, no
// requirements, and nothing else but component.json and README.md.
func validateAssist(p *Package) error {
	m := p.Manifest
	switch {
	case len(m.QuickPrompts) > 0 || m.Welcome != "":
		return errors.New("quick prompts and a welcome belong to experts")
	case len(m.Skills) > 0:
		return errors.New("an assist lists no skills")
	case len(m.Prompts) > 0:
		return errors.New("prompts belong to prompt packs")
	case len(m.Requires.Skills)+len(m.Requires.Prompts)+len(m.Requires.MCP)+len(m.Requires.Permissions) > 0:
		return errors.New("an assist requires nothing: it runs on the assist model, never on an agent")
	}
	for _, f := range p.Bundle.Paths() {
		if f != ManifestFile && f != InstructionsFile && f != ReadmeFile {
			return fmt.Errorf("%s: an assist holds only %s, %s and %s", f, ManifestFile, InstructionsFile, ReadmeFile)
		}
	}
	text, err := readText(p.Bundle, InstructionsFile, maxAssistInstructions)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s is empty", InstructionsFile)
	}
	p.Instructions = text
	if !slices.Contains(wire.AssistOutputs, m.Output) {
		return fmt.Errorf("output %q: an assist's output is one of %s", m.Output, strings.Join(wire.AssistOutputs, ", "))
	}
	if err := checkAssistFields(m); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range m.Context {
		if !slices.Contains(wire.AssistContexts, c) {
			return fmt.Errorf("context %q: one of %s", c, strings.Join(wire.AssistContexts, ", "))
		}
		if seen[c] {
			return fmt.Errorf("context %q named twice", c)
		}
		seen[c] = true
	}
	return nil
}

// checkAssistFields: a JSON output declares at least one field, each with
// a unique name; a text output declares none.
func checkAssistFields(m Manifest) error {
	if m.Output == wire.AssistOutputText {
		if len(m.Fields) > 0 {
			return errors.New("a text assist declares no fields (they name a JSON reply's)")
		}
		return nil
	}
	if len(m.Fields) == 0 {
		return errors.New("a JSON assist declares the fields its reply holds")
	}
	if len(m.Fields) > maxAssistFields {
		return fmt.Errorf("at most %d fields", maxAssistFields)
	}
	seen := map[string]bool{}
	for i, f := range m.Fields {
		if !assistFieldRE.MatchString(f.Name) {
			return fmt.Errorf("field %d: name %q: letters, digits and '_', starting with a letter (at most %d)", i+1, f.Name, maxAssistFieldName)
		}
		if seen[f.Name] {
			return fmt.Errorf("field %q named twice", f.Name)
		}
		seen[f.Name] = true
		if err := checkText("field "+f.Name+" description", f.Description, maxAssistFieldDesc, true); err != nil {
			return err
		}
	}
	return nil
}

func assistFromForm(f Form, m *Manifest, files map[string][]byte) error {
	if len(f.QuickPrompts) > 0 || f.Welcome != "" || len(f.Skills) > 0 || f.Team != nil {
		return errors.New("quick prompts, a welcome, skills and a team belong to experts and skill packs")
	}
	if len(f.Prompts) > 0 {
		return errors.New("prompts belong to prompt packs")
	}
	m.Output = strings.TrimSpace(f.Output)
	for _, fd := range f.Fields {
		m.Fields = append(m.Fields, wire.AssistField{Name: strings.TrimSpace(fd.Name), Description: strings.TrimSpace(fd.Description), List: fd.List})
	}
	m.Context = f.Context
	files[InstructionsFile] = []byte(strings.TrimSpace(f.Instructions) + "\n")
	return nil
}

// AssistSpec is an assist manifest's task as the API shows it.
func (m Manifest) AssistSpec() *wire.AssistSpec {
	if m.Kind != KindAssist {
		return nil
	}
	return &wire.AssistSpec{Output: m.Output, Fields: m.Fields, Context: m.Context}
}
