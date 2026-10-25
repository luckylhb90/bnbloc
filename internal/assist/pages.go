package assist

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/extgov"
)

// The pages context (wire.AssistContextPages): public web pages a request
// names, fetched here and given to the model as text. Only https, no
// redirect off the page's host, bounded in time and size. Where pages are
// fetched from is the deployment's (PagesClient): by default any host on a
// public address (resolved and dialled by address: no private, loopback,
// link-local or metadata one); a deployment whose egress is a DNS
// allowlist names its hosts instead, so an unlisted name is never looked
// up. A page that cannot be read is a line saying so, never a failed run.

// Page is one fetched page: its address and its text (markup removed), or
// why it could not be read.
type Page struct {
	URL   string
	Text  string
	Error string
}

// Bounds of the pages context.
const (
	pageTimeout  = 10 * time.Second
	maxPageBytes = 2 << 20
	maxPagesText = 20000
)

var (
	errNotPublic = errors.New("not a public address")
	errNotListed = errors.New("not a host this deployment reads pages from")
)

// publicControl refuses a connection to anything but a public address.
func publicControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return errNotPublic
	}
	a = a.Unmap()
	if extgov.CheckAddr(a) != nil || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(a) {
		return errNotPublic
	}
	return nil
}

// listedControl refuses only what no fetch may reach (extgov.CheckAddr):
// a listed host may sit behind the deployment's private egress proxy.
func listedControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil || extgov.CheckAddr(a) != nil {
		return errNotPublic
	}
	return nil
}

// listedTransport sends only to the listed hosts, before any lookup.
type listedTransport struct {
	hosts map[string]bool
	base  http.RoundTripper
}

func (t listedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !t.hosts[strings.ToLower(r.URL.Hostname())] {
		return nil, errNotListed
	}
	return t.base.RoundTrip(r)
}

// PagesClient is the fetcher's client, redirects kept on the page's host:
// with no hosts, any public address; with hosts (the deployment's list),
// only those names, on any address extgov allows.
func PagesClient(hosts []string) *http.Client {
	control := publicControl
	if len(hosts) > 0 {
		control = listedControl
	}
	d := &net.Dialer{Timeout: pageTimeout, Control: control}
	var rt http.RoundTripper = &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: pageTimeout, ResponseHeaderTimeout: pageTimeout}
	if len(hosts) > 0 {
		listed := map[string]bool{}
		for _, h := range hosts {
			listed[strings.ToLower(strings.TrimSpace(h))] = true
		}
		rt = listedTransport{hosts: listed, base: rt}
	}
	return &http.Client{
		Timeout:   pageTimeout,
		Transport: rt,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 3 || r.URL.Scheme != "https" || !strings.EqualFold(r.URL.Hostname(), via[0].URL.Hostname()) {
				return errors.New("a redirect off the page")
			}
			return nil
		},
	}
}

// FetchPages reads each https URL (at most wire.MaxAssistURLs, checked by
// the caller) with c, as text, within maxPagesText in all.
func FetchPages(ctx context.Context, c *http.Client, urls []string) []Page {
	var out []Page
	left := maxPagesText
	for _, raw := range urls {
		p := Page{URL: raw}
		u, err := url.Parse(strings.TrimSpace(raw))
		switch {
		case err != nil || u.Scheme != "https" || u.Host == "":
			p.Error = "not an https address"
		case left <= 0:
			p.Error = "left out: the pages' text is full"
		default:
			text, err := fetchText(ctx, c, u.String())
			if err != nil {
				p.Error = err.Error()
			} else {
				p.Text = cut(text, left)
				left -= utf8.RuneCountInString(p.Text)
			}
		}
		out = append(out, p)
	}
	return out
}

func fetchText(ctx context.Context, c *http.Client, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html,text/plain,text/markdown,application/json;q=0.9,*/*;q=0.5")
	resp, err := c.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, errNotPublic):
			return "", errNotPublic
		case errors.Is(err, errNotListed):
			return "", errNotListed
		}
		return "", errors.New("could not be fetched")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("answered %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", errors.New("could not be read")
	}
	if !utf8.Valid(raw) {
		return "", errors.New("not text")
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") {
		return htmlText(string(raw)), nil
	}
	return strings.TrimSpace(string(raw)), nil
}

var (
	dropREs = func() []*regexp.Regexp {
		var out []*regexp.Regexp
		for _, tag := range []string{"script", "style", "noscript", "svg", "nav", "footer", "header"} {
			out = append(out, regexp.MustCompile(`(?is)<`+tag+`\b.*?</\s*`+tag+`\s*>`))
		}
		return out
	}()
	breakRE = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/h[1-6]|/tr|/pre|/table|li|h[1-6])\b[^>]*>`)
	tagRE   = regexp.MustCompile(`(?s)<[^>]*>`)
	blankRE = regexp.MustCompile(`\n\s*\n+`)
	spaceRE = regexp.MustCompile(`[ \t]+`)
)

// htmlText is a page's readable text: scripts, styles and navigation left
// out, blocks on lines of their own.
func htmlText(s string) string {
	for _, re := range dropREs {
		s = re.ReplaceAllString(s, " ")
	}
	s = breakRE.ReplaceAllString(s, "\n")
	s = html.UnescapeString(tagRE.ReplaceAllString(s, " "))
	s = spaceRE.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(blankRE.ReplaceAllString(strings.Join(lines, "\n"), "\n"))
}

func pages(b *strings.Builder, ps []Page) {
	b.WriteString("\n<pages>\n")
	for _, p := range ps {
		fmt.Fprintf(b, "<page url=\"%s\">\n", fence(cut(p.URL, 300)))
		if p.Error != "" {
			fmt.Fprintf(b, "(not read: %s)\n", p.Error)
		} else {
			b.WriteString(fence(p.Text))
			b.WriteString("\n")
		}
		b.WriteString("</page>\n")
	}
	b.WriteString("</pages>\n")
}
