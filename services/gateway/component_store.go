package gateway

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/servicecomp"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// A service component's sealed values (docs/architecture/service-components.md,
// the store primitive): GET|PUT|DELETE /v1/component-store/{scope}/{key},
// signed by the component over component.SignedRequest. A value is sealed
// with the platform key bound to its component, scope and key, so a row
// read by another component, or moved to another key, does not open.
// GET on a scope with no key lists its keys.

// componentValueScope parses the path after the store prefix into a scope
// ("members/<subject>", "orgs/<org>", "shared") and a key ("" = the scope's
// listing). ok is false for any other shape.
func componentValueScope(rest string) (scope, key string, ok bool) {
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 2 && parts[0] == component.StoreShared:
		scope, key = component.StoreShared, parts[1]
	case len(parts) == 3 && (parts[0] == component.StoreMembers || parts[0] == component.StoreOrgs) && parts[1] != "":
		scope, key = parts[0]+"/"+parts[1], parts[2]
	default:
		return "", "", false
	}
	return scope, key, key == "" || component.ValidStoreKey(key)
}

// componentValueAD binds a sealed value to where it is stored.
func componentValueAD(comp, scope, key string) string {
	return "component.value/" + comp + "/" + scope + "/" + key
}

func (s *Service) handleComponentStore(w http.ResponseWriter, r *http.Request) {
	if s.ComponentValues == nil || s.SecretBox == nil {
		httpx.Unavailable(w, "component store")
		return
	}
	kind, name := componentCaller(r)
	if kind != extreg.KindService {
		httpx.Error(w, component.ComponentHeader+" must name a service (service/<name>)", http.StatusUnauthorized)
		return
	}
	body, ok := readServiceBody(w, r)
	if !ok {
		return
	}
	pathQuery := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		pathQuery += "?" + r.URL.RawQuery
	}
	rec, ok := s.verifyComponentCall(w, r, kind, name, component.SignedRequest(r.Method, pathQuery, "", body))
	if !ok {
		return
	}
	if !servicecomp.Declares(rec, component.ServiceStore) {
		httpx.Error(w, "the service does not declare store", http.StatusForbidden)
		return
	}
	scope, key, ok := componentValueScope(strings.TrimPrefix(r.URL.Path, component.ComponentStorePath))
	if !ok {
		httpx.Error(w, "invalid store path: "+component.ComponentStorePath+"{shared|members/<subject>|orgs/<org>}/{key}", http.StatusBadRequest)
		return
	}
	comp := rec.Kind + "/" + rec.Name
	ctx := r.Context()
	switch {
	case r.Method == http.MethodGet && key == "":
		keys, err := s.ComponentValues.ListComponentValueKeys(ctx, comp, scope)
		if err != nil {
			obs.ServerError(w, "list component values failed", err)
			httpx.Error(w, "list component values failed", http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string][]string{"keys": keys})
	case key == "":
		httpx.Error(w, "a key is required", http.StatusBadRequest)
	case r.Method == http.MethodGet:
		v, found, err := s.ComponentValues.GetComponentValue(ctx, comp, scope, key)
		if err != nil {
			obs.ServerError(w, "read component value failed", err)
			httpx.Error(w, "read component value failed", http.StatusInternalServerError)
			return
		}
		if !found {
			httpx.Error(w, "no value", http.StatusNotFound)
			return
		}
		plain, err := s.SecretBox.Open(v.Sealed, componentValueAD(comp, scope, key))
		if err != nil {
			obs.ServerError(w, "open component value failed", err)
			httpx.Error(w, "the value cannot be opened with this deployment's key", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(plain)
	case r.Method == http.MethodPut:
		if len(body) > component.MaxStoreValue {
			httpx.Error(w, "a value is at most 64 KiB", http.StatusRequestEntityTooLarge)
			return
		}
		sealed, err := s.SecretBox.Seal(body, componentValueAD(comp, scope, key))
		if err != nil {
			obs.ServerError(w, "seal component value failed", err)
			httpx.Error(w, "seal component value failed", http.StatusInternalServerError)
			return
		}
		if err := s.ComponentValues.PutComponentValue(ctx, store.ComponentValue{Component: comp, Scope: scope, Key: key, Sealed: sealed, UpdatedAtMs: s.nowTime().UnixMilli()}); err != nil {
			obs.ServerError(w, "store component value failed", err)
			httpx.Error(w, "store component value failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete:
		if err := s.ComponentValues.DeleteComponentValue(ctx, comp, scope, key); err != nil {
			obs.ServerError(w, "delete component value failed", err)
			httpx.Error(w, "delete component value failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
