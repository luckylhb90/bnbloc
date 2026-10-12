package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func (s *Service) handleListComponentTrustedKeys(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
	keys, err := loadTrustedKeyRecords(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load trusted keys failed", err)
		httpx.Error(w, "load trusted keys failed", http.StatusInternalServerError)
		return
	}
	// signatureRequired tells the publishing UI whether this gateway
	// refuses unsigned packages, so it demands a signature exactly when
	// the install would (the runtime decision, not a client guess).
	httpx.WriteJSON(w, http.StatusOK, wire.ComponentKeysReply{Keys: keys, SignatureRequired: !s.AllowExtUnsigned})
}

func (s *Service) handleAddComponentTrustedKey(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	actor := claims.Subject
	var body struct {
		PublicKey string `json:"publicKey"`
		Label     string `json:"label"`
	}
	if !httpx.DecodeJSON(w, r, 16*1024, &body) {
		return
	}
	// Derive the id from the key material instead of trusting a supplied one,
	// so a listed id can never disagree with the key it names.
	keyID, _, err := pkgmeta.DecodeMinisignPublicKey(body.PublicKey)
	if err != nil {
		httpx.Error(w, "invalid publisher key: "+err.Error(), http.StatusBadRequest)
		return
	}
	existing, err := loadTrustedKeyRecords(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load trusted keys failed", err)
		httpx.Error(w, "load trusted keys failed", http.StatusInternalServerError)
		return
	}
	for _, k := range existing {
		if strings.EqualFold(k.KeyID, keyID) {
			httpx.Error(w, "publisher key "+keyID+" is already trusted", http.StatusConflict)
			return
		}
	}
	record := wire.ComponentKey{
		KeyID:     keyID,
		PublicKey: strings.TrimSpace(body.PublicKey),
		Label:     strings.TrimSpace(body.Label),
		AddedAtMs: time.Now().UnixMilli(),
		AddedBy:   actor,
	}
	if err := saveTrustedKeyRecords(r.Context(), s.PlatformSettings, append(existing, record), actor); err != nil {
		obs.ServerError(w, "save trusted keys failed", err)
		httpx.Error(w, "save trusted keys failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, record)
}

func (s *Service) handleDeleteComponentTrustedKey(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	keyID := strings.TrimSpace(r.PathValue("keyId"))
	existing, err := loadTrustedKeyRecords(r.Context(), s.PlatformSettings)
	if err != nil {
		obs.ServerError(w, "load trusted keys failed", err)
		httpx.Error(w, "load trusted keys failed", http.StatusInternalServerError)
		return
	}
	kept := make([]wire.ComponentKey, 0, len(existing))
	found := false
	for _, k := range existing {
		if strings.EqualFold(k.KeyID, keyID) {
			found = true
			continue
		}
		kept = append(kept, k)
	}
	if !found {
		httpx.Error(w, "publisher key not found", http.StatusNotFound)
		return
	}
	// Packages already installed stay installed: they were verified at install
	// time and revoking a key is about what may be installed next.
	if err := saveTrustedKeyRecords(r.Context(), s.PlatformSettings, kept, claims.Subject); err != nil {
		obs.ServerError(w, "save trusted keys failed", err)
		httpx.Error(w, "save trusted keys failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func loadTrustedKeyRecords(ctx context.Context, settings store.PlatformSettingsStore) ([]wire.ComponentKey, error) {
	if settings == nil {
		return nil, fmt.Errorf("platform settings store not configured")
	}
	setting, ok, err := settings.GetPlatformSetting(ctx, componentTrustedKeysKey)
	if err != nil || !ok || strings.TrimSpace(setting.Value) == "" {
		return []wire.ComponentKey{}, err
	}
	var keys []wire.ComponentKey
	if err := json.Unmarshal([]byte(setting.Value), &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func saveTrustedKeyRecords(ctx context.Context, settings store.PlatformSettingsStore, keys []wire.ComponentKey, actor string) error {
	if settings == nil {
		return fmt.Errorf("platform settings store not configured")
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	return settings.PutPlatformSetting(ctx, store.PlatformSetting{
		Key:         componentTrustedKeysKey,
		Value:       string(raw),
		UpdatedAtMs: time.Now().UnixMilli(),
		UpdatedBy:   actor,
	})
}
