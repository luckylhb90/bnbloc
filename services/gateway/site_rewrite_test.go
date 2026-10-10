package gateway

import (
	"strings"
	"testing"
)

const rewriteBase = "/v1/sites/T.k/"

func TestRewriteSiteHTML_rebases_root_relative_urls_only(t *testing.T) {
	in := `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <base href="/">
    <link rel="icon" type="image/svg+xml" href="/vite.svg" />
    <script type="module" crossorigin src="/assets/index-AbC123.js"></script>
    <link rel="stylesheet" href="/assets/index-x.css">
    <link rel="modulepreload" href="/assets/vendor.js">
    <link rel="preconnect" href="//cdn.example.com">
    <script src="https://cdn.example.com/lib.js"></script>
    <meta http-equiv="refresh" content="30; url=/reload">
    <style>body{background:url('/bg.png')} .x{background:url(//cdn.example.com/x.png)}</style>
    <!-- <script src="/commented.js"></script> -->
  </head>
  <body>
    <img srcset="/img/a-1x.png 1x, /img/a-2x.png 2x, https://x.example/y.png 3x" src="/img/a.png" alt="A &amp; B">
    <form action="/submit"><button formaction="/alt">go</button></form>
    <video poster="/poster.jpg"><source src="/clip.mp4"></video>
    <div id="root" style="background-image: url(/hero.jpg)"></div>
    <script>if (a && b < c) { location.href = "/x" }</script>
    <a href="relative/page.html">r</a> <a href="#top">t</a> <a href="mailto:x@example.com">m</a>
    <svg viewBox="0 0 10 10"><image href="/sprite.svg"/></svg>
  </body>
</html>`
	out := string(rewriteSiteHTML([]byte(in), rewriteBase))

	for _, want := range []string{
		`<!DOCTYPE html>`,
		`<base href="/v1/sites/T.k/">`,
		`href="/v1/sites/T.k/vite.svg"`,
		`<script type="module" crossorigin="" src="/v1/sites/T.k/assets/index-AbC123.js"></script>`,
		`<link rel="stylesheet" href="/v1/sites/T.k/assets/index-x.css">`,
		`href="/v1/sites/T.k/assets/vendor.js"`,
		`<link rel="preconnect" href="//cdn.example.com">`,
		`<script src="https://cdn.example.com/lib.js"></script>`,
		`content="30; url=/v1/sites/T.k/reload"`,
		`<style>body{background:url('/v1/sites/T.k/bg.png')} .x{background:url(//cdn.example.com/x.png)}</style>`,
		`<!-- <script src="/commented.js"></script> -->`,
		`srcset="/v1/sites/T.k/img/a-1x.png 1x, /v1/sites/T.k/img/a-2x.png 2x, https://x.example/y.png 3x"`,
		`src="/v1/sites/T.k/img/a.png"`,
		`alt="A &amp; B"`,
		`action="/v1/sites/T.k/submit"`,
		`formaction="/v1/sites/T.k/alt"`,
		`poster="/v1/sites/T.k/poster.jpg"`,
		`<source src="/v1/sites/T.k/clip.mp4">`,
		`style="background-image: url(/v1/sites/T.k/hero.jpg)"`,
		`<script>if (a && b < c) { location.href = "/x" }</script>`,
		`<a href="relative/page.html">r</a> <a href="#top">t</a> <a href="mailto:x@example.com">m</a>`,
		`<svg viewBox="0 0 10 10">`,
		`href="/v1/sites/T.k/sprite.svg"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %s", want)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
}

func TestRewriteSiteHTML_leaves_documents_without_root_urls_byte_identical(t *testing.T) {
	in := "<!doctype html><HTML><Body class=X>café &copy; <script>a<b&&c</script><img src=a.png></Body></HTML>"
	if out := string(rewriteSiteHTML([]byte(in), rewriteBase)); out != in {
		t.Fatalf("changed:\n%s", out)
	}
}

func TestRewriteSiteCSS(t *testing.T) {
	in := `@import "/theme.css"; @import '/print.css' print; @import url(/x.css);
.a{background:url("/i.png")} .b{background:url( /j.png )} .c{background:url(//cdn/x.png)}
.d{background:url(data:image/png;base64,AAA)} .e{background:url(rel.png)} .f{src:url(https://x/y.woff2)}`
	want := `@import "/v1/sites/T.k/theme.css"; @import '/v1/sites/T.k/print.css' print; @import url(/v1/sites/T.k/x.css);
.a{background:url("/v1/sites/T.k/i.png")} .b{background:url( /v1/sites/T.k/j.png )} .c{background:url(//cdn/x.png)}
.d{background:url(data:image/png;base64,AAA)} .e{background:url(rel.png)} .f{src:url(https://x/y.woff2)}`
	if got := string(rewriteSiteCSS([]byte(in), rewriteBase)); got != want {
		t.Fatalf("css\n got %s\nwant %s", got, want)
	}
}
