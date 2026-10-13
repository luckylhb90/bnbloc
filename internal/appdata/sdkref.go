package appdata

import "bytes"

// SDKPath is how a site loads the app SDK: relative to the site's base,
// `<script src="~app/sdk.js">` (the gateway serves it beside every site,
// services/gateway/apps.go). A site whose files name it is an interactive
// app even before anyone wrote a document (store.Artifact.IsApp).
const SDKPath = "~app/sdk.js"

// SDKScan detects SDKPath in a site bundle while its bytes stream into the
// blob store, so the detection costs one pass over bytes already moving and
// never a second download: the room service tees every site version it
// stores through one (services/room putVersionBlob) and records the answer
// on the version (store.ArtifactVersion.LoadsAppSDK). A site bundle is an
// uncompressed ustar archive (internal/tarbundle), so every file's bytes
// appear in the stream as they are; a match may span writes.
type SDKScan struct {
	tail  []byte
	found bool
}

var sdkPath = []byte(SDKPath)

// Write scans p; it never fails.
func (s *SDKScan) Write(p []byte) (int, error) {
	if s.found || len(p) == 0 {
		return len(p), nil
	}
	buf := append(s.tail, p...)
	if bytes.Contains(buf, sdkPath) {
		s.found, s.tail = true, nil
		return len(p), nil
	}
	keep := len(sdkPath) - 1
	if len(buf) > keep {
		buf = buf[len(buf)-keep:]
	}
	s.tail = append(s.tail[:0:0], buf...)
	return len(p), nil
}

// Found reports whether the bytes written so far name the SDK.
func (s *SDKScan) Found() bool { return s.found }
