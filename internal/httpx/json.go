package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// ErrorBody is the one error response body of every bnbloc HTTP surface
// (the gateway and every backend): {"error": "<message>"}, plus a
// machine-readable code where a route defines one (dispatch refusals).
// Clients read only this shape (the web's errorDetail).
type ErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// ErrorText is the message of a failed response's body: the one error
// body's "error", else the trimmed raw text (a proxy's or an older peer's
// plain body).
func ErrorText(raw []byte) string {
	var body ErrorBody
	if err := json.Unmarshal(raw, &body); err == nil && body.Error != "" {
		return body.Error
	}
	return strings.TrimSpace(string(raw))
}

// DefaultBodyLimit caps a JSON control body when a route names no larger
// limit: big enough for any settings or form document, small enough that a
// client cannot stream an unbounded payload into the decoder.
const DefaultBodyLimit = 64 << 10

// WriteJSON writes v as a JSON response with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	// A JSON response is never embedded in HTML, so messages keep their
	// <, > and & readable.
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// Error answers status with the one error body. Its arguments are in
// http.Error's order, which it replaces on every bnbloc surface. A 5xx must
// be preceded by obs.ServerError (TestNoSilent5xx); a status held in a
// variable goes through WriteStatus instead.
func Error(w http.ResponseWriter, msg string, status int) {
	WriteJSON(w, status, ErrorBody{Error: msg})
}

// WriteStatus answers a status computed at run time (a mapping helper's
// result, an upstream's status) with the one error body, recording err as the
// cause when the status is a 5xx so no server error goes unlogged.
func WriteStatus(w http.ResponseWriter, status int, msg string, err error) {
	if status >= 500 {
		obs.ServerError(w, msg, err)
	}
	Error(w, msg, status)
}

// Unavailable answers 503 for a dependency this deployment does not
// configure (no store, no backend URL, a feature turned off): the one status
// for "not configured", never 500/501/502.
func Unavailable(w http.ResponseWriter, what string) {
	msg := what + " not configured"
	obs.ServerError(w, msg, nil)
	Error(w, msg, http.StatusServiceUnavailable)
}

// DecodeJSON decodes the request body, capped at limit bytes
// (DefaultBodyLimit when limit <= 0), into v. It answers 413 for an
// oversized body and 400 for malformed JSON, and returns false then.
func DecodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	return decode(w, r, limit, v, false)
}

// DecodeOptionalJSON is DecodeJSON for a route whose body may be empty
// (v keeps its zero value).
func DecodeOptionalJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	return decode(w, r, limit, v, true)
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, v any, optional bool) bool {
	if limit <= 0 {
		limit = DefaultBodyLimit
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil || (optional && errors.Is(err, io.EOF)) {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return false
	}
	Error(w, "invalid json body", http.StatusBadRequest)
	return false
}
