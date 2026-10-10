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

// `bnbloc search reindex` drives the search backfill (R7): it calls
// POST /v1/platform/search/reindex (super-admin) until the whole room feed is
// walked. Each call is bounded server-side; the cursor it returns is printed
// so an interrupted run resumes with --cursor.

// reindexCallTimeout bounds one reindex call (the server caps its own work
// at ~10s; the margin covers a slow database round).
const reindexCallTimeout = 2 * time.Minute

func runSearch(args []string) error {
	if len(args) == 0 || args[0] != "reindex" {
		return errors.New("usage: bnbloc search reindex [--cursor <c>] [--batches n]")
	}
	fs := flag.NewFlagSet("search reindex", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "gateway base URL")
	token := fs.String("token", "", "session token")
	cursor := fs.String("cursor", "", "resume after this cursor (printed by an earlier run)")
	batches := fs.Int("batches", 0, "batches of 500 rows per call (default: the server's, at most 50)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("search reindex: unexpected argument %q", fs.Arg(0))
	}
	c := newClient(resolveConfig(*gateway, *token))
	return searchReindex(context.Background(), c, *cursor, *batches, os.Stdout)
}

// searchReindex loops until the server reports done, printing one progress
// line per call. On an error it names the cursor to resume from.
func searchReindex(ctx context.Context, c *client, cursor string, batches int, out io.Writer) error {
	var scanned, updated int
	for {
		var res wire.SearchReindexResponse
		err := smokeCall(ctx, c, reindexCallTimeout, http.MethodPost, "/v1/platform/search/reindex",
			wire.SearchReindexRequest{Cursor: cursor, MaxBatches: batches}, &res)
		if err != nil {
			if cursor != "" {
				return fmt.Errorf("search reindex: %w (resume with --cursor %q)", err, cursor)
			}
			return fmt.Errorf("search reindex: %w", err)
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
