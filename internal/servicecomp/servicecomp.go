// Package servicecomp is the service component kind: an endpoint component
// (internal/endpointpkg) that acts for members and their agents' turns
// through the primitives it declares (component.ServiceCapabilities,
// docs/architecture/service-components.md). It owns the one way the
// platform calls a service: Forward, signed over component.SignedRequest
// with the component's credential, held to its permissions.network, no
// redirects followed, bodies bounded.
package servicecomp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/endpointpkg"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

// callTimeout bounds one forwarded call: a service answers an agent's
// request to another system inside it.
const callTimeout = 90 * time.Second

// MaxBody bounds a forwarded request's and answer's body.
const MaxBody = component.MaxEgressBody + 1<<20

// Kind is the service's endpoint-component description.
var Kind = endpointpkg.Kind{
	Kind:         extreg.KindService,
	PackageKind:  pkgmeta.KindServicePackageV1,
	Label:        "service",
	Contract:     pkgmeta.ServiceContractV1,
	DiscoverPath: component.ServiceDiscoverPath,
	Timeout:      callTimeout,
}

// Installer is the kind's extpipe lifecycle: the shared endpoint install
// path (validate, network guard, discover probe).
func Installer() extpipe.KindInstaller { return Kind.Installer() }

// Capabilities are the primitives a service record declared.
func Capabilities(rec extpipe.Record) []string {
	cfg, err := extpipe.DecodeConfig(rec)
	if err != nil {
		return nil
	}
	return cfg.Capabilities
}

// Declares reports whether the record declared the primitive.
func Declares(rec extpipe.Record, capability string) bool {
	return slices.Contains(Capabilities(rec), capability)
}

// Call is one request the platform forwards to a service: Path is the
// component's path with its query; IdentityHeader/Identity the attested
// identity it carries (component.TurnHeader, MemberHeader or
// CallbackHeader, "" for none).
type Call struct {
	Method         string
	Path           string
	IdentityHeader string
	Identity       string
	ContentType    string
	Accept         string
	Body           []byte
}

// Answer is a service's answer: its status, the headers that may pass on
// (passedHeaders), its body and the audit row it asked for.
type Answer struct {
	Status int
	Header http.Header
	Body   []byte
	Audit  *component.Audit
}

// passedHeaders are the answer headers that reach the caller; a cookie
// never does (a service's page shares the platform's origin).
var passedHeaders = []string{"Content-Type", "Content-Disposition", "Cache-Control", "Location", "Etag", "Last-Modified"}

// ErrUnreachable is a transport failure: the service did not answer.
var ErrUnreachable = errors.New("the service is unreachable")

// Forward sends one signed call to the service and reads its answer.
func Forward(ctx context.Context, ep endpointpkg.Endpoint, secret string, c Call, now time.Time) (Answer, error) {
	if !strings.HasPrefix(c.Path, "/") {
		return Answer{}, errors.New("servicecomp: path must start with /")
	}
	if len(c.Body) > MaxBody {
		return Answer{}, fmt.Errorf("the request body is larger than %d bytes", MaxBody)
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, ep.BaseURL+c.Path, bytes.NewReader(c.Body))
	if err != nil {
		return Answer{}, errors.New("build the service request failed")
	}
	if c.ContentType != "" {
		req.Header.Set("Content-Type", c.ContentType)
	}
	if c.Accept != "" {
		req.Header.Set("Accept", c.Accept)
	}
	if c.IdentityHeader != "" {
		req.Header.Set(c.IdentityHeader, c.Identity)
	}
	req.Header.Set("User-Agent", "bnbloc-service/1")
	req.Header.Set(component.SignatureHeader, component.Sign(secret, now, component.SignedRequest(c.Method, c.Path, c.Identity, c.Body)))
	client := http.DefaultClient
	if ep.Client != nil {
		client = ep.Client
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		// The transport error names the URL, never the body: keep the class.
		return Answer{}, ErrUnreachable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return Answer{}, ErrUnreachable
	}
	if len(body) > MaxBody {
		return Answer{}, fmt.Errorf("the service's answer is larger than %d bytes", MaxBody)
	}
	out := Answer{Status: resp.StatusCode, Header: http.Header{}, Body: body}
	for _, h := range passedHeaders {
		if v := resp.Header.Get(h); v != "" {
			out.Header.Set(h, v)
		}
	}
	if raw := resp.Header.Get(component.AuditHeader); raw != "" {
		var a component.Audit
		if component.DecodeHeader(raw, &a) == nil && strings.TrimSpace(a.Action) != "" {
			a.Action, a.Target, a.Outcome, a.Detail = cut(a.Action, 64), cut(a.Target, 256), cut(a.Outcome, 32), cut(a.Detail, 512)
			out.Audit = &a
		}
	}
	return out, nil
}

func cut(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
