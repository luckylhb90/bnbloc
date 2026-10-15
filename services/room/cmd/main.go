// Command bnbloc-room runs the room directory, membership, and invite service.
package main

import (
	"log"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/services/room"
)

func main() {
	cfg := config.FromEnv("BNBLOC_ROOM", ":8080")
	backends, err := store.NewBackends(config.FromEnvRaw("BNBLOC_MYSQL_DSN"))
	if err != nil {
		log.Fatalf("bnbloc-room: store: %v", err)
	}
	svc := room.New(backends.Room, backends.Channel)
	svc.AgentPrefs = backends.AgentPrefs
	svc.Artifacts = backends.Artifacts
	// Share links read the org policy switch and org roles from the tenant
	// registry (shared MySQL; shareagent writes it).
	svc.Tenants = backends.Tenant
	// Display names: the profile routes and the share resolve's "shared by".
	svc.Profiles = backends.Profiles
	// Artifact changes fan out through realtime's internal ingest so every
	// open preview refreshes live. Best-effort, same pattern as activity.
	svc.Publisher = realtime.NewHTTPPublisher(config.FromEnvRaw("BNBLOC_REALTIME_BASE_URL"), "room")
	// Handoff packages: export reads the feed + attachment blobs from realtime,
	// import writes history-only rows and re-uploads attachments there.
	rt := realtime.NewHandoffClient(config.FromEnvRaw("BNBLOC_REALTIME_BASE_URL"), "room")
	svc.Handoff = rt
	// Blob artifact versions and thumbnails live in realtime's blob store
	// (realtime is its single owner), reached through the same signed client.
	svc.Blobs = rt
	maxBytes, err := room.ArtifactMaxBytesFromEnv()
	if err != nil {
		log.Fatalf("bnbloc-room: %v", err)
	}
	svc.ArtifactMaxBytes = maxBytes
	svc.RoomImports = backends.RoomImports
	svc.Templates = backends.Templates
	svc.Schedules = backends.Schedules
	// A template's prompt packs: the content store under the built-in
	// overlay, as the gateway and realtime read it.
	svc.Content = contentbuiltin.Overlay(backends.ContentComponents)
	// Interactive apps' data is sealed under the deployment's secret store
	// key (appdata.Cipher); without it the app data routes answer 503.
	box, err := sealbox.FromEnv()
	if err != nil {
		log.Fatalf("bnbloc-room: %v", err)
	}
	svc.AppDocs = backends.AppDocs
	svc.RoomGroups = backends.RoomGroups
	// Exports and agent reads of app data are audited.
	svc.Audit = backends.ExtAudit
	// A member's apps name who produced each (the source job's run record).
	svc.Runs = backends.AgentRuns()
	if svc.AppCipher = appdata.NewCipher(box); svc.AppCipher == nil {
		log.Printf("bnbloc-room: %s is not set: interactive app data is off", sealbox.KeyEnv)
	}
	if err := obs.ServeWithHealth("bnbloc-room", cfg.ListenAddr, svcauth.Middleware(svcauth.KeyFromEnv(), svc.Handler()), backends.HealthCheck()); err != nil {
		log.Fatalf("bnbloc-room: %v", err)
	}
}
