package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Web Push (N4) is a built-in notification channel: a member turns on
// browser notifications on a device, the browser's PushSubscription is
// stored, and the outbox worker delivers every notification for that member
// to each of their subscriptions as well — after the same preference, mute
// and collapse filtering, with the same title/text/url. The VAPID key pair is
// deployment configuration; without it the channel is off.
const (
	WebPushPublicKeyEnv  = "BNBLOC_WEB_PUSH_VAPID_PUBLIC_KEY"
	WebPushPrivateKeyEnv = "BNBLOC_WEB_PUSH_VAPID_PRIVATE_KEY"
	WebPushSubjectEnv    = "BNBLOC_WEB_PUSH_VAPID_SUBJECT"
)

// WebPushChannel is the outbox notifier value of the built-in channel. A
// component name can never contain ':' (pkgmeta), so it never collides with
// an installed notifier.
const WebPushChannel = "builtin:web-push"

// Web Push bounds.
const (
	maxPushSubscriptionsPerMember = 20
	maxPushEndpointLen            = 2048
	// pushTTL is how long a push service holds an undeliverable message
	// (device offline) before dropping it.
	pushTTL = 24 * time.Hour
)

// WebPushConfig is the deployment's VAPID identity.
type WebPushConfig struct {
	PublicKey  string // base64url uncompressed P-256 point (65 bytes)
	PrivateKey string // base64url P-256 scalar (32 bytes)
	Subject    string // mailto:<address> or https://<origin>
}

// Enabled reports whether the channel is configured.
func (c WebPushConfig) Enabled() bool { return c.PublicKey != "" && c.PrivateKey != "" }

// WebPushConfigFromEnv reads the VAPID configuration. Neither key set is the
// disabled channel (zero config, no error). One key without the other, a
// malformed or mismatched pair, or no subject is a configuration error. The
// subject defaults to the deployment's public URL when that is https.
func WebPushConfigFromEnv() (WebPushConfig, error) {
	c := WebPushConfig{
		PublicKey:  strings.TrimSpace(config.FromEnvRaw(WebPushPublicKeyEnv)),
		PrivateKey: strings.TrimSpace(config.FromEnvRaw(WebPushPrivateKeyEnv)),
		Subject:    strings.TrimSpace(config.FromEnvRaw(WebPushSubjectEnv)),
	}
	if c.PublicKey == "" && c.PrivateKey == "" {
		return WebPushConfig{}, nil
	}
	if c.Subject == "" {
		if pub := strings.TrimRight(strings.TrimSpace(config.FromEnvRaw(PublicURLEnv)), "/"); strings.HasPrefix(pub, "https://") {
			c.Subject = pub
		}
	}
	return c, c.Validate()
}

// Validate checks a configured key pair and subject.
func (c WebPushConfig) Validate() error {
	if c.PublicKey == "" || c.PrivateKey == "" {
		return fmt.Errorf("web push: set both %s and %s (or neither to disable browser notifications)", WebPushPublicKeyEnv, WebPushPrivateKeyEnv)
	}
	pub, err := decodeB64URL(c.PublicKey)
	if err != nil {
		return fmt.Errorf("web push: %s is not base64url: %w", WebPushPublicKeyEnv, err)
	}
	priv, err := decodeB64URL(c.PrivateKey)
	if err != nil {
		return fmt.Errorf("web push: %s is not base64url: %w", WebPushPrivateKeyEnv, err)
	}
	key, err := ecdh.P256().NewPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("web push: %s is not a P-256 private key: %w", WebPushPrivateKeyEnv, err)
	}
	if string(key.PublicKey().Bytes()) != string(pub) {
		return fmt.Errorf("web push: %s does not match %s (generate a pair with `bnbloc keys gen-vapid`)", WebPushPublicKeyEnv, WebPushPrivateKeyEnv)
	}
	switch {
	case strings.HasPrefix(c.Subject, "mailto:") && len(c.Subject) > len("mailto:"):
	case strings.HasPrefix(c.Subject, "https://") && len(c.Subject) > len("https://"):
	default:
		return fmt.Errorf("web push: %s must be mailto:<address> or https://<origin> (it defaults to an https %s)", WebPushSubjectEnv, PublicURLEnv)
	}
	return nil
}

