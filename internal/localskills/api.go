package localskills

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
)

// maxSettingsBody bounds a settings update.
const maxSettingsBody = 256 << 10

// Handler serves the skills provider API (ProviderContract) on loopback:
//
//	GET  /v1/state                what the component has (State)
//	PUT  /v1/settings             replaces the settings (Settings), answers State
//	POST /v1/pack?name=<skill>    one skill as a tar, its hash in X-Skill-Hash
//
// token, when set, is the bearer every call must carry (the desktop hands it to
// the component at launch and to nothing else).
func (s *Service) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.State()
		if err != nil {
			obs.ServerError(w, "read the skills", err)
			httpx.Error(w, "could not read the skills: "+err.Error(), http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("PUT /v1/settings", func(w http.ResponseWriter, r *http.Request) {
		var in Settings
		if !httpx.DecodeJSON(w, r, maxSettingsBody, &in) {
			return
		}
		st, err := s.Apply(in)
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /v1/pack", func(w http.ResponseWriter, r *http.Request) {
		archive, hash, err := s.Pack(r.URL.Query().Get("name"))
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Skill-Hash", hash)
		_, _ = w.Write(archive)
	})
	return requireBearer(token, mux)
}

func requireBearer(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			httpx.Error(w, errors.New("unauthorized").Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
