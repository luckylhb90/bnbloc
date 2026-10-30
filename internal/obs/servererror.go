package obs

import (
	"log/slog"
	"net/http"
)

// ServerErrorEvent is the log event of every 5xx a service answers.
const ServerErrorEvent = "bnbloc.http.5xx"

// ServerError records why the handler is about to answer a 5xx: what failed
// (a short, secret-free phrase — usually the response message) and the
// underlying error (nil when there is none, e.g. an unconfigured feature).
// Call it immediately before writing the 5xx; it does not write the
// response, so each service keeps its own error body shape.
//
// Behind Middleware the cause rides the request's log line (one Error-level
// line per failed request: method, path, subject, status, what, error);
// elsewhere (a handler driven directly, e.g. in tests) it logs on its own.
//
// Convention (enforced by TestNoSilent5xx): every handler write of a 5xx
// status is preceded in its block by ServerError — or another log call —
// that names the branch's error. Error strings must not carry secrets.
func ServerError(w http.ResponseWriter, what string, err error) {
	if rec := findRecorder(w); rec != nil {
		rec.what, rec.cause = what, err
		return
	}
	attrs := []any{"event", ServerErrorEvent, "what", what}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	slog.Error("server error", attrs...)
}

// findRecorder walks the writer chain (Unwrap, as http.ResponseController
// does) to Middleware's recorder.
func findRecorder(w http.ResponseWriter) *statusRecorder {
	for w != nil {
		if rec, ok := w.(*statusRecorder); ok {
			return rec
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

// ProxyError is an httputil.ReverseProxy ErrorHandler that records the
// upstream failure as the cause of its 502 (the default handler only writes
// an unstructured log line).
func ProxyError(what string) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		// The request itself ended (its client left, its stream drained):
		// nothing failed upstream, and no one reads the answer.
		if r.Context().Err() != nil {
			w.WriteHeader(StatusClientClosed)
			return
		}
		ServerError(w, what, err)
		w.WriteHeader(http.StatusBadGateway)
	}
}

// StatusClientClosed is the status logged for a request its client (or the
// process draining) ended before it was answered (nginx's 499).
const StatusClientClosed = 499
