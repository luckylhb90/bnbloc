package core

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type envErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *envErr) Error() string { return e.Code + ": " + e.Message }

// A failed answer is read bounded and mapped once: an error envelope becomes
// the protocol's typed error (with the HTTP answer still reachable), anything
// else stays the *HTTPError.
func TestRESTSendMapsFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/envelope":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"conflict","message":"busy"}}`))
		case "/huge":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(strings.Repeat("x", 4*maxErrorBodyBytes)))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"no code"}}`))
		}
	}))
	defer srv.Close()
	c := REST{Name: "t", DecodeError: DecodeEnvelope[envErr]()}
	ep := Endpoint{BaseURL: srv.URL, Token: "tok"}

	_, err := c.Get(context.Background(), ep, "/envelope")
	var typed *envErr
	var he *HTTPError
	if !errors.As(err, &typed) || typed.Code != "conflict" || !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("envelope err = %v, want the typed error over the 409", err)
	}
	_, err = c.Get(context.Background(), ep, "/plain")
	if errors.As(err, &typed) || !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("codeless body err = %v, want the plain *HTTPError", err)
	}
	_, err = c.Get(context.Background(), ep, "/huge")
	if !errors.As(err, &he) || len(he.Body) > maxErrorBodyBytes {
		t.Fatalf("huge error body kept %d bytes, want <= %d", len(he.Body), maxErrorBodyBytes)
	}
}

// Every bnbloc protocol client (manifest cores, Core Protocol, Sandbox
// Protocol, conformance) builds its requests through REST.Request: the one
// place a bearer goes on the wire and the one error mapping.
func TestProtocolRequestsBuiltByREST(t *testing.T) {
	root := filepath.Join("..")
	forbidden := regexp.MustCompile(`http\.NewRequest|"Bearer |\.Header\.Set\("Authorization"`)
	owner := filepath.Join("core", "rest.go")
	for _, dir := range []string{"core", "sandbox"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || rel == owner {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if m := forbidden.Find(src); m != nil {
				t.Errorf("%s builds its own request (%s): go through core.REST.Request / Do / JSON", rel, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
