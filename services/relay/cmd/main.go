// Command bnbloc-relay runs the ICE-config, rendezvous, and byte-stream relay
// service.
package main

import (
	"context"
	"log"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/services/relay"
)

func main() {
	cfg := config.FromEnv("BNBLOC_RELAY", ":8085")
	ice := relay.ICEConfig{
		StunServers:    splitEnv("BNBLOC_RELAY_STUN_SERVERS"),
		RelayEndpoints: splitEnv("BNBLOC_RELAY_ENDPOINTS"),
	}
	svc := relay.New(ice)
	// Scale-out (BNBLOC_RELAY_PEERS + _SELF): each session key is owned by one
	// replica; unset is a single node that pairs every key itself.
	peers, err := shard.FromEnv("BNBLOC_RELAY")
	if err != nil {
		log.Fatalf("bnbloc-relay: %v", err)
	}
	metrics := obs.NewMetrics("bnbloc-relay")
	peers.Instrument(metrics, "bnbloc-relay")
	peers.CheckSelfWith(func() error { return nil }) // no dependencies: serving = ready
	svc.Peers = peers
	handler := svc.Handler()
	peers.Start(context.Background())
	if err := obs.ServeWithMetrics("bnbloc-relay", cfg.ListenAddr, handler, metrics); err != nil {
		log.Fatalf("bnbloc-relay: %v", err)
	}
}

// splitEnv reads a comma-separated environment variable into a slice.
func splitEnv(key string) []string {
	v := config.FromEnvRaw(key)
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
