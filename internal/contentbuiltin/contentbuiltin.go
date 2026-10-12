// Package contentbuiltin is the built-in source of content components
// (docs/prd/2026-10-12-experts-and-teams.md §5): experts and skill packs that
// ship inside the binaries, versioned with the release. Today that is the
// Expert builder, which authors experts and skill packs by conversation.
//
// Built-in content is not a second library. Each package directory under
// packages/ is packed and validated by internal/contentpkg (the one
// validator) when the process starts, and Overlay presents the packages on
// top of the content store the gateway and realtime already use, as
// components of scope "builtin": extpipe.MemberVisibility admits them to
// every member, so listings, the composer's expert picker, chat pinning,
// contentuse.Effective and the bundle entitlement treat them like any other
// component. They are read-only. A built-in bundle never enters the blob
// store: its artifact key is "builtin/<sha256>" and Bundle serves the bytes
// from the binary.
//
// Changing a package's files means bumping its component.json version, so a
// pinned job or room use never sees different bytes under one version.
package contentbuiltin

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

//go:embed all:packages
var packagesFS embed.FS

// IDPrefix prefixes every built-in component id ("cc_builtin_<name>").
const IDPrefix = store.BuiltinContentIDPrefix

// BundleKeyPrefix prefixes the artifact key of a built-in bundle
// ("builtin/<sha256>").
const BundleKeyPrefix = "builtin/"

// Author is the createdBy of built-in versions.
const Author = "bnbloc"

// BuilderID is the Expert builder's component id.
const BuilderID = IDPrefix + "expert-builder"

// ErrReadOnly: built-in content cannot be changed or deleted.
var ErrReadOnly = errors.New("built-in content is read-only")

// Package is one built-in package: its component row, its one version and
// the bundle bytes.
type Package struct {
	Component store.ContentComponent
	Version   store.ContentComponentVersion
	Bundle    []byte
	Validated *contentpkg.Package
}

var builtins = mustLoad(packagesFS)

// mustLoad packs and validates every package directory. A built-in package
// that does not validate is a build bug: the process refuses to start (and
// the package test fails first).
func mustLoad(fsys fs.FS) map[string]*Package {
	out, err := load(fsys)
	if err != nil {
		panic("contentbuiltin: " + err.Error())
	}
	return out
}

func load(fsys fs.FS) (map[string]*Package, error) {
	dirs, err := fs.ReadDir(fsys, "packages")
	if err != nil {
		return nil, err
	}
	out := map[string]*Package{}
	for _, d := range dirs {
		if !d.IsDir() || tarbundle.Ignored(d.Name()) {
			continue
		}
		p, err := loadPackage(fsys, path.Join("packages", d.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name(), err)
		}
		if p.Validated.Manifest.Name != d.Name() {
			return nil, fmt.Errorf("%s: component.json names %q; the directory is the package name", d.Name(), p.Validated.Manifest.Name)
		}
		out[p.Component.ID] = p
	}
	return out, nil
}

func loadPackage(fsys fs.FS, root string) (*Package, error) {
	// The one directory packer: junk a checkout or an editor leaves beside
	// the files (.DS_Store, ._*) is never packed, so it cannot fail boot.
	raw, err := tarbundle.PackFS(fsys, root, tarbundle.ContentLimits)
	if err != nil {
		return nil, err
	}
	pkg, err := contentpkg.Validate(raw, "", contentpkg.Options{})
	if err != nil {
		return nil, err
	}
	manifest, err := marshalManifest(pkg.Manifest)
	if err != nil {
		return nil, err
	}
	m := pkg.Manifest
	id := IDPrefix + m.Name
	return &Package{
		Component: store.ContentComponent{
			ID: id, Kind: m.Kind, Name: m.Name, Scope: store.ContentScopeBuiltin, Status: store.ContentStatusActive,
			LatestVersion: m.Version, DisplayName: m.DisplayName, Description: m.Description, Category: m.Category,
		},
		Version: store.ContentComponentVersion{
			ComponentID: id, Version: m.Version, ManifestJSON: manifest, BundleBlobKey: BundleKeyPrefix + pkg.SHA256,
			SHA256: pkg.SHA256, SizeBytes: int64(len(raw)), CreatedBy: Author,
		},
		Bundle:    raw,
		Validated: pkg,
	}, nil
}

