// Command bnbloc-loadtest drives a running gateway with simulated members:
// each member signs in, holds the room SSE stream the web client holds, and
// sends chat at a configured total rate. Every message carries a clientMsgId,
// so the tool measures send -> delivery latency on every receiving stream.
// See docs/load-testing.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bnbloc-loadtest:", err)
		os.Exit(2)
	}
	// The first SIGINT/SIGTERM ends the send window: the run skips the drain,
	// reports, and still deletes the rooms it created. A second one exits now.
	ctx, stop := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "bnbloc-loadtest: interrupted; reporting and cleaning up (interrupt again to exit now)")
		stop()
		<-sig
		os.Exit(130)
	}()
	report, err := run(ctx, cfg, os.Stderr)
	if report != nil {
		if cfg.JSON {
			_ = report.writeJSON(os.Stdout)
		} else {
			report.writeText(os.Stdout)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bnbloc-loadtest:", err)
		os.Exit(1)
	}
}

// config is one run's parameters.
type config struct {
	Gateway        string
	Members        int
	MemberPrefix   string
	TokensFile     string
	ForwardedFor   bool
	TZ             string
	LoginParallel  int
	Rooms          int
	RoomIDs        []string
	MembersPerRoom int
	Channels       int
	Rate           float64
	Duration       time.Duration
	RampUp         time.Duration
	Settle         time.Duration
	Drain          time.Duration
	TextBytes      int
	MaxInflight    int
	Timeout        time.Duration
	StreamIdle     time.Duration
	Cleanup        bool
	JSON           bool
	Progress       time.Duration
}

func parseFlags(args []string, errOut io.Writer) (config, error) {
	var c config
	fs := flag.NewFlagSet("bnbloc-loadtest", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(errOut, "usage: bnbloc-loadtest --gateway URL [flags]")
		fmt.Fprintln(errOut, "")
		fmt.Fprintln(errOut, "Simulated members hold the web client's room SSE stream and send chat;")
		fmt.Fprintln(errOut, "reports send -> delivery latency, throughput, errors and stream drops.")
		fmt.Fprintln(errOut, "")
		fs.PrintDefaults()
	}
	fs.StringVar(&c.Gateway, "gateway", "", "gateway base URL (required), e.g. http://localhost:7090")
	fs.IntVar(&c.Members, "members", 200, "simulated members, signed in through the dev sign-in (ignored with --tokens-file)")
	fs.StringVar(&c.MemberPrefix, "member-prefix", "loadtest", "dev sign-in subjects are <prefix>-0001, <prefix>-0002, ...")
	fs.StringVar(&c.TokensFile, "tokens-file", "", "file with one session token per line; each line is one member (instead of the dev sign-in)")
	fs.StringVar(&c.TZ, "tz", "", "comma-separated IANA time zones, given to members round-robin on their room streams (the web client's tz parameter, sent only when the organization has member locations on); empty = no time zone")
	fs.BoolVar(&c.ForwardedFor, "forwarded-for", false, "send a distinct synthetic X-Forwarded-For per member on sign-in, so the gateway's per-IP login throttle does not bind; effective only when the gateway trusts this host as a proxy (BNBLOC_TRUSTED_PROXIES)")
	fs.IntVar(&c.LoginParallel, "login-parallel", 16, "concurrent sign-in and setup requests")
	fs.IntVar(&c.Rooms, "rooms", 20, "rooms the tool creates (ignored with --room-ids)")
	roomIDs := fs.String("room-ids", "", "comma-separated existing room ids to use instead of creating rooms; every assigned member must already be a member")
	fs.IntVar(&c.MembersPerRoom, "members-per-room", 0, "members in each room (default members/rooms); above that a member holds one stream per room it is in")
	fs.IntVar(&c.Channels, "channels", 1, "channels per room (the default plus channels the tool creates, or reuses in --room-ids rooms); members are spread over them round-robin, each holding a web tab's member stream showing its room with that channel open, and posting in it")
	fs.Float64Var(&c.Rate, "rate", 50, "total chat sends per second, spread round-robin over every (room, member) pair")
	fs.DurationVar(&c.Duration, "duration", 60*time.Second, "send window")
	fs.DurationVar(&c.RampUp, "ramp-up", 10*time.Second, "streams open evenly over this period before sending starts")
	fs.DurationVar(&c.Settle, "settle", 10*time.Second, "after the ramp-up, wait up to this long for every stream to be open")
	fs.DurationVar(&c.Drain, "drain", 5*time.Second, "after the send window, wait up to this long for outstanding deliveries")
	fs.IntVar(&c.TextBytes, "text-bytes", 64, "chat text size in bytes")
	fs.IntVar(&c.MaxInflight, "max-inflight", 512, "outstanding sends; a send due while this many are in flight is skipped and counted")
	fs.DurationVar(&c.Timeout, "timeout", 15*time.Second, "per-request timeout for sign-in, setup and sends")
	fs.DurationVar(&c.StreamIdle, "stream-idle", 45*time.Second, "reopen a stream that delivered no bytes for this long (the server sends a keep-alive every 15s); 0 disables")
	fs.BoolVar(&c.Cleanup, "cleanup", true, "delete the rooms the tool created at the end")
	fs.BoolVar(&c.JSON, "json", false, "print the report as JSON instead of text")
	fs.DurationVar(&c.Progress, "progress", 5*time.Second, "progress line interval on stderr; 0 disables")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	c.Gateway = strings.TrimRight(strings.TrimSpace(c.Gateway), "/")
	if c.Gateway == "" {
		return c, errors.New("--gateway is required")
	}
	for _, id := range strings.Split(*roomIDs, ",") {
		if id = strings.TrimSpace(id); id != "" {
			c.RoomIDs = append(c.RoomIDs, id)
		}
	}
	if len(c.RoomIDs) > 0 {
		c.Rooms = len(c.RoomIDs)
	}
	switch {
	case c.TokensFile == "" && c.Members < 1:
		return c, errors.New("--members must be at least 1")
	case c.Rooms < 1:
		return c, errors.New("--rooms must be at least 1")
	case c.Channels < 1 || c.Channels > 64:
		return c, errors.New("--channels must be between 1 and 64")
	case c.Rate <= 0:
		return c, errors.New("--rate must be positive")
	case c.Duration <= 0:
		return c, errors.New("--duration must be positive")
	case c.MembersPerRoom < 0, c.RampUp < 0, c.Settle < 0, c.Drain < 0, c.StreamIdle < 0:
		return c, errors.New("negative sizes and durations are not allowed")
	case c.TextBytes < 1:
		return c, errors.New("--text-bytes must be at least 1")
	case c.MaxInflight < 1 || c.LoginParallel < 1:
		return c, errors.New("--max-inflight and --login-parallel must be at least 1")
	}
	return c, nil
}
