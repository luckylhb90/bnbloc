// Package appdata owns the rules of interactive apps' data
// (docs/prd/2026-10-12-interactive-apps.md): its encryption (Cipher), the
// document shape the server stamps (doc.go) and the filter whitelist
// (filter.go). The room service (services/room/appdata.go) serves it.
package appdata

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Cipher is the one owner of app data encryption (§4.8): envelope
// encryption with one random 256-bit data key per app, wrapped by the
// deployment key (sealbox, BNBLOC_SECRET_STORE_KEY) and stored only wrapped
// (app_keys); every document body is AES-256-GCM under the app's key with
// "app_id|collection|doc_id" as additional data, so a ciphertext moved to
// another document or app does not open; the author's real identity is
// sealed the same way. Only BodySealed / AuthorSealed values this package
// produces are ever written (TestSealedColumnsWrittenOnlyByCipher).
//
// Destroying an app's key row (store.AppDocs.WipeApp, room deletion)
// crypto-shreds its documents; rotating the deployment key re-wraps the
// data keys only (Rotate).
type Cipher struct {
	box *sealbox.Box
}

// NewCipher builds the cipher over the deployment's box; nil when there is
// no box (no BNBLOC_SECRET_STORE_KEY: app data is off).
func NewCipher(box *sealbox.Box) *Cipher {
	if box == nil {
		return nil
	}
	return &Cipher{box: box}
}

// ErrOpen reports a sealed value that does not open: tampered, moved to
// another document, or sealed under a key that was destroyed.
var ErrOpen = errors.New("app data does not open")

// sealVersion prefixes every sealed body and author, so a future scheme is
// a new version.
const sealVersion byte = 1

// keyAD binds a wrapped data key to its app, room and tenant.
func keyAD(tenantID, roomID, appID string) string {
	return "bnbloc.app-key|" + tenantID + "|" + roomID + "|" + appID
}

// NewKey mints a data key for the app and returns it wrapped.
func (c *Cipher) NewKey(tenantID, roomID, appID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return c.box.Seal(raw, keyAD(tenantID, roomID, appID))
}

// Key unwraps an app's data key.
func (c *Cipher) Key(k store.AppKey) (*DataKey, error) {
	raw, err := c.box.Open(k.WrappedKey, keyAD(k.TenantID, k.RoomID, k.AppID))
	if err != nil {
		return nil, fmt.Errorf("unwrap the key of app %s: %w", k.AppID, err)
	}
	return newDataKey(k.AppID, raw)
}

// Rewrap re-wraps k's data key under to (a rotation's new deployment key).
func (c *Cipher) Rewrap(k store.AppKey, to *Cipher) (string, error) {
	raw, err := c.box.Open(k.WrappedKey, keyAD(k.TenantID, k.RoomID, k.AppID))
	if err != nil {
		return "", fmt.Errorf("unwrap the key of app %s: %w", k.AppID, err)
	}
	return to.box.Seal(raw, keyAD(k.TenantID, k.RoomID, k.AppID))
}

// Rotate re-wraps every app's data key from the old deployment key to the
// new one; documents are not touched. It returns the keys re-wrapped.
func Rotate(ctx context.Context, docs store.AppDocs, from, to *Cipher) (int, error) {
	return docs.RewrapAppKeys(ctx, func(k store.AppKey) (string, error) { return from.Rewrap(k, to) })
}

// DataKey is one app's unwrapped key: it seals and opens that app's
// documents and derives its pseudonyms.
type DataKey struct {
	appID string
	aead  cipher.AEAD
	byKey []byte
}

// subkey derives a purpose-bound key from an app's data key.
func subkey(raw []byte, purpose string) []byte {
	m := hmac.New(sha256.New, raw)
	m.Write([]byte("bnbloc.app-data|" + purpose))
	return m.Sum(nil)
}

