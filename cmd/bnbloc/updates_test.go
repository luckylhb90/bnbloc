package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeUpdateGateway implements the super-admin endpoints the update publish
// uses, with switchable faults.
type fakeUpdateGateway struct {
	*httptest.Server
	mu        sync.Mutex
	cfg       wire.UpdateConfig
	hub       []wire.ReleaseEntry
	blobs     map[string][]byte
	calls     []string
	configGET int

	badUploadSHA   bool
	badUploadSize  bool
	corruptBlob    bool
	failHubPut     bool
	failConfigPut  bool
	flipChannelGET int // flip the channel on this config GET (1-based)
}

func newFakeUpdateGateway(t *testing.T) *fakeUpdateGateway {
	g := &fakeUpdateGateway{cfg: testUpdateConfig(), blobs: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/platform/update-config", func(w http.ResponseWriter, r *http.Request) {
		g.configGET++
		if g.configGET == g.flipChannelGET {
			g.cfg.Policy.Channel = "stable"
		}
		_ = json.NewEncoder(w).Encode(g.cfg)
	})
	mux.HandleFunc("PUT /v1/platform/update-config", func(w http.ResponseWriter, r *http.Request) {
		if g.failConfigPut {
			http.Error(w, "save update config failed", http.StatusInternalServerError)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&g.cfg)
		_ = json.NewEncoder(w).Encode(g.cfg)
	})
	mux.HandleFunc("POST /v1/platform/artifacts", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(raw)
		sha := hex.EncodeToString(sum[:])
		g.blobs["sha256/"+sha] = raw
		size := len(raw)
		if g.badUploadSHA {
			sha = strings.Repeat("0", 64)
		}
		if g.badUploadSize {
			size--
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"key": "sha256/" + sha, "sha256": sha, "size": size})
	})
	mux.HandleFunc("GET /v1/artifacts/{key...}", func(w http.ResponseWriter, r *http.Request) {
		blob, ok := g.blobs[r.PathValue("key")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if g.corruptBlob {
			blob = append([]byte("x"), blob...)
		}
		_, _ = w.Write(blob)
	})
	mux.HandleFunc("GET /v1/platform/app-releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(wire.ReleaseHub{Releases: g.hub})
	})
	mux.HandleFunc("PUT /v1/platform/app-releases/{version}", func(w http.ResponseWriter, r *http.Request) {
		if g.failHubPut {
			http.Error(w, "save release failed", http.StatusInternalServerError)
			return
		}
		var e wire.ReleaseEntry
		_ = json.NewDecoder(r.Body).Decode(&e)
		e.Version = r.PathValue("version")
		g.hub = append(g.hub, e)
		_ = json.NewEncoder(w).Encode(e)
	})
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer admin" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		g.calls = append(g.calls, r.Method+" "+r.URL.Path)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.Close)
	return g
}

func writeArchive(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bnbloc.app.tar.gz")
	if err := os.WriteFile(path, []byte("updater archive bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func publishInput(archive string) desktopPublishInput {
	return desktopPublishInput{
		ArchivePath: archive, Signature: tauriSig("bnbloc.app.tar.gz"), Version: "0.1.3", Notes: "fixes", Target: "darwin-aarch64",
		Now: func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) },
	}
}

