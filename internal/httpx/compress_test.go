package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestWriteStaticCompressesTextOnce(t *testing.T) {
	js := []byte(strings.Repeat("console.log('a previewer bundle');\n", 200))
	serve := func(accept, ctype string, data []byte) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", ctype)
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		WriteStatic(rec, req, "test:"+ctype+":"+string(rune(len(data))), data)
		return rec
	}
	rec := serve("gzip, deflate, br", "text/javascript; charset=utf-8", js)
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("headers = %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, js) || rec.Header().Get("Content-Length") == strconv.Itoa(len(js)) {
		t.Fatalf("round trip %d bytes, length %s", len(got), rec.Header().Get("Content-Length"))
	}
	for name, r := range map[string]*httptest.ResponseRecorder{
		"no gzip accepted": serve("", "text/javascript", js),
		"gzip refused":     serve("gzip;q=0", "text/javascript", js),
		"an image":         serve("gzip", "image/png", js),
		"tiny":             serve("gzip", "text/css", []byte("a{}")),
	} {
		if r.Header().Get("Content-Encoding") != "" || r.Header().Get("Content-Length") == "" {
			t.Errorf("%s: headers = %v", name, r.Header())
		}
	}
	if !Compressible("application/javascript") || Compressible("application/octet-stream") {
		t.Error("Compressible")
	}
}
