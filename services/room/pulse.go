package room

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Pulse hooks (docs/prd/2026-10-24-executive-pulse-backend.md, section 4). Each
// runs after the store has committed the write and only hands an event to the
// emitter; a nil emitter makes it a no-op.

// pulseArtifactVersion: a new artifact version (or a new artifact, which is
// version 1). Thumbnail refreshes are not versions and are not reported here.
func (s *Service) pulseArtifactVersion(art store.Artifact, actor string) {
	ev := pulse.Event{
		ID:     "artifact:" + art.ArtifactID + ":v" + strconv.Itoa(int(art.CurrentVersion)),
		Tenant: art.TenantID,
		Kind:   pulse.KindArtifactVersion,
	}.With(pulse.Ident("kind", art.Kind)).With(pulse.Flag("isNew", art.CurrentVersion <= 1))
	if m, err := pulse.MemberHash(s.PulseKey, actor); err == nil && actor != "" {
		ev = ev.With(pulse.Hash("member", m))
	}
	s.Pulse.Emit(ev)
}

// pulseAppWritten: a document was created or updated in an interactive app.
// The app's collection and document id are chosen by the app and may hold any
// text, so the event id carries a digest of them, never the strings themselves.
func (s *Service) pulseAppWritten(a *appCall, d store.AppDoc) {
	sum := sha256.Sum256([]byte(d.AppID + "\x00" + d.Collection + "\x00" + d.DocID))
	ev := pulse.Event{
		ID:     "app:" + hex.EncodeToString(sum[:16]) + ":v" + strconv.FormatInt(d.Version, 10),
		Tenant: a.tenantID,
		Kind:   pulse.KindAppWritten,
	}.With(pulse.Flag("public", d.Public))
	// Only a member's own write names the member; an agent's write names none.
	if a.subject != "" {
		if m, err := pulse.MemberHash(s.PulseKey, a.subject); err == nil {
			ev = ev.With(pulse.Hash("member", m))
		}
	}
	s.Pulse.Emit(ev)
}
