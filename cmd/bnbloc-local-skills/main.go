// Command bnbloc-local-skills is the local-skills plugin: an installable,
// replaceable component (docs/prd/2026-10-16-local-skills.md) that lets a member
// choose which skill folders on their machine reach their agents' sandbox and
// their library. Nothing is shared until the member turns it on.
//
// It runs on the member's desktop as a plugin sidecar with two capabilities:
//
//	skills-provider  serves bnbloc.skills.provider/v1 on loopback behind a
//	                 per-launch bearer; the desktop's Settings → Skills view
//	                 renders it.
//	sandbox-mounts   keeps a read-only mirror of the ticked skills and the
//	                 bnbloc.sandbox.mounts/v1 descriptor the bridge reads before
//	                 every sandboxed turn.
//
// The environment is the one the package's sidecar block names:
//
//	BNBLOC_LOCAL_SKILLS_LISTEN_ADDR  loopback host:port to listen on (what the desktop sets)
//	BNBLOC_LOCAL_SKILLS_TOKEN        the bearer every request must carry
//	BNBLOC_LOCAL_SKILLS_DATA_DIR     private folder for settings
//	BNBLOC_LOCAL_SKILLS_MOUNTS_FILE  where the mount descriptor is written
//	BNBLOC_LOCAL_SKILLS_MOUNT_ROOT   folder (under the workspaces root) for the copies
//	BNBLOC_LOCAL_SKILLS_SUPERVISED   set when the desktop owns stdin (exit on EOF)
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/localskills"
)

// Env names; the package manifest (packages/local-skills) declares the same.
const (
	envListen     = "BNBLOC_LOCAL_SKILLS_LISTEN_ADDR"
	envToken      = "BNBLOC_LOCAL_SKILLS_TOKEN"
	envDataDir    = "BNBLOC_LOCAL_SKILLS_DATA_DIR"
	envMounts     = "BNBLOC_LOCAL_SKILLS_MOUNTS_FILE"
	envMountRoot  = "BNBLOC_LOCAL_SKILLS_MOUNT_ROOT"
	envSupervised = "BNBLOC_LOCAL_SKILLS_SUPERVISED"

	// publishEvery is how often the mirror catches up with edits to a skill.
	publishEvery = 10 * time.Second
	settingsName = "local-skills.json"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("bnbloc-local-skills: %v", err)
	}
}

func run() error {
	listen, err := listenAddr(config.FromEnvRaw(envListen))
	if err != nil {
		return err
	}
	token := config.FromEnvRaw(envToken)
	if token == "" {
		return fmt.Errorf("%s is required: the provider API never answers without a bearer", envToken)
	}
	dataDir := config.FromEnvRaw(envDataDir)
	if dataDir == "" {
		return fmt.Errorf("%s is required", envDataDir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	svc := &localskills.Service{
		Home:         home,
		SettingsFile: filepath.Join(dataDir, settingsName),
		MountsFile:   config.FromEnvRaw(envMounts),
		MirrorRoot:   config.FromEnvRaw(envMountRoot),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if config.FromEnvRaw(envSupervised) != "" {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop() // the desktop is gone: shut down
		}()
	}

	srv := &http.Server{Addr: listen, Handler: svc.Handler(token), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	go svc.Run(publishEvery, ctx.Done(), func(err error) { log.Printf("bnbloc-local-skills: publish: %v", err) })

	log.Printf("bnbloc-local-skills: serving %s on %s", localskills.ProviderContract, listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// listenAddr checks a host:port is loopback (an IP, or localhost) and returns
// it; any other address is refused, since the bearer travels in the clear.
func listenAddr(raw string) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || port == "" {
		return "", fmt.Errorf("%s must be host:port, got %q", envListen, raw)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("%s must be a loopback address, got %q", envListen, host)
	}
	return raw, nil
}
