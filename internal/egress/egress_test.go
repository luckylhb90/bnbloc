package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A policy is stored normalized and read back; none set is public.
func TestSaveAndLoad(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryPlatformSettings()
	if p, err := Load(ctx, st); err != nil || p.Mode != wire.ServerEgressPublic || len(p.Hosts) != 0 {
		t.Fatalf("default = %+v %v", p, err)
	}
	if _, err := Save(ctx, st, wire.ServerEgress{Mode: wire.ServerEgressPublic, Hosts: []string{"docs.example.com:443"}}, "ops", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a host with a port = %v", err)
	}
	if _, err := Save(ctx, st, wire.ServerEgress{Mode: wire.ServerEgressAllowlist, Hosts: []string{"B.example.com", "a.example.com", "b.example.com"}}, "ops", 7); err != nil {
		t.Fatal(err)
	}
	p, err := Load(ctx, st)
	if err != nil || p.Mode != wire.ServerEgressAllowlist || len(p.Hosts) != 2 || p.Hosts[0] != "a.example.com" || p.UpdatedBy != "ops" || p.UpdatedAtMs != 7 {
		t.Fatalf("loaded = %+v %v", p, err)
	}
}

// The transport: an unlisted host on a private address is refused in mode
// public and never sent in mode allowlist; a listed one is dialled on its
// private address.
func TestTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	get := func(p wire.ServerEgress) error {
		resp, err := (&http.Client{Transport: Transport(p, 5*time.Second)}).Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(Default()); !errors.Is(err, ErrNotPublic) {
		t.Fatalf("public, a loopback address = %v", err)
	}
	if err := get(wire.ServerEgress{Mode: wire.ServerEgressAllowlist, Hosts: []string{"docs.example.com"}}); !errors.Is(err, ErrNotListed) {
		t.Fatalf("allowlist, unlisted = %v", err)
	}
	for _, mode := range []string{wire.ServerEgressPublic, wire.ServerEgressAllowlist} {
		if err := get(wire.ServerEgress{Mode: mode, Hosts: []string{"127.0.0.1"}}); err != nil {
			t.Fatalf("%s, listed = %v", mode, err)
		}
	}
}
