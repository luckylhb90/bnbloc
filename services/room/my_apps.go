package room

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A member's apps across rooms (docs/prd/2026-10-13-app-gallery.md §5,
// Library → "My apps"): GET /v1/my/apps[?cursor=&limit=] (wire.MyAppList).
// The rooms are the caller's in the request's tenant (the gateway signs the
// effective tenant) where the caller is one of an app's owners: a human
// member who may chat (membership.Member.MayChat, the app data owner rule
// appOwner applies to members). Which of their deliverables are apps is
// the store's one rule (store.Artifact.IsApp, ListApps). Each app carries
// its room's name, its latest version's producer (roomlog.ProducerOf over
// the run store), its document count (only for an app whose data space
// exists) and its working share links.

// handleMyApps lists the caller's apps (above), newest-updated first.
func (s *Service) handleMyApps(w http.ResponseWriter, r *http.Request) {
	user := caller(r)
	if user == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	q := store.AppListQuery{}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > store.MaxAppListPage {
			httpx.Error(w, "limit must be 1.."+strconv.Itoa(store.MaxAppListPage), http.StatusBadRequest)
			return
		}
		q.Limit = n
	}
	before, id, ok := parseArtifactCursor(w, r.URL.Query().Get("cursor"))
	if !ok {
		return
	}
	q.BeforeUpdatedMs, q.BeforeID = before, id
	ctx := r.Context()
	rooms, err := s.Store.RoomsForUserDetailed(ctx, user)
	if err != nil {
		obs.ServerError(w, "list rooms failed", err)
		httpx.Error(w, "list rooms failed", http.StatusInternalServerError)
		return
	}
	names := map[string]string{}
	for _, room := range rooms {
		if (membership.Member{Role: room.Role}).MayChat() {
			names[room.RoomID] = room.Name
			q.RoomIDs = append(q.RoomIDs, room.RoomID)
		}
	}
	apps, err := s.Artifacts.ListApps(ctx, q)
	if err != nil {
		obs.ServerError(w, "list apps failed", err)
		httpx.Error(w, "list apps failed", http.StatusInternalServerError)
		return
	}
	out := wire.MyAppList{Apps: make([]wire.MyApp, 0, len(apps))}
	for _, a := range apps {
		app, err := s.myApp(ctx, a, names[a.RoomID])
		if err != nil {
			obs.ServerError(w, "read app failed", err)
			httpx.Error(w, "read app failed", http.StatusInternalServerError)
			return
		}
		out.Apps = append(out.Apps, app)
	}
	if len(apps) == q.PageSize() {
		out.NextCursor = artifactCursor(apps[len(apps)-1])
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// myApp is one app of the listing: its latest version's producer, its
// document count and its working share links.
func (s *Service) myApp(ctx context.Context, a store.Artifact, roomName string) (wire.MyApp, error) {
	app := wire.MyApp{
		AppID: a.ArtifactID, Name: a.Title, RoomID: a.RoomID, RoomName: roomName,
		Version: a.CurrentVersion, UpdatedAtMs: a.UpdatedAtMs, Links: []wire.AppLink{},
	}
	if err := s.appProducer(ctx, a, &app); err != nil {
		return app, err
	}
	if a.HasAppData && s.AppDocs != nil {
		counts, err := s.AppDocs.CountAppDocs(ctx, store.AppScope{AppID: a.ArtifactID, RoomID: a.RoomID})
		if err != nil {
			return app, err
		}
		app.Documents = counts.Docs
	}
	now := s.Now()
	shares, err := s.Artifacts.ListArtifactShares(ctx, a.RoomID, a.ArtifactID, now)
	if err != nil {
		return app, err
	}
	for _, sh := range shares {
		if sh.Active(now) {
			app.Links = append(app.Links, wire.AppLink{ShareID: sh.ShareID, Access: sh.Access, ExpiresAtMs: sh.ExpiresAtMs, Version: sh.Version})
		}
	}
	return app, nil
}

// appProducer sets app's producer: its latest version's (roomlog.ProducerOf
// over the run store). An app without a version yet has none.
func (s *Service) appProducer(ctx context.Context, a store.Artifact, app *wire.MyApp) error {
	if a.CurrentVersion <= 0 {
		return nil
	}
	v, err := s.Artifacts.GetArtifactVersion(ctx, a.ArtifactID, a.CurrentVersion)
	if err != nil {
		return err
	}
	// A run record that is gone (or names no agent) leaves the producer
	// unknown (empty); a failed read fails the listing.
	var readErr error
	p, _ := roomlog.ProducerOf(ctx, v.View(), func(ctx context.Context, job string) (wire.AgentRunAgent, error) {
		if s.Runs == nil {
			return wire.AgentRunAgent{}, store.ErrAgentJobNotFound
		}
		j, err := s.Runs.GetAgentRun(ctx, job)
		if err == nil && j.RoomID != a.RoomID {
			err = store.ErrAgentJobNotFound
		}
		if err != nil && !errors.Is(err, store.ErrAgentJobNotFound) {
			readErr = err
		}
		return wire.AgentRunAgent{Owner: j.OwnerUserID, Core: j.Core, Provider: j.Provider}, err
	})
	if readErr != nil {
		return readErr
	}
	app.Producer = wire.AppProducer{Owner: p.Owner, Provider: p.Provider, Core: p.Core, Person: p.Person}
	return nil
}
