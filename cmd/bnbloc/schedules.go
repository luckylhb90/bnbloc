package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's scheduled tasks from a script (docs/prd/2026-10-20-event-
// triggered-schedules.md §2.3): list them, and run one now through the
// room's own "Run now" route, as the signed-in subject (with
// BNBLOC_API_TOKEN, the token's subject), which must be the task's
// creator. A CI job or a release script runs "the room's tests" after its
// publish this way.

// scheduleRunTimeout bounds one run: an AI task runs to its end before the
// route answers (realtime gives the gateway's run ten minutes).
const scheduleRunTimeout = 11 * time.Minute

func runSchedules(args []string) error { return schedulesCmd(args, os.Stdout) }

func schedulesCmd(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("schedules: want list <room> | run [--dry] <room> <schedule>")
	}
	fs := flag.NewFlagSet("schedules "+args[0], flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	dry := fs.Bool("dry", false, "run: an AI task reads for real and sends nothing, reporting what it would do")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c := gw.client()
	switch args[0] {
	case "list":
		if fs.NArg() != 1 {
			return errors.New("schedules list: want exactly one room id")
		}
		return listSchedules(c, fs.Arg(0), out)
	case "run":
		if fs.NArg() != 2 {
			return errors.New("schedules run: want a room id and a schedule id")
		}
		return runSchedule(c, fs.Arg(0), fs.Arg(1), *dry, out)
	}
	return fmt.Errorf("schedules: unknown subcommand %q (list, run)", args[0])
}

func schedulesPath(room string) string {
	return "/v1/rooms/" + url.PathEscape(room) + "/schedules"
}

func listSchedules(c *client, room string, out io.Writer) error {
	var reply struct {
		Schedules []wire.RoomSchedule `json:"schedules"`
	}
	if err := c.call(context.Background(), apiTimeout, http.MethodGet, schedulesPath(room), nil, &reply); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tBY\tTO\tWHEN\tSTATE\tNEXT\tLAST")
	for _, sc := range reply.Schedules {
		state := "on"
		if !sc.Enabled {
			state = "paused"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", sc.ScheduleID, sc.CreatedBy, sc.Recipient.Kind, scheduleWhen(sc), state, msTime(sc.NextRunAtMs), sc.LastStatus)
	}
	return tw.Flush()
}

// scheduleWhen is a recurrence in a word or two (the web and room_setup
// render it for people; the list only tells schedules apart).
func scheduleWhen(sc wire.RoomSchedule) string {
	r := sc.Recurrence
	switch r.Kind {
	case wire.RecurOnce:
		return "once " + msTime(r.AtMs)
	case wire.RecurDaily:
		return "daily " + r.Time + " " + sc.Timezone
	case wire.RecurWeekly:
		return fmt.Sprintf("weekly %v %s %s", r.Days, r.Time, sc.Timezone)
	case wire.RecurCron:
		return "cron " + r.Cron + " " + sc.Timezone
	case wire.RecurEvent:
		on := "on " + strings.Join(r.Events, ",")
		if r.Component != "" {
			on += " (" + r.Component + ")"
		}
		return on
	}
	return r.Kind
}

// runSchedule runs a schedule now. A message (to the room or an agent)
// answers its sequence; an AI task or a notice answers what it did, and a
// run that failed fails the command.
func runSchedule(c *client, room, id string, dry bool, out io.Writer) error {
	path := schedulesPath(room) + "/" + url.PathEscape(id) + "/run"
	if dry {
		path += "?dry=1"
	}
	var reply struct {
		Sequence *int64 `json:"sequence"`
		wire.ScheduleRunReply
	}
	if err := c.call(context.Background(), scheduleRunTimeout, http.MethodPost, path, nil, &reply); err != nil {
		return err
	}
	if reply.Sequence != nil {
		fmt.Fprintf(out, "ran %s: posted message #%d\n", id, *reply.Sequence)
		return nil
	}
	fmt.Fprintf(out, "ran %s: %s", id, reply.Status)
	if reply.Summary != "" {
		fmt.Fprintf(out, ": %s", reply.Summary)
	}
	fmt.Fprintln(out)
	for _, a := range reply.Actions {
		mark := ""
		if a.DryRun {
			mark = " (dry run)"
		}
		fmt.Fprintf(out, "  %s: %s%s\n", a.Name, a.Detail, mark)
	}
	if reply.Status == wire.ScheduleRunError {
		return fmt.Errorf("schedule %s failed: %s", id, reply.Error)
	}
	return nil
}
