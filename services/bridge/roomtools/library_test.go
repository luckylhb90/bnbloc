package roomtools

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// list_components reads the whole Library of the job's requester (content
// and MCP tools) through the member catalog with the job's grant.
func TestListComponentsReadsTheRequestersLibrary(t *testing.T) {
	gw := &recordingGateway{reply: func(string, string) ([]byte, error) {
		return []byte(`{"components":[{"kind":"mcp","name":"search","version":"2.0.0","rollout":"auto","installed":true,"managed":true,"publishedAtMs":5}],
			"contentComponents":[{"id":"cc_1","kind":"prompt","name":"team-prompts","scope":"org","latestVersion":"3.0.0","updatedAtMs":9}]}`), nil
	}}
	res := listComponents(context.Background(), contentEnv(nil, gw, true), json.RawMessage(`{}`))
	if res.Err != "" {
		t.Fatalf("list = %s", res.Err)
	}
	items := res.Value.(map[string]any)["components"].([]roomlog.LibraryItem)
	if len(items) != 2 || items[0].Name != "team-prompts" || items[1].Kind != roomlog.LibraryKindMCP || items[1].Scope != "platform" {
		t.Fatalf("items = %+v", items)
	}
	u, _ := url.Parse(gw.calls[0].Path)
	if u.Path != "/v1/components" || u.Query().Get("job") != "job-1" || u.Query().Get("room") != "room-1" {
		t.Fatalf("call = %s", gw.calls[0].Path)
	}
	if res := listComponents(context.Background(), contentEnv(nil, gw, false), json.RawMessage(`{}`)); res.Err == "" {
		t.Fatal("an unbound session listed a Library")
	}
}