func newDataKey(appID string, raw []byte) (*DataKey, error) {
	if len(raw) != 32 {
		return nil, fmt.Errorf("app %s: data key is %d bytes, want 32", appID, len(raw))
	}
	block, err := aes.NewCipher(subkey(raw, "body"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &DataKey{appID: appID, aead: aead, byKey: subkey(raw, "by")}, nil
}

// Pseudonym is a participant's per-app id (a document's _by, the app's
// me().id): an HMAC keyed by the app, so the same participant has
// unrelated ids in two apps and nobody without the app's key can link one
// to a person. A wipe mints a new key, so it also renews the pseudonyms.
func (k *DataKey) Pseudonym(participant string) string {
	m := hmac.New(sha256.New, k.byKey)
	m.Write([]byte(participant))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// bodyAD is the additional data of a document body: its app, collection
// and id. Collection names and document ids never contain "|" (ValidName,
// ValidDocID).
func bodyAD(d store.AppDoc) []byte {
	return []byte(d.AppID + "|" + d.Collection + "|" + d.DocID)
}

func authorAD(d store.AppDoc) []byte { return append(bodyAD(d), "|author"...) }

func (k *DataKey) seal(plain, ad []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{sealVersion}, nonce...)
	return k.aead.Seal(out, nonce, plain, ad), nil
}

func (k *DataKey) open(sealed, ad []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(sealed) < 1+n || sealed[0] != sealVersion {
		return nil, ErrOpen
	}
	out, err := k.aead.Open(nil, sealed[1:1+n], sealed[1+n:], ad)
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}

// checkApp refuses a document of another app than the key's.
func (k *DataKey) checkApp(d store.AppDoc) error {
	if d.AppID != k.appID {
		return fmt.Errorf("document of app %s under the key of app %s", d.AppID, k.appID)
	}
	return nil
}

// SealBody seals body into d (its AppID, Collection and DocID set).
func (k *DataKey) SealBody(d *store.AppDoc, body []byte) error {
	if err := k.checkApp(*d); err != nil {
		return err
	}
	sealed, err := k.seal(body, bodyAD(*d))
	if err != nil {
		return err
	}
	d.BodySealed = sealed
	return nil
}

// SealAuthor seals the author's real identity (a participant) into d.
func (k *DataKey) SealAuthor(d *store.AppDoc, participant string) error {
	if err := k.checkApp(*d); err != nil {
		return err
	}
	sealed, err := k.seal([]byte(participant), authorAD(*d))
	if err != nil {
		return err
	}
	d.AuthorSealed = sealed
	return nil
}

// Body opens d's body.
func (k *DataKey) Body(d store.AppDoc) ([]byte, error) {
	if err := k.checkApp(d); err != nil {
		return nil, err
	}
	return k.open(d.BodySealed, bodyAD(d))
}

// Author opens d's author (the participant who inserted it).
func (k *DataKey) Author(d store.AppDoc) (string, error) {
	if err := k.checkApp(d); err != nil {
		return "", err
	}
	raw, err := k.open(d.AuthorSealed, authorAD(d))
	return string(raw), err
}

// fileAD is the additional data of an app file's name: its app and id
// (file ids never contain "|": ValidDocID).
func fileAD(f store.AppFile) []byte { return []byte(f.AppID + "|file|" + f.FileID) }

func fileAuthorAD(f store.AppFile) []byte { return append(fileAD(f), "|author"...) }

// checkFileApp refuses a file of another app than the key's.
func (k *DataKey) checkFileApp(f store.AppFile) error {
	if f.AppID != k.appID {
		return fmt.Errorf("file of app %s under the key of app %s", f.AppID, k.appID)
	}
	return nil
}

// SealFile seals a file's name and its uploader's real identity (a
// participant) into f (its AppID and FileID set).
func (k *DataKey) SealFile(f *store.AppFile, name, participant string) error {
	if err := k.checkFileApp(*f); err != nil {
		return err
	}
	n, err := k.seal([]byte(name), fileAD(*f))
	if err != nil {
		return err
	}
	a, err := k.seal([]byte(participant), fileAuthorAD(*f))
	if err != nil {
		return err
	}
	f.NameSealed, f.AuthorSealed = n, a
	return nil
}

// FileName opens f's name.
func (k *DataKey) FileName(f store.AppFile) (string, error) {
	if err := k.checkFileApp(f); err != nil {
		return "", err
	}
	raw, err := k.open(f.NameSealed, fileAD(f))
	return string(raw), err
}

// FileAuthor opens f's uploader (the participant).
func (k *DataKey) FileAuthor(f store.AppFile) (string, error) {
	if err := k.checkFileApp(f); err != nil {
		return "", err
	}
	raw, err := k.open(f.AuthorSealed, fileAuthorAD(f))
	return string(raw), err
}
