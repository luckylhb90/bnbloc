package extpipe

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// VerifyPackage is the pipeline's first step for a package kind the
// pipeline does not install itself (a content component's platform
// package, installed into the content tables): the signature checked
// against the trusted keys (or an unsigned package admitted only when
// AllowUnsigned), then the package parsed and validated. It returns the
// signing key id ("" when unsigned).
func (p *Pipeline) VerifyPackage(ctx context.Context, body, signature []byte) (pkgmeta.Package, string, error) {
	pkg, keyID, _, err := p.verify(ctx, body, signature)
	return pkg, keyID, err
}

func (p *Pipeline) verify(ctx context.Context, body, signature []byte) (pkgmeta.Package, string, string, error) {
	if len(strings.TrimSpace(string(signature))) == 0 {
		if !p.AllowUnsigned {
			return pkgmeta.Package{}, "", "", fmt.Errorf("signature required (set BNBLOC_EXT_ALLOW_UNSIGNED=1 for dev)")
		}
		pkg, err := pkgmeta.ParsePackage(body)
		return pkg, "", "", err
	}
	trusted, err := p.trustedKeys(ctx)
	if err != nil {
		return pkgmeta.Package{}, "", "", fmt.Errorf("%w: %w", ErrTrustedKeys, err)
	}
	v, err := pkgmeta.VerifySignedPackage(body, signature, trusted)
	if err != nil {
		return pkgmeta.Package{}, "", "", err
	}
	return v.Package, v.KeyID, v.ContentHash, nil
}

func (p *Pipeline) save(ctx context.Context, rec Record, actor string) error {
	if p.Settings == nil {
		return ErrSettingsStore
	}
	rec.Rollout = NormalizeRollout(rec.Rollout)
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return p.Settings.PutPlatformSetting(ctx, store.PlatformSetting{
		Key: recordKey(extreg.Kind(rec.Kind), rec.Name), Value: string(raw),
		UpdatedAtMs: rec.InstalledAtMs, UpdatedBy: actor,
	})
}

func (p *Pipeline) load(ctx context.Context, kind extreg.Kind, name string) (Record, bool, error) {
	if p.Settings == nil {
		return Record{}, false, ErrSettingsStore
	}
	s, ok, err := p.Settings.GetPlatformSetting(ctx, recordKey(kind, name))
	if err != nil || !ok {
		return Record{}, false, err
	}
	var rec Record
	if err := json.Unmarshal([]byte(s.Value), &rec); err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

func recordKindNameFromKey(key string) string {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(key, recordKeyPrefix), recordKeySuffix)
	recKind, recName, ok := strings.Cut(trimmed, ".")
	if !ok {
		return trimmed
	}
	return recKind + "/" + recName
}

func (p *Pipeline) trustedKeys(ctx context.Context) ([]pkgmeta.TrustedKey, error) {
	if p.Settings == nil {
		return nil, ErrSettingsStore
	}
	s, ok, err := p.Settings.GetPlatformSetting(ctx, p.trustedKeysSettingKey())
	if err != nil || !ok || strings.TrimSpace(s.Value) == "" {
		return nil, err
	}
	var keys []pkgmeta.TrustedKey
	if err := json.Unmarshal([]byte(s.Value), &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func (p *Pipeline) trustedKeysSettingKey() string {
	if strings.TrimSpace(p.TrustedKeysKey) != "" {
		return p.TrustedKeysKey
	}
	return trustedKeysKey
}

func (p *Pipeline) now() int64 {
	if p.Now != nil {
		return p.Now()
	}
	return Now()
}

func (p *Pipeline) installer(kind extreg.Kind) (KindInstaller, bool) {
	if p.Installers != nil {
		in, ok := p.Installers[kind]
		return in, ok
	}
	in, ok := installers[kind]
	return in, ok
}

func (p *Pipeline) installerForPackageKind(pkgKind string) (extreg.Kind, KindInstaller, bool) {
	if p.Installers != nil {
		return installerForPackageKindIn(p.Installers, pkgKind)
	}
	return installerForPackageKindIn(installers, pkgKind)
}

func installerForPackageKindIn(ins map[extreg.Kind]KindInstaller, pkgKind string) (extreg.Kind, KindInstaller, bool) {
	for kind, in := range ins {
		if string(kind) == pkgKind {
			return kind, in, true
		}
		for _, alias := range in.PackageKinds {
			if alias == pkgKind {
				return kind, in, true
			}
		}
	}
	return "", KindInstaller{}, false
}

func (p *Pipeline) audit(ctx context.Context, rec Record, action, outcome, actor, detail string) {
	if p.Audit == nil {
		return
	}
	_ = p.Audit.AppendExtAudit(ctx, store.ExtAuditRecord{
		Kind: rec.Kind, Name: rec.Name, Action: action, Actor: actor,
		KeyID: rec.KeyID, Outcome: outcome, Detail: detail, AtMs: p.now(),
	})
}

func hostsInclude(hosts []extreg.Service, host extreg.Service) bool {
	for _, h := range hosts {
		if h == host {
			return true
		}
	}
	return false
}

func notFound(err error, kind extreg.Kind, name string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("extension %s/%s not found", kind, name)
}
