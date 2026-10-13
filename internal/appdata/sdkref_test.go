package appdata

import (
	"bytes"
	"io"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// A site bundle that loads the SDK is found wherever the reference falls,
// split across writes included; one that does not is not.
func TestSDKScanFindsTheReference(t *testing.T) {
	app, err := tarbundle.PackFiles(map[string][]byte{
		"index.html": []byte(`<!doctype html><script src="~app/sdk.js"></script><main id="poll"></main>`),
		"style.css":  []byte(`main{display:grid}`),
	}, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	page, err := tarbundle.PackFiles(map[string][]byte{
		"index.html": []byte(`<!doctype html><p>~app/ is a reserved path; sdk.js is not loaded here</p>`),
	}, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{1, 3, 7, 512, 1 << 20} {
		for name, c := range map[string]struct {
			bundle []byte
			want   bool
		}{"app": {app, true}, "page": {page, false}} {
			var s SDKScan
			r := bytes.NewReader(c.bundle)
			buf := make([]byte, chunk)
			if _, err := io.CopyBuffer(struct{ io.Writer }{&s}, struct{ io.Reader }{r}, buf); err != nil {
				t.Fatal(err)
			}
			if s.Found() != c.want {
				t.Errorf("%s in %d-byte writes: found=%v, want %v", name, chunk, s.Found(), c.want)
			}
		}
	}
}
