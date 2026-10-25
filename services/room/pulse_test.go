package room

import (
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/pulse/pulsetest"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func TestPulseArtifactVersionIsNewOnlyForTheFirstVersion(t *testing.T) {
	e, rec := pulsetest.New(t)
	s := &Service{Pulse: e}
	s.pulseArtifactVersion(store.Artifact{ArtifactID: "a1", TenantID: "t1", Kind: "deck", CurrentVersion: 1}, "")
	s.pulseArtifactVersion(store.Artifact{ArtifactID: "a1", TenantID: "t1", Kind: "deck", CurrentVersion: 2}, "")
	evs := rec.Wait(t, 2)
	if evs[0]["id"] != "artifact:a1:v1" || pulsetest.Attr(evs[0], "isNew") != true || pulsetest.Attr(evs[0], "kind") != "deck" {
		t.Fatalf("first version = %v", evs[0])
	}
	if pulsetest.Attr(evs[1], "isNew") != false || evs[1]["tenant"] != "t1" {
		t.Fatalf("second version = %v", evs[1])
	}
}

// An app's collection and document id are its own text: the event carries a
// digest of them, never the strings, and public reflects the document.
func TestPulseAppWrittenNeverCarriesTheDocumentsText(t *testing.T) {
	e, rec := pulsetest.New(t)
	s := &Service{Pulse: e}
	a := &appCall{tenantID: "t1"}
	s.pulseAppWritten(a, store.AppDoc{AppID: "app1", Collection: "secret-notes", DocID: "my private id", Version: 3, Public: true})
	ev := rec.Wait(t, 1)[0]
	if ev["kind"] != "app.written" || pulsetest.Attr(ev, "public") != true {
		t.Fatalf("event = %v", ev)
	}
	raw := ev["id"].(string)
	if strings.Contains(raw, "secret") || strings.Contains(raw, "private") {
		t.Fatalf("app text leaked into the event id: %q", raw)
	}
	if !strings.HasSuffix(raw, ":v3") {
		t.Fatalf("id %q must carry the version", raw)
	}
}
