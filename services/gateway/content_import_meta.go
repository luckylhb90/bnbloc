package gateway

import (
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
)

// withImportMeta is an import with what the member set beside its files
// (the publish step of a local skill, where they reviewed the entry): the
// display name, description and tags of ?displayName=, ?description= and
// each ?tag=, in place of what the import read from the files. Nothing
// given leaves the import as it is. The package is packed again, so the
// content validator still owns what each may be (a tag's rule, a
// description's bound).
func withImportMeta(pkg *contentpkg.Package, bundle []byte, q url.Values, opts contentpkg.Options) (*contentpkg.Package, []byte, error) {
	displayName, description := strings.TrimSpace(q.Get("displayName")), strings.TrimSpace(q.Get("description"))
	var tags []string
	for _, t := range q["tag"] {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	if displayName == "" && description == "" && len(tags) == 0 {
		return pkg, bundle, nil
	}
	m := pkg.Manifest
	if displayName != "" {
		m.DisplayName = displayName
	}
	if description != "" {
		m.Description = description
	}
	if len(tags) > 0 {
		m.Tags = tags
	}
	files := map[string][]byte{}
	for _, p := range pkg.Bundle.Paths() {
		if p == contentpkg.ManifestFile {
			continue
		}
		files[p], _ = pkg.Bundle.File(p)
	}
	return contentpkg.Pack(m, files, opts)
}