// All returns the built-in packages, sorted by id.
func All() []*Package {
	out := make([]*Package, 0, len(builtins))
	for _, p := range builtins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Component.ID < out[j].Component.ID })
	return out
}

// Get returns a built-in package by component id.
func Get(id string) (*Package, bool) {
	p, ok := builtins[id]
	return p, ok
}

// IsBuiltinID reports whether id names built-in content (existing or not).
func IsBuiltinID(id string) bool { return strings.HasPrefix(id, IDPrefix) }

// Bundle returns the bytes of a built-in bundle by artifact key.
func Bundle(key string) ([]byte, bool) {
	if !strings.HasPrefix(key, BundleKeyPrefix) {
		return nil, false
	}
	for _, p := range builtins {
		if p.Version.BundleBlobKey == key {
			return p.Bundle, true
		}
	}
	return nil, false
}

// Overlay presents the built-in packages on top of a content store. Reads
// of a built-in id answer from the binary; listings include built-in
// components when the query asks for the builtin scope or names their ids;
// writes to a built-in id are refused (ErrReadOnly). Uses, prompt use counts
// and permission acks of built-in components are ordinary rows of the
// underlying store.
func Overlay(st store.ContentComponentStore) store.ContentComponentStore {
	if st == nil {
		return nil
	}
	if o, ok := st.(*overlay); ok {
		return o
	}
	return &overlay{ContentComponentStore: st}
}

type overlay struct {
	store.ContentComponentStore
}

func (o *overlay) CreateContentComponent(ctx context.Context, c store.ContentComponent, v store.ContentComponentVersion) error {
	if IsBuiltinID(c.ID) || c.Scope == store.ContentScopeBuiltin {
		return ErrReadOnly
	}
	return o.ContentComponentStore.CreateContentComponent(ctx, c, v)
}

func (o *overlay) AddContentComponentVersion(ctx context.Context, c store.ContentComponent, prevLatest string, v store.ContentComponentVersion) error {
	if IsBuiltinID(c.ID) {
		return ErrReadOnly
	}
	return o.ContentComponentStore.AddContentComponentVersion(ctx, c, prevLatest, v)
}

func (o *overlay) DeleteContentComponent(ctx context.Context, id string) error {
	if IsBuiltinID(id) {
		return ErrReadOnly
	}
	return o.ContentComponentStore.DeleteContentComponent(ctx, id)
}

func (o *overlay) SetContentComponentStatus(ctx context.Context, id string, status store.ContentStatus, atMs int64) error {
	if IsBuiltinID(id) {
		return ErrReadOnly
	}
	return o.ContentComponentStore.SetContentComponentStatus(ctx, id, status, atMs)
}

func (o *overlay) SetContentComponentTenants(ctx context.Context, id string, tenants []string, atMs int64) error {
	if IsBuiltinID(id) {
		return ErrReadOnly
	}
	return o.ContentComponentStore.SetContentComponentTenants(ctx, id, tenants, atMs)
}

// PutContentUse checks that a built-in component exists before its use is
// stored (the store cannot: built-in components have no rows).
func (o *overlay) PutContentUse(ctx context.Context, u store.ContentUse) error {
	if IsBuiltinID(u.ComponentID) {
		if _, ok := builtins[u.ComponentID]; !ok {
			return store.ErrContentNotFound
		}
	}
	return o.ContentComponentStore.PutContentUse(ctx, u)
}

// RecordContentPromptUse checks that a built-in pack exists before its
// prompt use is counted (the store cannot, as for PutContentUse).
func (o *overlay) RecordContentPromptUse(ctx context.Context, tenantID, componentID, promptID string, atMs int64) error {
	if IsBuiltinID(componentID) {
		if _, ok := builtins[componentID]; !ok {
			return store.ErrContentNotFound
		}
	}
	return o.ContentComponentStore.RecordContentPromptUse(ctx, tenantID, componentID, promptID, atMs)
}

