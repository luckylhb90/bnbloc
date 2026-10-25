// Package egress is the one owner of where the platform's own server-side
// requests may go (wire.ServerEgress, docs/architecture/server-egress.md):
// the policy a platform admin sets, stored as a platform setting, and the
// transport that enforces it. A request to a listed host is dialled on any
// address extgov allows (an external name behind the deployment's private
// egress proxy); any other host, in mode public, only on a public address
// (no private, loopback, shared or metadata one: a link to an internal
// service is never fetched), and in mode allowlist not at all, before any
// lookup.
package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// SettingKey is the platform setting that holds the policy (JSON).
const SettingKey = "server.egress"

var (
	// ErrNotPublic: an unlisted host whose address is not public.
	ErrNotPublic = errors.New("not a public address")
	// ErrNotListed: a host the allowlist does not name.
	ErrNotListed = errors.New("not a host this deployment reaches")
	// ErrInvalid wraps a policy that cannot be saved.
	ErrInvalid = errors.New("invalid server egress")
)

var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// Default is the policy of a deployment that set none.
func Default() wire.ServerEgress {
	return wire.ServerEgress{Mode: wire.ServerEgressPublic, Hosts: []string{}}
}

// Normalize checks p and answers it with its hosts lower-cased, trimmed,
// de-duplicated and sorted.
func Normalize(p wire.ServerEgress) (wire.ServerEgress, error) {
	if p.Mode != wire.ServerEgressPublic && p.Mode != wire.ServerEgressAllowlist {
		return p, fmt.Errorf("%w: mode is public or allowlist", ErrInvalid)
	}
	hosts := []string{}
	for _, h := range p.Hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !hostRE.MatchString(h) {
			return p, fmt.Errorf("%w: %q is not a host name (no scheme, port, path or wildcard)", ErrInvalid, h)
		}
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) > wire.MaxServerEgressHosts {
		return p, fmt.Errorf("%w: at most %d hosts", ErrInvalid, wire.MaxServerEgressHosts)
	}
	slices.Sort(hosts)
	return wire.ServerEgress{Mode: p.Mode, Hosts: hosts, UpdatedAtMs: p.UpdatedAtMs, UpdatedBy: p.UpdatedBy}, nil
}

// Load is the platform's policy (Default when none is set).
func Load(ctx context.Context, settings store.PlatformSettingsStore) (wire.ServerEgress, error) {
	if settings == nil {
		return Default(), nil
	}
	row, ok, err := settings.GetPlatformSetting(ctx, SettingKey)
	if err != nil || !ok {
		return Default(), err
	}
	var p wire.ServerEgress
	if err := json.Unmarshal([]byte(row.Value), &p); err != nil {
		return wire.ServerEgress{}, err
	}
	p.UpdatedAtMs, p.UpdatedBy = row.UpdatedAtMs, row.UpdatedBy
	return Normalize(p)
}

// Save stores p (normalized) as set by actor at nowMs.
func Save(ctx context.Context, settings store.PlatformSettingsStore, p wire.ServerEgress, actor string, nowMs int64) (wire.ServerEgress, error) {
	p, err := Normalize(p)
	if err != nil {
		return p, err
	}
	if settings == nil {
		return p, errors.New("platform settings store not configured")
	}
	p.UpdatedAtMs, p.UpdatedBy = nowMs, actor
	raw, _ := json.Marshal(wire.ServerEgress{Mode: p.Mode, Hosts: p.Hosts})
	return p, settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: SettingKey, Value: string(raw), UpdatedAtMs: nowMs, UpdatedBy: actor})
}

// Transport enforces p on every request (each redirect hop too), dialling
// within dialTimeout. Connections are not kept: a caller builds one per
// use.
func Transport(p wire.ServerEgress, dialTimeout time.Duration) http.RoundTripper {
	build := func(control func(string, string, syscall.RawConn) error) *http.Transport {
		d := &net.Dialer{Timeout: dialTimeout, Control: control}
		return &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: dialTimeout, ResponseHeaderTimeout: dialTimeout, DisableKeepAlives: true}
	}
	return policyTransport{p: p, listed: build(listedControl), public: build(publicControl)}
}

type policyTransport struct {
	p              wire.ServerEgress
	listed, public http.RoundTripper
}

func (t policyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	switch {
	case slices.Contains(t.p.Hosts, strings.ToLower(r.URL.Hostname())):
		return t.listed.RoundTrip(r)
	case t.p.Mode == wire.ServerEgressPublic:
		return t.public.RoundTrip(r)
	}
	return nil, ErrNotListed
}

func dialAddr(address string) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return netip.Addr{}, err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, ErrNotPublic
	}
	return a.Unmap(), nil
}

// publicControl refuses a connection to anything but a public address.
func publicControl(_, address string, _ syscall.RawConn) error {
	a, err := dialAddr(address)
	if err != nil {
		return err
	}
	if extgov.CheckAddr(a) != nil || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(a) {
		return ErrNotPublic
	}
	return nil
}

// listedControl refuses only what no request may reach (extgov.CheckAddr).
func listedControl(_, address string, _ syscall.RawConn) error {
	a, err := dialAddr(address)
	if err != nil {
		return err
	}
	if extgov.CheckAddr(a) != nil {
		return ErrNotPublic
	}
	return nil
}
