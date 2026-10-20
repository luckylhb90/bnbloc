package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The platform ships no content (docs/prd/2026-10-20-no-built-in-content.md):
// the default content is published by an operator like any platform
// content. These helpers stand in for it in tests: a package saved by an
// author, exported, signed and installed for every organization through
// the platform install route, exactly the path `bnbloc content publish`
// takes once the bundle is uploaded.

// defaultAuthor saves the packages the tests publish; nobody else sees
// its own copies.
const defaultAuthor = "publisher"

// publishDefault installs form as platform content and returns its id.
func (e *appGW) publishDefault(form map[string]any) string {
	e.t.Helper()
	r := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": form}, e.as(defaultAuthor))
	if r.status != http.StatusCreated {
		e.t.Fatalf("save %v = %d %s", form["name"], r.status, r.body)
	}
	src := decodeDetail(e.t, r.body).Component
	r = e.call(http.MethodGet, "/v1/platform/content-components/"+src.ID+"/package", nil, e.superAdmin())
	if r.status != http.StatusOK {
		e.t.Fatalf("export %s = %d %s", src.Name, r.status, r.body)
	}
	if e.gw.ExtPipe == nil || e.gw.ExtPipe.Settings != e.gw.PlatformSettings {
		// The install pipeline reads the trusted keys from the settings
		// the gateway has now.
		e.gw.ExtPipe = NewCorePipeline(e.gw, e.gw.ExtAudit)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	trusted, _ := json.Marshal([]pkgmeta.TrustedKey{{KeyID: "0102030405060708", PublicKey: minisignTestPublicKey("0102030405060708", pub)}})
	if err := e.gw.PlatformSettings.PutPlatformSetting(context.Background(), store.PlatformSetting{Key: componentTrustedKeysKey, Value: string(trusted)}); err != nil {
		e.t.Fatal(err)
	}
	r = e.call(http.MethodPost, "/v1/platform/components", wire.ComponentInstallRequest{Package: string(r.body), Signature: signPackage(priv, r.body), Rollout: "auto"}, e.superAdmin())
	if r.status != http.StatusOK {
		e.t.Fatalf("install %s = %d %s", src.Name, r.status, r.body)
	}
	var inst contentInstallReply
	_ = json.Unmarshal(r.body, &inst)
	return inst.ID
}

// assistForm is an assist named task that meets its output contract
// (wire.AssistContracts), with instructions as its system prompt.
func assistForm(t *testing.T, task, instructions string) map[string]any {
	t.Helper()
	c, ok := wire.AssistContractOf(task)
	if !ok {
		t.Fatalf("%s has no contract", task)
	}
	form := map[string]any{"kind": "assist", "name": task, "displayName": task, "instructions": instructions, "output": c.Output}
	var fields []wire.AssistField
	for _, f := range c.Fields {
		af := wire.AssistField{Name: f.Name, Type: f.Type, Description: "the " + f.Name}
		for _, it := range f.Item {
			af.Item = append(af.Item, wire.AssistField{Name: it.Name, Description: "the " + it.Name})
		}
		fields = append(fields, af)
	}
	if len(fields) > 0 {
		form["fields"] = fields
	}
	if ctx := defaultContexts[task]; len(ctx) > 0 {
		form["context"] = ctx
	}
	if c.Gate {
		form["gate"] = wire.AssistGate{Question: "Does the reply leave what the member asked for undone?", Threshold: 0.7}
	}
	return form
}

// defaultContexts are the contexts the stand-ins declare (what a package
// receives is its own choice; the contract holds only its output).
var defaultContexts = map[string][]string{
	wire.AssistTaskEnhancePrompt: {wire.AssistContextRoom, wire.AssistContextAttachments},
	wire.AssistTaskDescribeWork:  {wire.AssistContextArtifact},
	wire.AssistTaskDescribeRoom:  {wire.AssistContextConversation},
	wire.AssistTaskPlanRoom:      {wire.AssistContextLibrary},
	wire.AssistTaskDraftSchedule: {wire.AssistContextClock},
	wire.AssistTaskFindAWay:      {wire.AssistContextRoom, wire.AssistContextCapabilities},
}

// defaultAssists are the stand-ins the assist tests run: what each test
// reads of a task's system prompt is in its instructions.
var defaultAssists = map[string]string{
	wire.AssistTaskEnhancePrompt:    "You improve a request before it is sent to an agent.",
	wire.AssistTaskDescribeWork:     "You describe a work for the gallery.",
	wire.AssistTaskDescribeRoom:     "You describe a room being saved as a component.",
	wire.AssistTaskPlanRoom:         "You plan a room from the library.",
	wire.AssistTaskDraftSchedule:    "You draft a schedule.",
	wire.AssistTaskDraftPromptPack:  "You draft a prompt pack.",
	wire.AssistTaskFindAWay:         "You find a way past a request left undone.",
	wire.AssistTaskExplainSourceUse: "You help an admin decide whether a room may read records.",
}

// publishDefaultAssists publishes every stand-in of defaultAssists, and
// the expert and skill pack the library context lists.
func (e *appGW) publishDefaultAssists() {
	e.t.Helper()
	for task, instructions := range defaultAssists {
		e.publishDefault(assistForm(e.t, task, instructions))
	}
	e.publishDefault(map[string]any{"kind": "skill", "name": "interactive-apps", "displayName": "Interactive apps", "description": "Build apps with the app SDK.",
		"skills": []map[string]string{{"name": "interactive-apps", "description": "Build an interactive app.", "body": "Use the SDK."}}})
	e.publishDefault(map[string]any{"kind": "expert", "name": "app-builder", "displayName": "App builder", "description": "Builds interactive apps.",
		"instructions": "Build the app the person asks for."})
}

// The contract is held at save and at a platform install: an assist under
// a task's name that does not meet it is refused, so a replaced prompt
// never breaks the dialog that reads it; any other name is free.
func TestAssistContractHeldAtSaveAndInstall(t *testing.T) {
	e, _, _ := assistGW(t)
	broken := assistForm(t, wire.AssistTaskFindAWay, "Find a way.")
	delete(broken, "gate")
	if r := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": broken}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("find-a-way without a gate saved: %d %s", r.status, r.body)
	}
	text := assistForm(t, wire.AssistTaskDescribeWork, "Describe it.")
	text["output"] = wire.AssistOutputText
	delete(text, "fields")
	if r := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": text}, e.as("maya")); r.status != http.StatusBadRequest {
		t.Fatalf("a text describe-work saved: %d %s", r.status, r.body)
	}
	free := assistForm(t, wire.AssistTaskEnhancePrompt, "Rhyme it.")
	free["name"] = "rhyme"
	if r := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": free}, e.as("maya")); r.status != http.StatusCreated {
		t.Fatalf("an assist of another name: %d %s", r.status, r.body)
	}
	if id := e.publishDefault(assistForm(t, wire.AssistTaskReviewSkill, "Review the skill.")); id == "" {
		t.Fatal("no id")
	}
}
