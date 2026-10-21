package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/media"
	"github.com/luckylhb90/bnbloc/internal/ratelimit"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// videoCall posts or polls a video route and decodes the reply.
func (e *appGW) videoCall(method, path string, body any, header string) (int, wire.MediaVideoReply, string) {
	e.t.Helper()
	r := e.call(method, path, body, header)
	var reply wire.MediaVideoReply
	_ = json.Unmarshal(r.body, &reply)
	return r.status, reply, string(r.body)
}

// countPaths counts the provider requests whose path has suffix.
func (f *mediaProviderFake) countPaths(suffix string) int {
	n := 0
	for _, p := range f.paths {
		if strings.HasSuffix(p, suffix) {
			n++
		}
	}
	return n
}

// A member's video: the submit answers at once (rendering, the provider's
// id) with the length as a string and the aspect's size, audited without
// the prompt; polls answer rendering, then done (no file: no job), and a
// failed video its reason, audited; an unknown id is the provider's 404;
// the bounds refuse before any spend.
func TestMediaVideo(t *testing.T) {
	fake := &mediaProviderFake{videoPolls: 1}
	e := mediaGW(t, fake)
	sink := e.gw.ExtAudit.(*recordedAudit)
	code, reply, raw := e.videoCall(http.MethodPost, "/v1/media/video", wire.MediaVideoRequest{Prompt: "a paper boat at sunrise", Seconds: 6, Aspect: "9:16"}, e.as("maya"))
	if code != http.StatusOK || reply.ID != "video_abc" || reply.Status != wire.MediaVideoRendering || reply.Model != "media-model" {
		t.Fatalf("submit = %d %s", code, raw)
	}
	if fake.last["seconds"] != "6" || fake.last["size"] != "720x1280" || fake.last["prompt"] != "a paper boat at sunrise" {
		t.Fatalf("provider got %v", fake.last)
	}
	rows := sink.take()
	if len(rows) != 1 || rows[0].Name != "video" || rows[0].Kind != wire.AuditKindAssist || strings.Contains(rows[0].Detail, "paper boat") {
		t.Fatalf("submit audit = %+v", rows)
	}

	if code, reply, raw = e.videoCall(http.MethodGet, "/v1/media/video/video_abc", nil, e.as("maya")); code != http.StatusOK || reply.Status != wire.MediaVideoRendering {
		t.Fatalf("first poll = %d %s", code, raw)
	}
	// A member's own call publishes nothing, even asked to: done, no file.
	if code, reply, raw = e.videoCall(http.MethodGet, "/v1/media/video/video_abc?publish=true", nil, e.as("maya")); code != http.StatusOK || reply.Status != wire.MediaVideoDone || reply.ArtifactID != "" {
		t.Fatalf("done poll = %d %s", code, raw)
	}
	if fake.countPaths("/content") != 0 || len(sink.take()) != 0 {
		t.Fatal("a done video without a job was read or audited")
	}

	fake.videoFinal = "failed"
	if code, reply, raw = e.videoCall(http.MethodGet, "/v1/media/video/video_abc", nil, e.as("maya")); code != http.StatusOK || reply.Status != wire.MediaVideoFailed || reply.Error != "the prompt was refused" {
		t.Fatalf("failed poll = %d %s", code, raw)
	}
	if rows := sink.take(); len(rows) != 1 || !strings.Contains(rows[0].Detail, "failed: the provider failed the video") || strings.Contains(rows[0].Detail, "refused") {
		t.Fatalf("failed audit = %+v", rows)
	}
	if code, _, raw = e.videoCall(http.MethodGet, "/v1/media/video/video_gone", nil, e.as("maya")); code != http.StatusNotFound || !strings.Contains(raw, "no such video") {
		t.Fatalf("unknown id = %d %s", code, raw)
	}
	sink.take()

	calls := len(fake.paths)
	for name, req := range map[string]wire.MediaVideoRequest{
		"no prompt":       {Prompt: "  "},
		"a long prompt":   {Prompt: strings.Repeat("a", wire.MaxMediaPrompt+1)},
		"another aspect":  {Prompt: "x", Aspect: "4:3"},
		"negative length": {Prompt: "x", Seconds: -1},
	} {
		if code, _, raw := e.videoCall(http.MethodPost, "/v1/media/video", req, e.as("maya")); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, code, raw)
		}
	}
	if code, _, raw := e.videoCall(http.MethodGet, "/v1/media/video/not$an$id", nil, e.as("maya")); code != http.StatusBadRequest {
		t.Errorf("a bad id = %d %s", code, raw)
	}
	if len(fake.paths) != calls {
		t.Fatal("the provider was called for a refused request")
	}

	// The provider test reads the model list; it never films.
	r := e.call(http.MethodPost, "/v1/platform/media/video/test", nil, e.superAdmin())
	var test wire.MediaProviderTest
	if json.Unmarshal(r.body, &test) != nil || !test.OK || !strings.Contains(test.Reply, "media-model is among") {
		t.Fatalf("test = %d %s", r.status, r.body)
	}
	if fake.paths[len(fake.paths)-1] != "/v1/models" {
		t.Fatalf("the test called %s", fake.paths[len(fake.paths)-1])
	}

	// AI off, or no provider: both routes refuse as an image does.
	off := true
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{AssistDisabled: &off}); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := e.videoCall(http.MethodPost, "/v1/media/video", wire.MediaVideoRequest{Prompt: "x"}, e.as("maya")); code != http.StatusConflict {
		t.Fatalf("submit with AI off = %d %s", code, raw)
	}
	if code, _, raw := e.videoCall(http.MethodGet, "/v1/media/video/video_abc", nil, e.as("maya")); code != http.StatusConflict {
		t.Fatalf("poll with AI off = %d %s", code, raw)
	}
}

