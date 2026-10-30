package main

import (
	"errors"
	"log"
	"net"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/redisbus"
	relay "github.com/luckylhb90/bnbloc/services/relay"
)

func main() {
	cfg := config.FromEnv("BNBLOC_RELAY", ":8085")
	svc := relay.New()
	// More than one replica: a session's two halves may reach different
	// replicas and meet through the replicas' Redis (BNBLOC_REDIS_URL); the
	// second joins the first's replica at its own address. Unset: one
	// replica pairs every session itself.
	bus, err := redisbus.FromEnv(config.FromEnvRaw)
	if err != nil {
		log.Fatalf("bnbloc-relay: %v", err)
	}
	if bus != nil {
		self, err := ownAddr(cfg.ListenAddr)
		if err != nil {
			log.Fatalf("bnbloc-relay: %v", err)
		}
		svc.Meet, svc.Self = bus, self
		log.Printf("bnbloc-relay: replicas meet through %s; this one is %s", redisbus.EnvURL, self)
	}
	metrics := obs.NewMetrics("bnbloc-relay")
	if err := obs.ServeWithMetrics("bnbloc-relay", cfg.ListenAddr, svc.Handler(), metrics); err != nil {
		log.Fatalf("bnbloc-relay: %v", err)
	}
}

// ownAddr is this replica's address as the others reach it: its first
// non-loopback address (the pod IP) and the listen port.
func ownAddr(listen string) (string, error) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
			return net.JoinHostPort(n.IP.String(), port), nil
		}
	}
	return "", errors.New("own address: no non-loopback address")
}
