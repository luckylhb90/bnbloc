package contentpkg

import (
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// assistBundle is an assist package: a JSON task with one field by
// default, extra manifest fields set or removed (nil), and files.
func assistBundle(t *testing.T, extra map[string]any, files map[string]string) []byte {
	t.Helper()
	m := map[string]any{"kind": KindAssist, "name": "fill-invite", "version": "1.0.0", "displayName": "Fill invite",
		"output": wire.AssistOutputJSON, "fields": []map[string]any{{"name": "title", "description": "One line"}}, "context": []string{wire.AssistContextRoom}}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	all := map[string][]byte{ManifestFile: manifestJSON(t, m), InstructionsFile: []byte("Write an invite.\n")}
	for k, v := range files {
		if v == "" {
			delete(all, k)
			continue
		}
		all[k] = []byte(v)
	}
	raw, err := tarbundle.PackFiles(all, tarbundle.ContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAssistValidation(t *testing.T) {
	p, err := Validate(assistBundle(t, nil, nil), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Instructions != "Write an invite.\n" || p.Manifest.AssistSpec().Fields[0].Name != "title" {
		t.Fatalf("package = %+v", p)
	}
	if _, err := Validate(assistBundle(t, map[string]any{"output": wire.AssistOutputText, "fields": nil}, nil), "", Options{}); err != nil {
		t.Fatalf("a text assist: %v", err)
	}
	bad := []struct {
		name  string
		extra map[string]any
		files map[string]string
		want  string
	}{
		{"no output", map[string]any{"output": nil}, nil, "output"},
		{"unknown output", map[string]any{"output": "html"}, nil, "output"},
		{"json without fields", map[string]any{"fields": nil}, nil, "declares the fields"},
		{"text with fields", map[string]any{"output": wire.AssistOutputText}, nil, "declares no fields"},
		{"bad field name", map[string]any{"fields": []map[string]any{{"name": "1st"}}}, nil, "name"},
		{"duplicate field", map[string]any{"fields": []map[string]any{{"name": "a"}, {"name": "a"}}}, nil, "twice"},
		{"unknown context", map[string]any{"context": []string{"inbox"}}, nil, "context"},
		{"requires", map[string]any{"requires": map[string]any{"skills": []string{"x"}}}, nil, "requires nothing"},
		{"no instructions", nil, map[string]string{InstructionsFile: ""}, InstructionsFile},
		{"empty instructions", nil, map[string]string{InstructionsFile: "  \n"}, "empty"},
		{"too long instructions", nil, map[string]string{InstructionsFile: strings.Repeat("a", maxAssistInstructions+1)}, InstructionsFile},
		{"another file", nil, map[string]string{"notes.md": "x"}, "holds only"},
	}
	for _, c := range bad {
		if _, err := Validate(assistBundle(t, c.extra, c.files), "", Options{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// The assist fields belong to assists; an assist built from a form.
func TestAssistFieldsBelongToAssists(t *testing.T) {
	f := Form{Kind: "expert", Name: "x", Instructions: "Be helpful.", Output: wire.AssistOutputText}
	if _, _, err := Build(f, "1.0.0", Options{}, nil); err == nil || !strings.Contains(err.Error(), "belong to an assist") {
		t.Fatalf("an expert form with an output: %v", err)
	}
	p, _, err := Build(Form{Kind: "assist", Name: "polish", Instructions: "Polish the text.", Output: wire.AssistOutputText,
		Context: []string{wire.AssistContextRoom}}, "1.0.0", Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Output != wire.AssistOutputText || p.Instructions != "Polish the text.\n" {
		t.Fatalf("built = %+v", p.Manifest)
	}
	if spec, _ := Lookup("assist"); spec.Use || spec.MemberUse {
		t.Fatal("an assist is no content use")
	}
}
