// Command bnbloc-pulse runs the organization board's backend: it consumes the
// Pulse stream into live state and minute rollups, and serves the board's
// snapshot (docs/prd/2026-10-24-executive-pulse-backend.md).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	pulsesvc "github.com/luckylhb90/bnbloc/services/pulse"
)

func main() {
	cfg := config.FromEnv("BNBLOC_PULSE", ":8090")
	backends, err := store.NewBackends(config.FromEnvRaw("BNBLOC_MYSQL_DSN"))
	if err != nil {
		log.Fatalf("bnbloc-pulse: store: %v", err)
	}
	if backends.PulseMinutes == nil {
		log.Fatalf("bnbloc-pulse: needs BNBLOC_MYSQL_DSN: the board's rollups are stored in MySQL")
	}
	// SIGTERM ends the consume loop, which flushes and acks once more before exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics := obs.NewMetrics("bnbloc-pulse")
	health := backends.HealthCheck()
	var handler http.Handler = pulsesvc.NotConfigured()

	// Without a Redis the board has nothing to read. The process stays up (an
	// all-in-one container supervises it with the other services) and answers
	// the board routes 503. A malformed URL is still fatal: that is a mistake to
	// fix, not an absence to tolerate.
	if url := strings.TrimSpace(config.FromEnvRaw(pulse.EnvURL)); url == "" {
		log.Printf("bnbloc-pulse: WARNING %s is unset: the organization board is not configured; consuming nothing, answering 503", pulse.EnvURL)
	} else {
		opts, err := redis.ParseURL(url)
		if err != nil {
			log.Fatalf("bnbloc-pulse: %s: %v", pulse.EnvURL, err)
		}
		rdb := redis.NewClient(opts)
		consumer := pulsesvc.NewConsumer(rdb, backends.PulseMinutes, pulsesvc.ConsumerConfig{
			Name:       config.FromEnvRaw("BNBLOC_PULSE_CONSUMER"),
			FlushEvery: config.Duration("BNBLOC_PULSE_FLUSH", 15*time.Second),
			Retention:  config.Duration("BNBLOC_PULSE_RETENTION", 90*24*time.Hour),
		})
		go func() {
			if err := consumer.Run(ctx); err != nil {
				log.Fatalf("bnbloc-pulse: consumer: %v", err)
			}
		}()
		go pulsesvc.RunMetrics(ctx, metrics, consumer, 15*time.Second)

		handler = (&pulsesvc.Server{
			State: pulsesvc.NewLive(rdb),
			Rows:  backends.PulseMinutes,
			Now:   time.Now,
		}).Handler()
		health = func() error {
			if err := rdb.Ping(ctx).Err(); err != nil {
				return err
			}
			return backends.HealthCheck()()
		}
	}

	if err := obs.ServeWithHealth("bnbloc-pulse", cfg.ListenAddr, svcauth.Middleware(svcauth.KeyFromEnv(), handler), health, obs.WithMetrics(metrics)); err != nil {
		log.Fatalf("bnbloc-pulse: %v", err)
	}
}