func (o *overlay) GetContentComponent(ctx context.Context, id string) (store.ContentComponent, error) {
	if IsBuiltinID(id) {
		if p, ok := builtins[id]; ok {
			return p.Component, nil
		}
		return store.ContentComponent{}, store.ErrContentNotFound
	}
	return o.ContentComponentStore.GetContentComponent(ctx, id)
}

// admits reports whether a listing query includes a built-in component:
// built-in content has no tenant and no owner, so only queries without
// those filters that ask for the builtin scope (or name the id) list it.
func admits(q store.ContentComponentQuery, c store.ContentComponent) bool {
	if q.TenantID != "" || q.Owner != "" || q.SourceID != "" || (q.Kind != "" && q.Kind != c.Kind) || (q.Name != "" && q.Name != c.Name) {
		return false
	}
	if len(q.IDs) > 0 {
		for _, id := range q.IDs {
			if id == c.ID {
				return len(q.Scopes) == 0 || hasScope(q.Scopes, store.ContentScopeBuiltin)
			}
		}
		return false
	}
	return hasScope(q.Scopes, store.ContentScopeBuiltin)
}

func hasScope(list []store.ContentScope, s store.ContentScope) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (o *overlay) ListContentComponents(ctx context.Context, q store.ContentComponentQuery) ([]store.ContentComponent, error) {
	var out []store.ContentComponent
	for _, p := range All() {
		if admits(q, p.Component) {
			out = append(out, p.Component)
		}
	}
	// A query for the builtin scope alone never reaches the store.
	if len(q.Scopes) == 1 && q.Scopes[0] == store.ContentScopeBuiltin {
		return out, nil
	}
	if len(q.IDs) > 0 {
		rest := q.IDs[:0:0]
		for _, id := range q.IDs {
			if !IsBuiltinID(id) {
				rest = append(rest, id)
			}
		}
		if len(rest) == 0 {
			return out, nil
		}
		q.IDs = rest
	}
	stored, err := o.ContentComponentStore.ListContentComponents(ctx, q)
	if err != nil {
		return nil, err
	}
	return append(out, stored...), nil
}

func (o *overlay) ListContentComponentVersions(ctx context.Context, componentID string) ([]store.ContentComponentVersion, error) {
	if IsBuiltinID(componentID) {
		if p, ok := builtins[componentID]; ok {
			return []store.ContentComponentVersion{p.Version}, nil
		}
		return nil, nil
	}
	return o.ContentComponentStore.ListContentComponentVersions(ctx, componentID)
}

func (o *overlay) GetContentComponentVersion(ctx context.Context, componentID, version string) (store.ContentComponentVersion, error) {
	if IsBuiltinID(componentID) {
		if p, ok := builtins[componentID]; ok && p.Version.Version == version {
			return p.Version, nil
		}
		return store.ContentComponentVersion{}, store.ErrContentNotFound
	}
	return o.ContentComponentStore.GetContentComponentVersion(ctx, componentID, version)
}

// ContentComponentsByBlob answers a built-in bundle key with its built-in
// components (in every member's reach); any other key from the store.
func (o *overlay) ContentComponentsByBlob(ctx context.Context, q store.ContentBlobQuery) ([]store.ContentComponent, error) {
	if strings.HasPrefix(q.Key, BundleKeyPrefix) {
		out := []store.ContentComponent{}
		for _, p := range All() {
			if p.Version.BundleBlobKey == q.Key {
				out = append(out, p.Component)
			}
		}
		return out, nil
	}
	return o.ContentComponentStore.ContentComponentsByBlob(ctx, q)
}

func marshalManifest(m contentpkg.Manifest) (json.RawMessage, error) {
	raw, err := json.Marshal(m)
	return json.RawMessage(raw), err
}
