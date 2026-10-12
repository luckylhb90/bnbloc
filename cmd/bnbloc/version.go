package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/luckylhb90/bnbloc/internal/buildinfo"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// `bnbloc version` prints the deployment's build identity
// (GET /v1/platform/versions, super-admin) and exits non-zero on skew.
// The route is sampled versionSamples times: each call lands on whichever
// gateway replica the edge picks, and every replica reports itself, so the
// merged view also covers gateway replicas.

const versionSamples = 5

// errVersionSkew is returned when the deployment is not on one commit.
var errVersionSkew = errors.New("deployment versions are not consistent (see SKEW lines)")

// errNotSuperAdmin marks a 403 from the versions route.
var errNotSuperAdmin = errors.New("GET /v1/platform/versions needs a platform super-admin session")

func runVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	gw := addGatewayFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("version: unexpected argument %q", fs.Arg(0))
	}
	self := buildinfo.Get("bnbloc-cli")
	fmt.Printf("cli commit %s\n", orUnknown(self.Commit))
	c := gw.client()
	d, err := fetchDeploymentVersions(context.Background(), c, versionSamples)
	if err != nil {
		return err
	}
	printDeploymentVersions(os.Stdout, c.cfg.Gateway, d)
	if !d.Consistent {
		return errVersionSkew
	}
	return nil
}

// fetchDeploymentVersions merges samples calls of the versions route
// (instances deduplicated) and re-evaluates consistency over the union.
func fetchDeploymentVersions(ctx context.Context, c *client, samples int) (wire.DeploymentVersions, error) {
	var merged wire.DeploymentVersions
	seen := map[string]bool{}
	unreachable := map[string]wire.UnreachableService{}
	for i := 0; i < samples; i++ {
		var d wire.DeploymentVersions
		if err := c.call(ctx, apiTimeout, http.MethodGet, "/v1/platform/versions", nil, &d); err != nil {
			var se *statusError
			if errors.As(err, &se) && se.Status == http.StatusForbidden {
				return merged, errNotSuperAdmin
			}
			return merged, err
		}
		for _, s := range d.Services {
			if key := s.Service + "\x00" + s.Instance; !seen[key] {
				seen[key] = true
				merged.Services = append(merged.Services, s)
			}
		}
		for _, u := range d.Unreachable {
			unreachable[u.Service] = u
		}
		// Every gateway image embeds the bundle it was built with, so a web
		// skew between gateway replicas already shows as gateway skew.
		if merged.Web == nil {
			merged.Web = d.Web
		}
	}
	for _, u := range unreachable {
		merged.Unreachable = append(merged.Unreachable, u)
	}
	buildinfo.Evaluate(&merged)
	return merged, nil
}

func printDeploymentVersions(out io.Writer, gateway string, d wire.DeploymentVersions) {
	fmt.Fprintf(out, "deployment %s\n", gateway)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tINSTANCE\tCOMMIT\tBUILT")
	for _, s := range d.Services {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Service, s.Instance, orUnknown(s.Commit), orUnknown(s.BuiltAt))
	}
	if d.Web != nil {
		fmt.Fprintf(tw, "web bundle\tv%s\t%s\t-\n", d.Web.Version, orUnknown(d.Web.Commit))
	}
	for _, u := range d.Unreachable {
		fmt.Fprintf(tw, "%s\t-\tunreachable\t-\n", u.Service)
	}
	_ = tw.Flush()
	for _, w := range d.Warnings {
		fmt.Fprintf(out, "WARN %s\n", w)
	}
	if d.Consistent {
		if len(d.Warnings) == 0 {
			fmt.Fprintln(out, "consistent: every component runs the same commit")
		} else {
			fmt.Fprintln(out, "no skew among stamped builds; pass CI_COMMIT_SHA to every image build to verify all")
		}
		return
	}
	for _, p := range d.Problems {
		fmt.Fprintf(out, "SKEW %s\n", p)
	}
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// smokeVersions is the smoke's "versions" step: FAIL on skew, a WARN
// (smokeWarning) when the caller is not a platform super-admin.
func smokeVersions(ctx context.Context, c *client, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d, err := fetchDeploymentVersions(ctx, c, versionSamples)
	if errors.Is(err, errNotSuperAdmin) {
		return "", smokeWarning{err: errors.New("not checked: " + err.Error())}
	}
	if err != nil {
		return "", err
	}
	if !d.Consistent {
		return "", errors.New(strings.Join(d.Problems, "; "))
	}
	if len(d.Warnings) > 0 {
		return "", smokeWarning{err: errors.New("unverifiable: " + strings.Join(d.Warnings, "; "))}
	}
	commit := ""
	if len(d.Services) > 0 {
		commit = d.Services[0].Commit
	}
	return fmt.Sprintf("%s on %d instance(s) + web bundle", commit, len(d.Services)), nil
}
