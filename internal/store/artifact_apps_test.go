package store

import (
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// "Is this deliverable an app" has one owner (artifact_apps.go): Artifact.
// IsApp over its two facts, sqlArtifactIsApp beside it, the projection's
// isApp the one way out. Nothing else decides it: no second IsApp, no read
// of the facts outside the store's backends and the room service that
// records the SDK detection (and counts an app's documents only when its
// data space exists), the wire field set only by the projection, the SDK
// detection run only where a version's bytes are stored, and the SDK's path
// spelled only by its detector. The web reads the projection's isApp and
// never re-derives it (web/src/views/workbench/deliverableFilters.ts).
func TestAppRuleSingleOwner(t *testing.T) {
	isAppDef := regexp.MustCompile(`func \([a-z]+ \*?[A-Za-z]+\) IsApp\(|func IsApp\(`)
	facts := regexp.MustCompile(`\.(LoadsAppSDK|HasAppData)\b`)
	factsAllowed := map[string]bool{
		"internal/store/artifact_apps.go":   true,
		"internal/store/artifacts.go":       true,
		"internal/store/mysql_artifacts.go": true,
		"services/room/artifact_files.go":   true, // putVersionBlob records the detection
		"services/room/handoff_import.go":   true, // an imported version, through putVersionBlob
		"services/room/my_apps.go":          true, // counts documents of an app whose data space exists
	}
	wireField := regexp.MustCompile(`\bIsApp:\s`)
	scan := regexp.MustCompile(`appdata\.SDKScan\b`)
	// The predicate's parts (artifact_apps.go): a second SQL spelling of
	// the rule would drift from IsApp.
	sdkSQL := regexp.MustCompile(`loads_app_sdk\s*=\s*1|app_id\s*=\s*a\.artifact_id`)
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(rel string, raw []byte) {
		src := stripLineComments(raw)
		if isAppDef.Match(src) && rel != "internal/store/artifact_apps.go" {
			t.Errorf("%s defines IsApp: whether a deliverable is an app is store.Artifact.IsApp alone", rel)
		}
		if facts.Match(src) && !factsAllowed[rel] {
			t.Errorf("%s reads an app fact (LoadsAppSDK, HasAppData): decide with Artifact.IsApp, or read the projection's isApp", rel)
		}
		if wireField.Match(src) && rel != "internal/store/wire_views.go" {
			t.Errorf("%s sets a wire isApp: Artifact.View is its one writer", rel)
		}
		if scan.Match(src) && rel != "services/room/artifact_files.go" {
			t.Errorf("%s runs the SDK detection: putVersionBlob runs it once, while a version's bytes are stored", rel)
		}
		if rel != "internal/appdata/sdkref.go" && strings.Contains(string(src), `~app/sdk.js`) {
			t.Errorf("%s spells the SDK's path in code: appdata.SDKPath", rel)
		}
		if sdkSQL.Match(src) && rel != "internal/store/artifact_apps.go" {
			t.Errorf("%s queries the app facts in SQL: sqlArtifactIsApp", rel)
		}
	})
}

// stripLineComments drops each line's // comment (a comment may name an
// owner or a path; code may not).
func stripLineComments(src []byte) []byte {
	lines := strings.Split(string(src), "\n")
	for i, line := range lines {
		lines[i], _, _ = strings.Cut(line, "//")
	}
	return []byte(strings.Join(lines, "\n"))
}
