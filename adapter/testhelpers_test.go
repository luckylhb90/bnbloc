package adapter

import (
	"io"
	"net/http"
	"strings"
)

func readAll(r *http.Request) ([]byte, error) { return io.ReadAll(r.Body) }

func containsAny(haystack string, needles ...string) bool {
	low := strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(low, n) {
			return true
		}
	}
	return false
}
