// Package appsdk is the interactive apps' first-party SDK
// (docs/prd/2026-10-12-interactive-apps.md §4.1): sdk.js, which the gateway
// serves at ~app/sdk.js beside every published site, and its types,
// sdk.d.ts, served at ~app/sdk.d.ts. Both are embedded, so the SDK is
// versioned with the gateway that answers its calls.
package appsdk

import (
	"bytes"
	_ "embed"
)

// JS is the SDK script.
//
//go:embed sdk.js
var JS []byte

// Types is the SDK's TypeScript declarations.
//
//go:embed sdk.d.ts
var Types []byte

// The SDK's mode argument, spelled once at the end of sdk.js: "live" (the
// data API) or "demo" (an in-memory data space of the page alone).
const (
	liveMode = `, "live");`
	demoMode = `, "demo");`
)

// DemoJS is the SDK in demo mode (docs/prd/2026-10-13-app-gallery.md §7,
// "Try demo"): the same file with its mode argument "demo", so every call
// is answered in the page's own memory and nothing is ever sent. The
// gateway serves it at ~app/sdk.js under a demo ticket only.
var DemoJS = withMode(JS)

func withMode(js []byte) []byte {
	if bytes.Count(js, []byte(liveMode)) != 1 {
		panic("appsdk: sdk.js must end by passing its mode once: " + liveMode)
	}
	return bytes.Replace(js, []byte(liveMode), []byte(demoMode), 1)
}
