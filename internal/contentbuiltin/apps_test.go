package contentbuiltin

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/gateway/appsdk"
)

// The interactive apps' built-ins (docs/prd/2026-10-12-interactive-apps.md
// §4.6): the skill pack documenting the app SDK, the expert wearing it and
// the starter prompts.
const (
	appsSkillID     = IDPrefix + "interactive-apps"
	appBuilderID    = IDPrefix + "app-builder"
	appStartersID   = IDPrefix + "app-starters"
	appsSkillFile   = "interactive-apps/SKILL.md"
	appsExamples    = "interactive-apps/references/examples.md"
	appsSelfTest    = "interactive-apps/references/self-test.md"
	sdkBegin        = "<!-- sdk:begin -->"
	sdkEnd          = "<!-- sdk:end -->"
	appsSkillMaxTk  = 1870 // below the 8 KiB a core without agentProfile inlines a skill body at
	appBuilderMaxTk = 700
)

func builtinFile(t *testing.T, id, name string) string {
	t.Helper()
	p, ok := Get(id)
	if !ok {
		t.Fatalf("built-in %s missing", id)
	}
	raw, ok := p.Validated.Bundle.File(name)
	if !ok {
		t.Fatalf("%s has no %s", id, name)
	}
	return string(raw)
}

var (
	tsCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)
	// tsMemberRE is a declared name: a member or parameter ("name:",
	// "name?:") or a method ("name(", "name<T>(").
	tsMemberRE = regexp.MustCompile(`([A-Za-z_$][\w$]*)\??\s*(?:<.*?>)?\s*[:(]`)
	// tsInterfaceRE is one interface's body (no nested braces at its own
	// level but inline object types, which stay inside).
	tsInterfaceRE = regexp.MustCompile(`(?s)interface (\w+)[^{\n]*\{\n(.*?)\n  \}`)
	tsMethodRE    = regexp.MustCompile(`(?m)^\s{4}(\w+)(?:<.*?>)?\(`)
	tsOptionRE    = regexp.MustCompile(`[{;]\s*([\w$]+)\?:`)
	codeSpanRE    = regexp.MustCompile("`([^`\n]+)`")
	identRE       = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	appCallRE     = regexp.MustCompile(`\bapp\.(\w+)\(`)
	collVarRE     = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*=\s*app\.collection\(`)
)

// sdkDecl is what sdk.d.ts declares: every name, and the methods and
// option names of the app and of a collection.
type sdkDecl struct {
	names             map[string]bool
	appMethods        []string
	collectionMethods []string
	collectionOptions []string
}

func readSDKDecl(t *testing.T) sdkDecl {
	t.Helper()
	src := tsCommentRE.ReplaceAllString(string(appsdk.Types), "")
	d := sdkDecl{names: map[string]bool{}}
	for _, m := range tsMemberRE.FindAllStringSubmatch(src, -1) {
		d.names[m[1]] = true
	}
	for _, m := range tsInterfaceRE.FindAllStringSubmatch(src, -1) {
		var methods []string
		for _, mm := range tsMethodRE.FindAllStringSubmatch(m[2], -1) {
			methods = append(methods, mm[1])
		}
		switch m[1] {
		case "BnblocApp":
			d.appMethods = methods
		case "BnblocAppCollection":
			d.collectionMethods = methods
			for _, o := range tsOptionRE.FindAllStringSubmatch(m[2], -1) {
				if !slices.Contains(d.collectionOptions, o[1]) {
					d.collectionOptions = append(d.collectionOptions, o[1])
				}
			}
		}
	}
	t.Logf("sdk.d.ts: app %v, collection %v, options %v", d.appMethods, d.collectionMethods, d.collectionOptions)
	if len(d.appMethods) == 0 || len(d.collectionMethods) == 0 || len(d.collectionOptions) == 0 {
		t.Fatalf("sdk.d.ts parsed to %+v: the parser no longer reads it", d)
	}
	return d
}

// The skill documents the SDK the gateway serves: every name its SDK
// section uses is declared in sdk.d.ts, every method of the app and of a
// collection and every option a collection call takes is documented, every
// SDK call in its examples names a declared method, and the filter
// operators and the caps are the server's own. A renamed or added SDK
// method fails here until the skill follows.
func TestInteractiveAppsSkillMatchesSDK(t *testing.T) {
	d := readSDKDecl(t)
	skill := builtinFile(t, appsSkillID, appsSkillFile)
	examples := builtinFile(t, appsSkillID, appsExamples)
	selfTest := builtinFile(t, appsSkillID, appsSelfTest)
	start, end := strings.Index(skill, sdkBegin), strings.Index(skill, sdkEnd)
	if start < 0 || end < start {
		t.Fatalf("the skill has no %s … %s section", sdkBegin, sdkEnd)
	}
	section := skill[start:end]
	// Names in the section's code spans: SDK names, besides the literals
	// and the page's own variable.
	plain := map[string]bool{"app": true, "true": true, "false": true}
	documented := map[string]bool{}
	for _, span := range codeSpanRE.FindAllStringSubmatch(section, -1) {
		for _, name := range identRE.FindAllString(span[1], -1) {
			documented[name] = true
			if !plain[name] && !d.names[name] {
				t.Errorf("the skill documents %q (in `%s`), which sdk.d.ts does not declare", name, span[1])
			}
		}
	}
	for _, list := range [][]string{d.appMethods, d.collectionMethods, d.collectionOptions} {
		for _, name := range list {
			if !documented[name] {
				t.Errorf("the skill's SDK section does not document %q of sdk.d.ts", name)
			}
		}
	}
	// Every SDK call in the skill and its examples is a declared method.
	for name, text := range map[string]string{appsSkillFile: skill, appsExamples: examples, appsSelfTest: selfTest} {
		for _, m := range appCallRE.FindAllStringSubmatch(text, -1) {
			if !slices.Contains(d.appMethods, m[1]) {
				t.Errorf("%s calls app.%s, which the SDK does not have", name, m[1])
			}
		}
		for _, v := range collVarRE.FindAllStringSubmatch(text, -1) {
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(v[1]) + `\.(\w+)\(`)
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				if !slices.Contains(d.collectionMethods, m[1]) {
					t.Errorf("%s calls %s.%s on a collection, which has no such method", name, v[1], m[1])
				}
			}
		}
	}
	for _, want := range []string{
		`<script src="~app/sdk.js"></script>`,
		strings.Join(appdata.FilterOps, " "),
		fmt.Sprintf("%d KiB per document, %d documents or %d MiB per app, %d\ncollections per app", wire.MaxAppDocBytes>>10, wire.MaxAppDocs, wire.MaxAppBytes>>20, wire.MaxAppCollections),
		"`{public: true}`", "{ifAbsent: true}", "`$inc`", "`subscribe`", "app_data", "`index.html`", "`dist/`",
		// The cover convention (docs/prd/2026-10-13-app-gallery.md §4.1):
		// the file the app validator reads, within its cap.
		"`cover.png`", "headless browser", fmt.Sprintf("under %d MiB", wire.MaxWorkCoverBytes>>20), "never SVG",
		// The self-test before delivery
		// (docs/prd/2026-10-13-interactive-apps-self-test.md): the three
		// checks, the never-claim-done rule, the demo-mode SDK bnbloc
		// writes into the room records (roomlog.AppSDKPath) and the harness.
		"no console error", "visible content", "writes through the SDK", "read back",
		"Never say an app is done or works", "`" + strings.TrimPrefix(roomlog.AppSDKPath, core.RoomContextDir) + "`", "`references/self-test.md`",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("the skill does not state %q", want)
		}
	}
	// The harness serves that SDK as the page's own and checks what the
	// skill promises.
	for _, want := range []string{`"/~app/sdk.js"`, "pageerror", "innerText", "app.collection(", "never call the\napp done"} {
		if !strings.Contains(selfTest, want) {
			t.Errorf("the self-test reference does not hold %q", want)
		}
	}
}

// The always-on parts stay small: the expert's instructions ride every
// request of its sessions, and a core without agentProfile inlines the
// skill's body into the first prompt (up to 8 KiB); the long examples are
// the skill's reference file, read on demand.
func TestAppBuiltinsAlwaysOnBudget(t *testing.T) {
	for _, c := range []struct {
		what, text string
		max        int
	}{
		{"the app builder's INSTRUCTIONS.md", builtinFile(t, appBuilderID, contentpkg.InstructionsFile), appBuilderMaxTk},
		{"the interactive-apps SKILL.md", builtinFile(t, appsSkillID, appsSkillFile), appsSkillMaxTk},
	} {
		if tokens := len(c.text) / 4; tokens > c.max {
			t.Errorf("%s is about %d tokens, over its budget of %d: move examples to the skill's reference file", c.what, tokens, c.max)
		}
	}
}

// The App builder wears the interactive-apps skill pack and offers the
// app starters as its quick prompts: both resolve for any member, as a
// job and as the composer would.
func TestAppBuilderRequiresItsSkillAndPrompts(t *testing.T) {
	ctx := context.Background()
	st := Overlay(store.NewMemoryContentComponents())
	builder, ok := Get(appBuilderID)
	if !ok {
		t.Fatal("the app builder is missing")
	}
	m := builder.Validated.Manifest
	if !slices.Contains(m.Requires.Skills, "interactive-apps@^1") || !slices.Contains(m.Requires.Prompts, "app-starters@^1") {
		t.Fatalf("the app builder requires %+v", m.Requires)
	}
	if !strings.Contains(builder.Validated.Instructions, "`interactive-apps`") || !strings.Contains(builder.Validated.Instructions, "app_data") {
		t.Error("the app builder's instructions do not point at its skill and app_data")
	}
	eff, err := contentuse.Effective(ctx, st, store.AgentJob{RoomID: "r1", TenantID: "t1", RequesterUserID: "maya", OwnerUserID: "jack", ExpertID: appBuilderID, ExpertVersion: builder.Version.Version})
	if err != nil || eff.Expert == nil || len(eff.Missing) != 0 || len(eff.SkillPacks) != 1 || eff.SkillPacks[0].ComponentID != appsSkillID {
		t.Fatalf("effective = %+v, %v", eff, err)
	}
	maya := extpipe.Viewer{Subject: "maya", Tenant: "t1"}
	for _, q := range m.QuickPrompts {
		ref, err := contentuse.ResolvePromptRef(ctx, st, maya, "r1", m.Requires.Prompts, q.Ref)
		if err != nil || ref == nil {
			t.Errorf("quick prompt %q does not resolve: %+v, %v", q.Ref, ref, err)
		}
	}
	starters, _ := Get(appStartersID)
	var slashes []string
	for _, p := range starters.Validated.Manifest.Prompts {
		slashes = append(slashes, p.Slash)
		if len(p.Variables) == 0 {
			t.Errorf("starter %s asks for nothing: each takes the app's specifics as variables", p.ID)
		}
	}
	sort.Strings(slashes)
	if strings.Join(slashes, " ") != "/app-report /poll /signup /survey" {
		t.Fatalf("app starters' commands = %v", slashes)
	}
}
