// Command bnbloc-relay runs the byte-stream relay service.
package main

import (
	"context"
	"log"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/services/relay"
)

func main() {
	cfg := config.FromEnv("BNBLOC_RELAY", ":8085")
	svc := relay.New()
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
