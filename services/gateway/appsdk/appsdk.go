// Package appsdk is the interactive apps' first-party SDK
// (docs/prd/2026-10-12-interactive-apps.md §4.1): sdk.js, which the gateway
// serves at ~app/sdk.js beside every published site, and its types,
// sdk.d.ts, served at ~app/sdk.d.ts. Both are embedded, so the SDK is
// versioned with the gateway that answers its calls.
package appsdk

import _ "embed"

// JS is the SDK script.
//
//go:embed sdk.js
var JS []byte

// Types is the SDK's TypeScript declarations.
//
//go:embed sdk.d.ts
var Types []byte
