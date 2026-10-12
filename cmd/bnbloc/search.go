package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// `bnbloc search reindex` brings the message search index up to date with
// the room feed: it calls POST /v1/platform/search/reindex (super-admin)
// until the whole feed is walked. On the MySQL backend that fills the search
// text of rows stored without it; on OpenSearch it writes every message's
// current document (backfill of a new index, repair after lost updates).
// Each call is bounded server-side; the cursor it returns is printed so an
// interrupted run resumes with --cursor.
//
// `bnbloc search status` prints GET /v1/platform/search/status: the backend,
// whether it is ready, and (OpenSearch) the index health, document count and
// the answering realtime replica's update queue. It exits non-zero when the
// backend is not ready.

// reindexCallTimeout bounds one reindex call (the server caps its own work
// at ~10s; the margin covers a slow database round).
const reindexCallTimeout = 2 * time.Minute

const searchUsage = "usage: bnbloc search reindex [--cursor <c>] [--batches n] | bnbloc search status"

func runSearch(args []string) error {
	if len(args) == 0 {
		return errors.New(searchUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("search "+sub, flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	var cursor *string
	var batches *int
	switch sub {
	case "reindex":
		cursor = fs.String("cursor", "", "resume after this cursor (printed by an earlier run)")
		batches = fs.Int("batches", 0, "batches of 500 rows per call (default: the server's, at most 50)")
	case "status":
	default:
		return errors.New(searchUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("search %s: unexpected argument %q", sub, fs.Arg(0))
	}
	c := gw.client()
	if sub == "status" {
		return searchStatus(context.Background(), c, os.Stdout)
	}
	return searchReindex(context.Background(), c, *cursor, *batches, os.Stdout)
}

// searchReindex loops until the server reports done, printing one progress
// line per call. On an error it names the cursor to resume from.
func searchReindex(ctx context.Context, c *client, cursor string, batches int, out io.Writer) error {
	var scanned, updated int
	backend := ""
	for {
		var res wire.SearchReindexResponse
		err := c.call(ctx, reindexCallTimeout, http.MethodPost, "/v1/platform/search/reindex",
			wire.SearchReindexRequest{Cursor: cursor, MaxBatches: batches}, &res)
		if err != nil {
			if cursor != "" {
				return fmt.Errorf("search reindex: %w (resume with --cursor %q)", err, cursor)
			}
			return fmt.Errorf("search reindex: %w", err)
		}
		if backend == "" {
			backend = res.Backend
			fmt.Fprintf(out, "backend: %s\n", backend)
		}
		scanned += res.Scanned
		updated += res.Updated
		cursor = res.Cursor
		fmt.Fprintf(out, "scanned %d, indexed %d (total %d / %d), cursor %q\n", res.Scanned, res.Updated, scanned, updated, cursor)
		if res.Done {
			fmt.Fprintf(out, "done: scanned %d messages, indexed %d\n", scanned, updated)
			return nil
		}
	}
}

// searchStatus prints the backend's status, failing when it is not ready.
func searchStatus(ctx context.Context, c *client, out io.Writer) error {
	var st wire.SearchStatus
	if err := c.call(ctx, 30*time.Second, http.MethodGet, "/v1/platform/search/status", nil, &st); err != nil {
		return fmt.Errorf("search status: %w", err)
	}
	fmt.Fprintf(out, "backend:   %s\n", st.Backend)
	if st.Index != "" {
		fmt.Fprintf(out, "index:     %s\n", st.Index)
	}
	fmt.Fprintf(out, "ready:     %v\n", st.Ready)
	if st.Health != "" {
		fmt.Fprintf(out, "health:    %s\n", st.Health)
	}
	if st.Documents != nil {
		fmt.Fprintf(out, "documents: %d\n", *st.Documents)
	}
	if u := st.Updates; u != nil {
		fmt.Fprintf(out, "updates:   queued %d, applied %d, failed %d, dropped %d (this realtime replica since start)\n", u.Queued, u.Applied, u.Failed, u.Dropped)
		if u.Failed > 0 || u.Dropped > 0 {
			fmt.Fprintln(out, "           failed or dropped updates leave the index behind: run `bnbloc search reindex`")
		}
	}
	if st.Error != "" {
		fmt.Fprintf(out, "error:     %s\n", st.Error)
	}
	if !st.Ready {
		return errors.New("search status: backend not ready")
	}
	return nil
}