// GenerateVAPIDKeys creates a VAPID key pair (base64url, unpadded).
func GenerateVAPIDKeys() (publicKey, privateKey string, err error) {
	privateKey, publicKey, err = webpush.GenerateVAPIDKeys()
	return publicKey, privateKey, err
}

func decodeB64URL(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	return base64.RawURLEncoding.DecodeString(s)
}

// WebPush delivers notifications to members' browser subscriptions and
// serves the subscription routes.
type WebPush struct {
	Config WebPushConfig
	Subs   store.WebPushSubscriptionStore
	// Client sends to push services; nil uses a client that only reaches
	// public addresses (a subscription endpoint is member-supplied).
	Client *http.Client
	Now    func() time.Time
}

// Enabled reports whether the channel can deliver.
func (p *WebPush) Enabled() bool { return p != nil && p.Config.Enabled() && p.Subs != nil }

func (p *WebPush) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// pushSubscribed keeps the members with at least one browser subscription.
// A read error drops the member (the channel is best-effort).
func pushSubscribed(ctx context.Context, subs store.WebPushSubscriptionStore, members []string) []string {
	var out []string
	for _, m := range members {
		if rows, err := subs.ListWebPushSubscriptions(ctx, m); err == nil && len(rows) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// pushOutcome classifies one send to one subscription.
type pushOutcome int

const (
	pushDelivered pushOutcome = iota
	pushGone                  // 404/410 or an unreachable endpoint: subscription dropped
	pushRetry                 // 429, 5xx, transport error
	pushFailed                // any other answer: final for this message
)

// Deliver sends p to every subscription of each recipient and reports the
// per-recipient outcome in the notifier result shape, so the worker applies
// the same narrowing and retry rules as for a notifier component. A
// recipient is retried when any of their subscriptions failed transiently
// (the message's tag makes a re-shown notification replace the first).
func (p *WebPush) Deliver(ctx context.Context, payload wire.NotificationPayload) []wire.NotificationRecipientResult {
	msg, err := json.Marshal(wire.WebPushMessage{
		ID: payload.ID, Type: payload.Type, RoomID: payload.RoomID, Title: payload.Title, Body: payload.Text,
		URL: payload.URL, Path: urlHash(payload.URL, payload.RoomID),
	})
	results := make([]wire.NotificationRecipientResult, 0, len(payload.Recipients))
	for _, r := range payload.Recipients {
		res := wire.NotificationRecipientResult{UserID: r.UserID, OK: true}
		if err != nil {
			res = wire.NotificationRecipientResult{UserID: r.UserID, Error: err.Error()}
			results = append(results, res)
			continue
		}
		subs, lerr := p.Subs.ListWebPushSubscriptions(ctx, r.UserID)
		if lerr != nil {
			results = append(results, wire.NotificationRecipientResult{UserID: r.UserID, Error: "read push subscriptions: " + lerr.Error(), Retryable: true})
			continue
		}
		var errs []string
		for _, s := range subs {
			outcome, detail := p.send(ctx, payload.ID, msg, s)
			switch outcome {
			case pushGone:
				if derr := p.Subs.DeleteWebPushSubscription(ctx, r.UserID, s.ID); derr != nil {
					errs = append(errs, "drop expired subscription: "+derr.Error())
				}
			case pushRetry:
				res.OK, res.Retryable = false, true
				errs = append(errs, detail)
			case pushFailed:
				errs = append(errs, detail)
			}
		}
		if len(errs) > 0 {
			res.Error = strings.Join(errs, "; ")
			if !res.Retryable && len(errs) == len(subs) {
				res.OK = false // every device refused the message
			}
		}
		results = append(results, res)
	}
	return results
}

// send posts one encrypted message to one subscription.
func (p *WebPush) send(ctx context.Context, eventID string, msg []byte, s store.WebPushSubscription) (pushOutcome, string) {
	if err := validPushEndpoint(s.Endpoint); err != nil {
		return pushGone, err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	client := p.Client
	if client == nil {
		client = publicPushClient
	}
	resp, err := webpush.SendNotificationWithContext(ctx, msg, &webpush.Subscription{
		Endpoint: s.Endpoint, Keys: webpush.Keys{P256dh: s.P256dh, Auth: s.Auth},
	}, &webpush.Options{
		HTTPClient:      client,
		Subscriber:      strings.TrimPrefix(p.Config.Subject, "mailto:"),
		VAPIDPublicKey:  p.Config.PublicKey,
		VAPIDPrivateKey: p.Config.PrivateKey,
		TTL:             int(pushTTL.Seconds()),
		Urgency:         webpush.UrgencyNormal,
		// A retry of the same event replaces a still-undelivered copy.
		Topic: pushTopic(eventID),
	})
	if err != nil {
		if errors.Is(err, errPushAddressRefused) {
			return pushGone, err.Error()
		}
		return pushRetry, "push service unreachable: " + err.Error()
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return pushDelivered, ""
	case code == http.StatusNotFound || code == http.StatusGone:
		return pushGone, ""
	case code == http.StatusTooManyRequests || code >= 500:
		return pushRetry, fmt.Sprintf("push service answered HTTP %d", code)
	default:
		return pushFailed, fmt.Sprintf("push service answered HTTP %d", code)
	}
}

// pushTopic derives the Topic header (at most 32 base64url characters) from
// the event id.
func pushTopic(eventID string) string {
	sum := sha256.Sum256([]byte(eventID))
	return hex.EncodeToString(sum[:16])
}

// validPushEndpoint accepts an absolute https URL of bounded length.
func validPushEndpoint(raw string) error {
	if len(raw) == 0 || len(raw) > maxPushEndpointLen {
		return errors.New("push endpoint must be 1-2048 characters")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("push endpoint must be an https URL")
	}
	return nil
}

// errPushAddressRefused marks a push endpoint resolving to a non-public
// address: subscription endpoints are member-supplied, so the channel never
// reaches the deployment's own network.
var errPushAddressRefused = errors.New("push endpoint address is not public")

// checkPublicAddr refuses every address a push service never lives at:
// loopback, private, CGNAT, link-local, unspecified, multicast, metadata.
func checkPublicAddr(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %q", errPushAddressRefused, host)
	}
	addr = addr.Unmap()
	if extgov.CheckAddr(addr) != nil || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || cgnat.Contains(addr) {
		return fmt.Errorf("%w: %s", errPushAddressRefused, addr)
	}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicPushClient dials push services directly with the public-address
// check, or through the operator's egress proxy (HTTPS_PROXY) when one is
// configured — the proxy is then the egress policy.
var publicPushClient = &http.Client{
	Transport: pushTransport{direct: newPushTransport(nil, checkPublicAddr), proxied: newPushTransport(http.ProxyFromEnvironment, nil)},
	// A push service answers directly; a redirect is never followed.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func newPushTransport(proxy func(*http.Request) (*url.URL, error), control func(string, string, syscall.RawConn) error) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = proxy
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: control}
	t.DialContext = dialer.DialContext
	return t
}

type pushTransport struct{ direct, proxied http.RoundTripper }

func (t pushTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if proxy, err := http.ProxyFromEnvironment(req); err == nil && proxy != nil {
		return t.proxied.RoundTrip(req)
	}
	return t.direct.RoundTrip(req)
}

// --- member routes ---

// Register mounts the Web Push routes:
//
//	GET    /v1/push/vapid-public-key   (404 while the channel is disabled)
//	POST   /v1/my/push-subscriptions   (subscribe this browser; 404 while disabled)
//	DELETE /v1/my/push-subscriptions   (unsubscribe this browser, by endpoint)
func (p *WebPush) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/push/vapid-public-key", p.handlePublicKey)
	mux.HandleFunc("POST /v1/my/push-subscriptions", p.handleSubscribe)
	mux.HandleFunc("DELETE /v1/my/push-subscriptions", p.handleUnsubscribe)
}

func (p *WebPush) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := caller(w, r); !ok {
		return
	}
	if !p.Enabled() {
		httpx.Error(w, "browser notifications are not configured on this deployment", http.StatusNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.WebPushPublicKey{PublicKey: strings.TrimRight(p.Config.PublicKey, "=")})
}

// subscriptionID keys a subscription by its endpoint.
func subscriptionID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

func (p *WebPush) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	user, ok := caller(w, r)
	if !ok {
		return
	}
	if !p.Enabled() {
		httpx.Error(w, "browser notifications are not configured on this deployment", http.StatusNotFound)
		return
	}
	var in wire.WebPushSubscriptionInput
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &in) {
		return
	}
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	if err := validPushEndpoint(in.Endpoint); err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validSubscriptionKeys(in.Keys); err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := p.now().UnixMilli()
	id := subscriptionID(in.Endpoint)
	existing, err := p.Subs.ListWebPushSubscriptions(r.Context(), user)
	if err != nil {
		obs.ServerError(w, "read push subscriptions failed", err)
		httpx.Error(w, "read push subscriptions failed", http.StatusInternalServerError)
		return
	}
	// A member's devices are bounded: the least recently refreshed
	// subscriptions make room for a new one.
	known := false
	for _, s := range existing {
		known = known || s.ID == id
	}
	for i := 0; !known && len(existing)-i >= maxPushSubscriptionsPerMember; i++ {
		if err := p.Subs.DeleteWebPushSubscription(r.Context(), user, existing[i].ID); err != nil {
			obs.ServerError(w, "trim push subscriptions failed", err)
			httpx.Error(w, "trim push subscriptions failed", http.StatusInternalServerError)
			return
		}
	}
	ua := r.Header.Get("User-Agent")
	if len(ua) > 255 {
		ua = ua[:255]
	}
	if err := p.Subs.PutWebPushSubscription(r.Context(), store.WebPushSubscription{
		ID: id, UserID: user, Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth, UserAgent: ua,
		CreatedAtMs: now, UpdatedAtMs: now,
	}); err != nil {
		obs.ServerError(w, "save push subscription failed", err)
		httpx.Error(w, "save push subscription failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *WebPush) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	user, ok := caller(w, r)
	if !ok {
		return
	}
	var in wire.WebPushSubscriptionInput
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &in) {
		return
	}
	endpoint := strings.TrimSpace(in.Endpoint)
	if endpoint == "" {
		httpx.Error(w, "endpoint required", http.StatusBadRequest)
		return
	}
	if p.Subs == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := p.Subs.DeleteWebPushSubscription(r.Context(), user, subscriptionID(endpoint)); err != nil {
		obs.ServerError(w, "delete push subscription failed", err)
		httpx.Error(w, "delete push subscription failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validSubscriptionKeys checks the browser's p256dh (a P-256 point) and auth
// secret (16 bytes) so a malformed subscription is refused at subscribe time,
// not on every delivery.
func validSubscriptionKeys(k wire.WebPushSubscriptionKeys) error {
	pub, err := decodeB64URL(k.P256dh)
	if err != nil {
		return errors.New("keys.p256dh must be base64url")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return errors.New("keys.p256dh is not a P-256 public key")
	}
	auth, err := decodeB64URL(k.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("keys.auth must be 16 bytes, base64url")
	}
	return nil
}

// sendPush delivers one WebPushChannel outbox item.
func (w *Worker) sendPush(ctx context.Context, it store.NotificationOutboxItem) (store.NotificationOutcome, string, []byte) {
	if !w.Push.Enabled() {
		return store.NotificationFailed, "browser notifications are not configured on this deployment", nil
	}
	var p wire.NotificationPayload
	if err := json.Unmarshal(it.EventJSON, &p); err != nil {
		return store.NotificationFailed, "undecodable outbox item: " + err.Error(), nil
	}
	return conclude(p, DeliveryResult{Results: w.Push.Deliver(ctx, p)})
}
