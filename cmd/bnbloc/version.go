package main

import (
	"context"
	"encoding/json"
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
	gateway := fs.String("gateway", "", "gateway base URL")
	token := fs.String("token", "", "session token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("version: unexpected argument %q", fs.Arg(0))
	}
	self := buildinfo.Get("bnbloc-cli")
	fmt.Printf("cli commit %s\n", orUnknown(self.Commit))
	c := newClient(resolveConfig(*gateway, *token))
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
	token, err := c.bearer()
	if err != nil {
		return merged, err
	}
	for i := 0; i < samples; i++ {
		callCtx, cancel := context.WithTimeout(ctx, apiTimeout)
		raw, status, err := c.send(callCtx, http.MethodGet, "/v1/platform/versions", nil, "", token)
		cancel()
		switch {
		case err != nil:
			return merged, fmt.Errorf("GET /v1/platform/versions: %w", err)
		case status == http.StatusUnauthorized && token == "":
			return merged, errNotSignedIn(c.cfg.Gateway, "GET /v1/platform/versions: not signed in")
		case status == http.StatusForbidden:
			return merged, errNotSuperAdmin
		case status != http.StatusOK:
			return merged, fmt.Errorf("GET /v1/platform/versions: %d %s: %s", status, http.StatusText(status), strings.TrimSpace(string(raw)))
		}
		var d wire.DeploymentVersions
		if err := json.Unmarshal(raw, &d); err != nil {
			return merged, fmt.Errorf("GET /v1/platform/versions: invalid response: %w", err)
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
