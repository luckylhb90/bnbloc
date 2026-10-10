package webui

import (
	"encoding/json"
	"io/fs"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Build returns the identity of the embedded web bundle from the build.json
// the web build writes beside index.html (web/vite.config.ts), or nil when
// the embedded tree has none (the placeholder bundle of a non-gateway image,
// or a stale local build).
func Build() *wire.WebBundleVersion {
	raw, err := fs.ReadFile(files, "static/build.json")
	if err != nil {
		return nil
	}
	var v wire.WebBundleVersion
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}
