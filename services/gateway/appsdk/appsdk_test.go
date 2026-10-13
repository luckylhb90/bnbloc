package appsdk

import (
	"bytes"
	"testing"
)

// The demo SDK is the SDK with its mode argument "demo", and nothing else
// changed; the live SDK passes "live" once, at its end.
func TestDemoSDKIsTheSDKInDemoMode(t *testing.T) {
	if !bytes.HasSuffix(bytes.TrimSpace(JS), []byte(liveMode)) {
		t.Fatalf("sdk.js does not end by passing its mode %s", liveMode)
	}
	if bytes.Contains(JS, []byte(demoMode)) {
		t.Fatalf("sdk.js passes %s itself", demoMode)
	}
	if !bytes.HasSuffix(bytes.TrimSpace(DemoJS), []byte(demoMode)) || len(DemoJS) != len(JS) {
		t.Fatalf("DemoJS is not sdk.js in demo mode")
	}
	if !bytes.Equal(bytes.Replace(DemoJS, []byte(demoMode), []byte(liveMode), 1), JS) {
		t.Fatalf("DemoJS differs from sdk.js beyond its mode")
	}
}
