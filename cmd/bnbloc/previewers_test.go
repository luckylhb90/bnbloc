package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

func writePreviewerFixture(t *testing.T, kind, entry string) (pkgPath, siteDir string) {
	t.Helper()
	dir := t.TempDir()
	siteDir = filepath.Join(dir, "site")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"kind": kind, "contract": pkgmeta.PreviewerContractV1, "runtime": "bundle",
		"name": "sprites", "version": "1.0.0", "publisher": "examples",
		"artifacts": map[string]any{"bundle": map[string]string{"url": "", "sha256": ""}},
		"previewer": map[string]any{"entry": entry, "match": map[string]any{"extensions": []string{".png"}}, "rank": 50},
	})
	pkgPath = filepath.Join(dir, "package.json")
	if err := os.WriteFile(pkgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return pkgPath, siteDir
}

func TestPreviewerPublishInputs_packsAndChecksBeforeUpload(t *testing.T) {
	pkgPath, site := writePreviewerFixture(t, pkgmeta.KindPreviewerPackageV1, "index.html")
	pkg, bundlePath, cleanup, err := previewerPublishInputs(pkgPath, "", site)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := tarbundle.Parse(raw, "", tarbundle.PreviewerLimits); err != nil || b.RequireEntry("index.html") != nil || pkg.Name != "sprites" {
		t.Fatalf("packed bundle = %v %v", b, err)
	}

	// The same archive given as --bundle.
	if _, _, cleanup, err := previewerPublishInputs(pkgPath, bundlePath, ""); err != nil {
		t.Fatal(err)
	} else {
		cleanup()
	}
}

func TestPreviewerPublishInputs_refusesBadInputs(t *testing.T) {
	pkgPath, site := writePreviewerFixture(t, pkgmeta.KindPreviewerPackageV1, "viewer.html")
	if _, _, _, err := previewerPublishInputs(pkgPath, "", site); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("missing entry: err = %v", err)
	}
	if _, _, _, err := previewerPublishInputs(pkgPath, "", ""); err == nil {
		t.Fatal("neither --bundle nor --dir accepted")
	}
	if _, _, _, err := previewerPublishInputs(pkgPath, "x.tar", site); err == nil {
		t.Fatal("both --bundle and --dir accepted")
	}
	notTar := filepath.Join(t.TempDir(), "bundle.tar")
	_ = os.WriteFile(notTar, []byte("plain text, not an archive at all, long enough to look like one"), 0o644)
	okPkg, _ := writePreviewerFixture(t, pkgmeta.KindPreviewerPackageV1, "index.html")
	if _, _, _, err := previewerPublishInputs(okPkg, notTar, ""); err == nil {
		t.Fatal("non-archive bundle accepted")
	}
}