func TestUpdatesPublishHappyPath(t *testing.T) {
	gw := newFakeUpdateGateway(t)
	var out bytes.Buffer
	c := newClient(config{Gateway: gw.URL, Token: "admin"})
	saved, err := publishDesktopUpdate(c, publishInput(writeArchive(t)), &out)
	if err != nil {
		t.Fatalf("publish: %v\n%s", err, out.String())
	}
	sum := sha256.Sum256([]byte("updater archive bytes"))
	sha := hex.EncodeToString(sum[:])
	wantURL := gw.URL + "/v1/artifacts/sha256/" + sha
	if saved.Policy.Channel != "internal" || !saved.Policy.Enabled || saved.Policy.AutoCheck {
		t.Fatalf("policy = %+v, want the internal channel kept, enabled on, autoCheck untouched", saved.Policy)
	}
	if saved.Release.Version != "0.1.3" || saved.Release.PubDate != "2026-10-08T12:00:00Z" || saved.Release.Platforms["darwin-aarch64"].URL != wantURL {
		t.Fatalf("release = %+v", saved.Release)
	}
	if saved.Release.Platforms["windows-x86_64"].URL != "" {
		t.Fatal("old-version windows artifact kept")
	}
	if len(gw.hub) != 1 || gw.hub[0].Channel != "internal" || gw.hub[0].Files[0].URL != wantURL || gw.hub[0].Files[0].SHA256 != sha {
		t.Fatalf("hub = %+v", gw.hub)
	}
	want := []string{
		"GET /v1/platform/update-config",
		"POST /v1/platform/artifacts",
		"GET /v1/artifacts/sha256/" + sha,
		"GET /v1/platform/update-config",
		"GET /v1/platform/app-releases",
		"PUT /v1/platform/app-releases/0.1.3",
		"PUT /v1/platform/update-config",
	}
	if strings.Join(gw.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(gw.calls, "\n"), strings.Join(want, "\n"))
	}
	for i := 1; i <= 6; i++ {
		if !strings.Contains(out.String(), "["+string(rune('0'+i))+"/6]") {
			t.Fatalf("stage %d not reported:\n%s", i, out.String())
		}
	}
}

// Every mismatch stops the publish before anything is written, and the error
// says which stage completed.
func TestUpdatesPublishFailures(t *testing.T) {
	cases := []struct {
		name     string
		fault    func(*fakeUpdateGateway)
		wantErr  string
		hubSaved bool
	}{
		{"upload sha mismatch", func(g *fakeUpdateGateway) { g.badUploadSHA = true }, "upload failed; nothing was published", false},
		{"upload size mismatch", func(g *fakeUpdateGateway) { g.badUploadSize = true }, "upload failed; nothing was published", false},
		{"download mismatch", func(g *fakeUpdateGateway) { g.corruptBlob = true }, "download verification failed; nothing was published", false},
		{"channel changed", func(g *fakeUpdateGateway) { g.flipChannelGET = 2 }, "now uses the stable channel", false},
		{"version on other channel", func(g *fakeUpdateGateway) { g.hub = []wire.ReleaseEntry{{Version: "0.1.3", Channel: "stable"}} }, "another channel", false},
		{"hub save fails", func(g *fakeUpdateGateway) { g.failHubPut = true }, "Release Hub save failed. Auto-update is not activated", false},
		{"config save fails", func(g *fakeUpdateGateway) { g.failConfigPut = true }, "Release Hub saved, but updater configuration failed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw := newFakeUpdateGateway(t)
			tc.fault(gw)
			before := gw.cfg
			_, err := publishDesktopUpdate(newClient(config{Gateway: gw.URL, Token: "admin"}), publishInput(writeArchive(t)), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if gw.cfg.Release.Version != before.Release.Version || gw.cfg.Policy.Enabled {
				t.Fatalf("updater config changed on failure: %+v", gw.cfg)
			}
			hubWritten := false
			for _, call := range gw.calls {
				hubWritten = hubWritten || strings.HasPrefix(call, "PUT /v1/platform/app-releases/")
			}
			if hubWritten != (tc.hubSaved || tc.name == "hub save fails") {
				t.Fatalf("hub PUT attempted = %v; calls %v", hubWritten, gw.calls)
			}
		})
	}
}

// Local checks run before any request.
func TestUpdatesPublishLocalChecks(t *testing.T) {
	gw := newFakeUpdateGateway(t)
	c := newClient(config{Gateway: gw.URL, Token: "admin"})
	archive := writeArchive(t)
	dmg := filepath.Join(t.TempDir(), "bnbloc.dmg")
	_ = os.WriteFile(dmg, []byte("x"), 0o644)
	for name, mutate := range map[string]func(*desktopPublishInput){
		"dmg":           func(in *desktopPublishInput) { in.ArchivePath = dmg },
		"bad signature": func(in *desktopPublishInput) { in.Signature = tauriSig("other.tar.gz") },
		"bad version":   func(in *desktopPublishInput) { in.Version = "1.2" },
		"windows":       func(in *desktopPublishInput) { in.Target = "windows-x86_64" },
	} {
		in := publishInput(archive)
		mutate(&in)
		if _, err := publishDesktopUpdate(c, in, io.Discard); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(gw.calls) != 0 {
		t.Fatalf("requests sent despite failed local checks: %v", gw.calls)
	}
}
