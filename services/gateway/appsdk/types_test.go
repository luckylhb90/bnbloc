package appsdk

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// sdk.d.ts is the contract an agent reads (every pull workspace holds it,
// component.RoomAppSDKTypesFile) instead of a skill's copy
// (docs/prd/2026-10-20-no-built-in-content.md §2.5), so what it states is
// the server's own: the caps of an app's data and files and the filter
// operators a find accepts. A changed cap or operator fails here until
// the declarations follow.
func TestTypesStateTheServersCaps(t *testing.T) {
	src := string(Types)
	flat := strings.Join(strings.Fields(strings.ReplaceAll(src, "\n//", "\n")), " ")
	for _, want := range []string{
		fmt.Sprintf("Caps: %d KiB per document, %dk documents or %d MiB per app, %d collections per app.", wire.MaxAppDocBytes>>10, wire.MaxAppDocs/1000, wire.MaxAppBytes>>20, wire.MaxAppCollections),
		fmt.Sprintf("Files (app.files): %d MiB each, %d or %d GiB per app", wire.MaxAttachmentBytes>>20, wire.MaxAppFiles, wire.MaxAppFileBytes>>30),
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("sdk.d.ts does not state %q", want)
		}
	}
	filter := regexp.MustCompile(`(?s)type BnblocAppFilter = .*?>;`).FindString(src)
	var ops []string
	for _, m := range regexp.MustCompile(`(\$\w+)\??:`).FindAllStringSubmatch(filter, -1) {
		ops = append(ops, m[1])
	}
	if !slices.Equal(ops, appdata.FilterOps) {
		t.Errorf("sdk.d.ts declares the filter operators %v, the server takes %v", ops, appdata.FilterOps)
	}
}