// The submit spends the cost budget and the member's assist rate; a status
// poll spends neither (a video is polled for minutes), and is still a
// drive-permission route.
func TestMediaVideoSpendsOnSubmitOnly(t *testing.T) {
	fake := &mediaProviderFake{}
	e := mediaGW(t, fake)
	budgets := map[string]rateBudget{}
	for _, rt := range e.gw.routes() {
		if strings.Contains(rt.pattern, "/v1/media/video") {
			budgets[rt.pattern] = rt.budget
			if rt.perm != e.gw.routeFor(t, "POST /v1/media/image").perm {
				t.Errorf("%s is not admitted like an image", rt.pattern)
			}
		}
	}
	if budgets["POST /v1/media/video"] != costBudget || budgets["GET /v1/media/video/{id}"] != noBudget || len(budgets) != 2 {
		t.Fatalf("budgets = %v", budgets)
	}
	e.gw.assistLimiter = ratelimit.Lazy{}
	e.gw.assistLimiter.Get(assistRate, 1, nil).Now = func() time.Time { return time.Unix(0, 0) }
	if code, _, raw := e.videoCall(http.MethodPost, "/v1/media/video", wire.MediaVideoRequest{Prompt: "x"}, e.as("maya")); code != http.StatusOK {
		t.Fatalf("first submit = %d %s", code, raw)
	}
	if code, _, _ := e.videoCall(http.MethodPost, "/v1/media/video", wire.MediaVideoRequest{Prompt: "x"}, e.as("maya")); code != http.StatusTooManyRequests {
		t.Fatalf("a submit past the rate = %d", code)
	}
	for i := 0; i < 3; i++ {
		if code, _, raw := e.videoCall(http.MethodGet, "/v1/media/video/video_abc", nil, e.as("maya")); code != http.StatusOK {
			t.Fatalf("poll %d with the rate spent = %d %s", i, code, raw)
		}
	}
}

// routeFor is the gateway's route of pattern.
func (s *Service) routeFor(t *testing.T, pattern string) route {
	t.Helper()
	for _, rt := range s.routes() {
		if rt.pattern == pattern {
			return rt
		}
	}
	t.Fatalf("no route %s", pattern)
	return route{}
}

