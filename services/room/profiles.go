package room

import (
	"context"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Display names (docs/prd/2026-10-10-display-names-and-guided-onboarding.md
// §1). The room service owns them: it is the user-facing directory already
// (members, /v1/users/{id}/rooms), and its share resolve names the sharer to
// the public page, so that read stays a local store lookup.

// maxProfileLookupIDs caps one GET /v1/users/profiles batch.
const maxProfileLookupIDs = 200

func (s *Service) registerProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/my/profile", s.handleGetMyProfile)
	mux.HandleFunc("PUT /v1/my/profile", s.handlePutMyProfile)
	mux.HandleFunc("GET /v1/users/profiles", s.handleUserProfiles)
}

// profilesReady answers 503 when no profile store is configured.
func (s *Service) profilesReady(w http.ResponseWriter) bool {
	if s.Profiles == nil {
		obs.ServerError(w, "user profiles not configured", nil)
		writeErr(w, http.StatusServiceUnavailable, "user profiles not configured")
		return false
	}
	return true
}

// displayNames resolves ids to their display names (default applied), in
// order. It is the one lookup behind every surface naming a person.
func (s *Service) displayNames(ctx context.Context, ids []string) ([]wire.UserProfile, error) {
	stored, err := s.Profiles.DisplayNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]wire.UserProfile, 0, len(ids))
	for _, id := range ids {
		out = append(out, wire.UserProfile{UserID: id, DisplayName: store.DisplayName(id, stored[id])})
	}
	return out, nil
}

// myProfile reads userID's own profile.
func (s *Service) myProfile(ctx context.Context, userID string) (wire.MyProfile, error) {
	stored, err := s.Profiles.DisplayNames(ctx, []string{userID})
	if err != nil {
		return wire.MyProfile{}, err
	}
	name := stored[userID]
	return wire.MyProfile{UserID: userID, DisplayName: store.DisplayName(userID, name), IsDefault: name == ""}, nil
}

// handleGetMyProfile is GET /v1/my/profile → wire.MyProfile.
func (s *Service) handleGetMyProfile(w http.ResponseWriter, r *http.Request) {
	user := caller(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	if !s.profilesReady(w) {
		return
	}
	p, err := s.myProfile(r.Context(), user)
	if err != nil {
		obs.ServerError(w, "read profile failed", err)
		writeErr(w, http.StatusInternalServerError, "read profile failed")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handlePutMyProfile is PUT /v1/my/profile {displayName} → wire.MyProfile.
// The name is trimmed and validated (store.NormalizeDisplayName); an empty
// one clears it, restoring the default.
func (s *Service) handlePutMyProfile(w http.ResponseWriter, r *http.Request) {
	user := caller(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	if !s.profilesReady(w) {
		return
	}
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if !decode(w, r, &body) {
		return
	}
	name, err := store.NormalizeDisplayName(body.DisplayName)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Profiles.SetDisplayName(r.Context(), user, name, s.Now()); err != nil {
		obs.ServerError(w, "save profile failed", err)
		writeErr(w, http.StatusInternalServerError, "save profile failed")
		return
	}
	writeJSON(w, http.StatusOK, wire.MyProfile{UserID: user, DisplayName: store.DisplayName(user, name), IsDefault: name == ""})
}

// handleUserProfiles is GET /v1/users/profiles?ids=a,b,… → {profiles:
// [wire.UserProfile]} for any signed-in caller: every distinct id once, in
// request order, the default applied. It answers for any id (known or not)
// and never with more than the display name, so it confirms nothing about
// who exists. At most maxProfileLookupIDs distinct ids per call.
func (s *Service) handleUserProfiles(w http.ResponseWriter, r *http.Request) {
	if caller(r) == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	if !s.profilesReady(w) {
		return
	}
	ids := profileLookupIDs(r.URL.Query()["ids"])
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "ids is required")
		return
	}
	if len(ids) > maxProfileLookupIDs {
		writeErr(w, http.StatusBadRequest, "at most 200 ids per request")
		return
	}
	profiles, err := s.displayNames(r.Context(), ids)
	if err != nil {
		obs.ServerError(w, "read profiles failed", err)
		writeErr(w, http.StatusInternalServerError, "read profiles failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

// profileLookupIDs splits the comma-separated ids values into trimmed,
// non-empty ids, each distinct id (case-insensitively) once, in order. It
// stops one past maxProfileLookupIDs: that many already refuses the call.
func profileLookupIDs(values []string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, v := range values {
		for _, id := range strings.Split(v, ",") {
			id = strings.TrimSpace(id)
			key := strings.ToLower(id)
			if id == "" || seen[key] {
				continue
			}
			seen[key] = true
			if ids = append(ids, id); len(ids) > maxProfileLookupIDs {
				return ids
			}
		}
	}
	return ids
}
