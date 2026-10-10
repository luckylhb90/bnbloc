package wire

// Web Push DTOs (N4): the built-in browser notification channel. realtime
// serves the routes and delivers WebPushMessage, encrypted per RFC 8291, to
// every subscription of a notification's recipients.

// WebPushPublicKey answers GET /v1/push/vapid-public-key (404 when the
// deployment has no VAPID key pair: the channel is disabled).
type WebPushPublicKey struct {
	// PublicKey is the VAPID application server key, base64url (unpadded)
	// of the uncompressed P-256 point, as PushManager.subscribe expects it.
	PublicKey string `json:"publicKey"`
}

// WebPushSubscriptionKeys are a PushSubscription's encryption keys
// (PushSubscription.toJSON().keys), base64url.
type WebPushSubscriptionKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// WebPushSubscriptionInput is the POST /v1/my/push-subscriptions body (the
// browser's PushSubscription.toJSON()) and, with only Endpoint, the DELETE
// body.
type WebPushSubscriptionInput struct {
	Endpoint string                  `json:"endpoint"`
	Keys     WebPushSubscriptionKeys `json:"keys"`
}

// WebPushMessage is the decrypted push message the service worker receives.
type WebPushMessage struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	RoomID string `json:"roomId,omitempty"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	// URL is the absolute deep link ("" without a public URL); Path is its
	// hash route ("#/room/<id>?seq=<n>"), which the worker opens on its own
	// origin when URL is empty.
	URL  string `json:"url,omitempty"`
	Path string `json:"path"`
}
