package assist

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/egress"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The pages context (wire.AssistContextPages): web pages a request names,
// fetched here and given to the model as text. Only https, no redirect off
// the page's host, bounded in time and size, and only where the
// platform's server egress allows (internal/egress: by default any host on
// a public address, never an internal one). A page that cannot be read is
// a line saying so, never a failed run.

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

// PagesClient is the fetcher's client under the server egress policy p,
// redirects kept on the page's host.
func PagesClient(p wire.ServerEgress) *http.Client {
	return &http.Client{
		Timeout:   pageTimeout,
		Transport: egress.Transport(p, pageTimeout),
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
		for _, known := range []error{egress.ErrNotPublic, egress.ErrNotListed} {
			if errors.Is(err, known) {
				return "", known
			}
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
