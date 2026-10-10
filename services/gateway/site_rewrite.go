package gateway

import (
	"bytes"
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Root-relative URL rewriting for served site files (PRD 2026-10-09 "Site
// file rules"). A site is served under a path (/v1/sites/<ticket>/ or
// /v1/public/shares/<token>/site/), but common build outputs (Vite, CRA with
// base "/") reference their assets root-relative ("/assets/index.js"). Those
// references in .html and .css are rebased onto the site base; protocol-
// relative ("//cdn…"), absolute and relative URLs are left alone. URLs that
// runtime JavaScript builds are not rewritten (documented limitation).

// rebaseURL rewrites one root-relative URL onto base (which ends in "/").
func rebaseURL(u, base string) string {
	trimmed := strings.TrimLeft(u, " \t\n\f\r")
	if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, `/\`) {
		return u
	}
	return base + trimmed[1:]
}

// urlAttrs are the HTML attributes holding one URL.
var urlAttrs = map[string]bool{
	"src": true, "href": true, "action": true, "formaction": true, "poster": true,
	"data": true, "background": true, "xlink:href": true, "manifest": true,
}

// srcsetAttrs hold a comma-separated candidate list ("url [descriptor], …").
var srcsetAttrs = map[string]bool{"srcset": true, "imagesrcset": true}

// rewriteSiteHTML rebases root-relative URLs in an HTML document: URL
// attributes, srcset lists, <meta http-equiv=refresh> targets, inline style
// attributes and <style> elements. Tokens it does not change are copied
// byte for byte (scripts, comments, text, the doctype), so only the edited
// tags are re-serialized.
func rewriteSiteHTML(src []byte, base string) []byte {
	z := html.NewTokenizer(bytes.NewReader(src))
	var out bytes.Buffer
	out.Grow(len(src) + 256)
	inStyle := false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if z.Err() == io.EOF {
				return out.Bytes()
			}
			// A tokenizer failure on a document we only rewrite: serve it
			// untouched rather than a truncated page.
			return src
		}
		// Copied before Token(): the tokenizer lowercases tag and attribute
		// names in its buffer, and untouched tags are copied verbatim.
		raw := append([]byte(nil), z.Raw()...)
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			inStyle = tt == html.StartTagToken && tok.Data == "style"
			if rewriteTagAttrs(&tok, base) {
				out.WriteString(tok.String())
				continue
			}
		case html.TextToken:
			if inStyle {
				out.Write(rewriteSiteCSS(raw, base))
				continue
			}
		case html.EndTagToken:
			inStyle = false
		}
		out.Write(raw)
	}
}

// rewriteTagAttrs rebases the URL-bearing attributes of one tag, reporting
// whether any changed.
func rewriteTagAttrs(tok *html.Token, base string) bool {
	changed := false
	isRefresh := tok.Data == "meta" && strings.EqualFold(attrValue(tok, "http-equiv"), "refresh")
	for i, a := range tok.Attr {
		key := strings.ToLower(a.Key)
		if a.Namespace != "" {
			key = a.Namespace + ":" + key
		}
		var v string
		switch {
		case urlAttrs[key]:
			v = rebaseURL(a.Val, base)
		case srcsetAttrs[key]:
			v = rebaseSrcset(a.Val, base)
		case key == "style":
			v = string(rewriteSiteCSS([]byte(a.Val), base))
		case key == "content" && isRefresh:
			v = rebaseRefresh(a.Val, base)
		default:
			continue
		}
		if v != a.Val {
			tok.Attr[i].Val = v
			changed = true
		}
	}
	return changed
}

func attrValue(tok *html.Token, key string) string {
	for _, a := range tok.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// rebaseSrcset rewrites each candidate URL of a srcset list.
func rebaseSrcset(v, base string) string {
	parts := strings.Split(v, ",")
	for i, p := range parts {
		lead := len(p) - len(strings.TrimLeft(p, " \t\n\f\r"))
		fields := strings.Fields(p)
		if len(fields) == 0 {
			continue
		}
		rebased := rebaseURL(fields[0], base)
		if rebased == fields[0] {
			continue
		}
		parts[i] = p[:lead] + rebased + strings.TrimPrefix(p[lead:], fields[0])
	}
	return strings.Join(parts, ",")
}

// refreshURL finds the url= part of a refresh content value ("5; url=/x").
var refreshURL = regexp.MustCompile(`(?i)(url\s*=\s*['"]?)([^'"]*)`)

func rebaseRefresh(v, base string) string {
	m := refreshURL.FindStringSubmatchIndex(v)
	if m == nil {
		return v
	}
	return v[:m[4]] + rebaseURL(v[m[4]:m[5]], base) + v[m[5]:]
}

// cssURL matches url(…) with an optional quote; cssImport matches the
// string form of @import.
var (
	cssURL    = regexp.MustCompile(`(?i)(url\(\s*['"]?)([^'")\s]*)`)
	cssImport = regexp.MustCompile(`(?i)(@import\s+['"])([^'"]*)`)
)

// rewriteSiteCSS rebases root-relative url(…) and @import "…" references.
func rewriteSiteCSS(src []byte, base string) []byte {
	rebase := func(re *regexp.Regexp, in []byte) []byte {
		return re.ReplaceAllFunc(in, func(m []byte) []byte {
			sub := re.FindSubmatchIndex(m)
			prefix, target := m[sub[2]:sub[3]], string(m[sub[4]:sub[5]])
			return append(append([]byte(nil), prefix...), rebaseURL(target, base)...)
		})
	}
	return rebase(cssImport, rebase(cssURL, src))
}
