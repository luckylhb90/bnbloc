package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Sending a room an event from a script (docs/prd/2026-10-21-room-events.md
// §2.2): bnbloc events send <room> <type> [--subject s] [--data json|@file],
// as the signed-in member or BNBLOC_API_TOKEN's subject, through
// POST /v1/rooms/{room}/events. The room's tasks whose trigger takes it
// run; the command prints how many.

func runEvents(args []string) error { return eventsCmd(args, os.Stdout) }

func eventsCmd(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "send" {
		return errors.New("events: want send <room> <type> [--subject s] [--data json|@file]")
	}
	fs := flag.NewFlagSet("events send", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	subject := fs.String("subject", "", "the event's subject (what it is about, e.g. a branch or a version)")
	data := fs.String("data", "", "the event's data: JSON, or @file to read it from a file")
	if err := fs.Parse(interleaved(fs, args[1:])); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("events send: want a room id and an event type")
	}
	in := wire.MemberEvent{Type: fs.Arg(1), Subject: *subject}
	if *data != "" {
		raw := []byte(*data)
		if name, ok := strings.CutPrefix(*data, "@"); ok {
			var err error
			if raw, err = os.ReadFile(name); err != nil {
				return fmt.Errorf("events send: read --data: %w", err)
			}
		}
		if !json.Valid(raw) {
			return errors.New("events send: --data is not JSON")
		}
		in.Data = raw
	}
	var reply wire.MemberEventReply
	path := "/v1/rooms/" + url.PathEscape(fs.Arg(0)) + "/events"
	if err := gw.client().call(context.Background(), apiTimeout, http.MethodPost, path, in, &reply); err != nil {
		return err
	}
	fmt.Fprintf(out, "sent %s (%s): %d task(s) set to run", reply.Event.Type, reply.Event.ID, reply.Triggered)
	if reply.Dropped > 0 {
		fmt.Fprintf(out, ", %d dropped over a rate limit", reply.Dropped)
	}
	fmt.Fprintln(out)
	return nil
}

// interleaved moves flags after the positional arguments in front of them,
// so "send <room> <type> --subject s" parses like "send --subject s <room>
// <type>".
func interleaved(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil && i+1 < len(args) {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
				flags = append(flags, args[i+1])
				i++
			}
		}
	}
	return append(flags, pos...)
}