// An agent's video (the room relay's ?job=&room=): done, it is published
// once into the job's room as the requester, under the id the room and the
// video derive, sourced to the job, named as asked with .mp4; a repeated
// poll answers the same file without reading the provider again; a video
// over the room's file cap is refused, said plainly; another bridge's job
// is refused.
func TestMediaVideoPublishUnderJobGrant(t *testing.T) {
	fake := &mediaProviderFake{videoPolls: 1}
	e := mediaGW(t, fake)
	sink := e.gw.ExtAudit.(*recordedAudit)
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: "job-1", RoomID: e.roomID, ChannelID: "ch-1", MessageSeq: 4, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	bridge := "Authorization: Bearer " + e.session("jack")
	grant := "room=" + e.roomID + "&job=job-1"
	if code, reply, raw := e.videoCall(http.MethodPost, "/v1/media/video?"+grant, wire.MediaVideoRequest{Prompt: "a boat"}, bridge); code != http.StatusOK || reply.ID != "video_abc" {
		t.Fatalf("submit = %d %s", code, raw)
	}
	poll := "/v1/media/video/video_abc?publish=true&fileName=" + "boat.mov&" + grant
	if code, reply, raw := e.videoCall(http.MethodGet, poll, nil, bridge); code != http.StatusOK || reply.Status != wire.MediaVideoRendering || reply.ArtifactID != "" {
		t.Fatalf("rendering = %d %s", code, raw)
	}
	sink.take()
	code, done, raw := e.videoCall(http.MethodGet, poll, nil, bridge)
	if code != http.StatusOK || done.Status != wire.MediaVideoDone || done.ArtifactID != videoArtifactID(e.roomID, "video_abc") || done.FileName != "boat.mp4" || done.Bytes != 112 {
		t.Fatalf("done = %d %s", code, raw)
	}
	art, err := e.b.Artifacts.GetArtifact(e.ctx(), done.ArtifactID)
	if err != nil || art.RoomID != e.roomID || art.ChannelID != "ch-1" || art.CreatedBy != "maya" || art.CurrentVersion != 1 || art.Kind != store.ArtifactKindFile {
		t.Fatalf("artifact = %+v %v", art, err)
	}
	v, err := e.b.Artifacts.GetArtifactVersion(e.ctx(), done.ArtifactID, 1)
	if err != nil || v.SourceJobID != "job-1" || v.SourceSeq != 4 || v.MimeType != "video/mp4" || v.SizeBytes != 112 {
		t.Fatalf("version = %+v %v", v, err)
	}
	if rows := sink.take(); len(rows) != 1 || !strings.Contains(rows[0].Detail, "(published)") || rows[0].Actor != "maya" {
		t.Fatalf("publish audit = %+v", rows)
	}

	// Polled again: the same file, the provider not asked, nothing audited.
	calls := len(fake.paths)
	code, again, raw := e.videoCall(http.MethodGet, poll, nil, bridge)
	want := done
	want.Model = "" // the room file is the answer; the provider is not asked
	if code != http.StatusOK || again != want {
		t.Fatalf("again = %d %s; want %+v", code, raw, want)
	}
	if len(fake.paths) != calls || len(sink.take()) != 0 {
		t.Fatal("a published video was read or audited again")
	}
	if art, _ := e.b.Artifacts.GetArtifact(e.ctx(), done.ArtifactID); art.CurrentVersion != 1 {
		t.Fatalf("versions = %d", art.CurrentVersion)
	}

	// Two polls past the check at once: the second publish answers the
	// first's file, one version.
	maya := auth.Claims{Subject: "maya", Tenant: e.tenant}
	p, _, err := e.gw.mediaProvider(e.ctx(), maya, media.CapVideo, "")
	if err != nil {
		t.Fatal(err)
	}
	place := *jobMediaPlace(&store.AgentJob{JobID: "job-1", RoomID: e.roomID, ChannelID: "ch-1", MessageSeq: 4})
	first, err1 := e.gw.publishVideo(e.ctx(), maya, p, place, "video_race", "race")
	second, err2 := e.gw.publishVideo(e.ctx(), maya, p, place, "video_race", "race")
	if err1 != nil || err2 != nil || first.ArtifactID != second.ArtifactID || second.FileName != "race.mp4" {
		t.Fatalf("racing publishes = %+v %v ; %+v %v", first, err1, second, err2)
	}
	if art, _ := e.b.Artifacts.GetArtifact(e.ctx(), first.ArtifactID); art.CurrentVersion != 1 {
		t.Fatalf("racing versions = %d", art.CurrentVersion)
	}
	sink.take()

	// Over the room's file cap: refused with the sizes, nothing published.
	e.room.ArtifactMaxBytes = 64
	code, _, raw = e.videoCall(http.MethodGet, "/v1/media/video/video_big?publish=true&"+grant, nil, bridge)
	e.room.ArtifactMaxBytes = 0
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(raw, "the room's file cap") {
		t.Fatalf("over the cap = %d %s", code, raw)
	}
	if _, err := e.b.Artifacts.GetArtifact(e.ctx(), videoArtifactID(e.roomID, "video_big")); err == nil {
		t.Fatal("a video over the cap was published")
	}

	// The job grant is the bridge's: the requester's own session, or a job
	// of another room, is refused.
	if code, _, raw := e.videoCall(http.MethodGet, poll, nil, e.as("maya")); code != http.StatusForbidden {
		t.Fatalf("not the job's bridge = %d %s", code, raw)
	}
	if code, _, raw := e.videoCall(http.MethodGet, "/v1/media/video/video_abc?publish=true&room="+e.roomID+"&job=job-elsewhere", nil, bridge); code != http.StatusNotFound {
		t.Fatalf("another job = %d %s", code, raw)
	}
}

// The file name: the asked one's base, never a path or control characters,
// always an .mp4.
func TestVideoFileName(t *testing.T) {
	for asked, want := range map[string]string{
		"":                 "video.mp4",
		"boat":             "boat.mp4",
		"boat.mov":         "boat.mp4",
		"../../etc/passwd": "passwd.mp4",
		`dir\clip.mp4`:     "clip.mp4",
		"a\x00b.mp4":       "ab.mp4",
		".mp4":             "video.mp4",
	} {
		if got := videoFileName(asked); got != want {
			t.Errorf("videoFileName(%q) = %q; want %q", asked, got, want)
		}
	}
	if a, b := videoArtifactID("room-1", "video_abc"), videoArtifactID("room-2", "video_abc"); a == b || len(a) > 64 || !strings.HasPrefix(a, "artv_") {
		t.Fatalf("ids %s %s", a, b)
	}
}
