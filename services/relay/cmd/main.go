// Command bnbloc-relay runs the ICE-config, rendezvous, and byte-stream relay
// service.
package main

import (
	"log"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/services/relay"
)

func main() {
	cfg := config.FromEnv("BNBLOC_RELAY", ":8085")
	ice := relay.ICEConfig{
		StunServers:    splitEnv("BNBLOC_RELAY_STUN_SERVERS"),
		RelayEndpoints: splitEnv("BNBLOC_RELAY_ENDPOINTS"),
	}
	svc := relay.New(ice)
	if err := obs.Serve("bnbloc-relay", cfg.ListenAddr, svc.Handler()); err != nil {
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
