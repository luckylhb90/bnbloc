package roomtools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/component"
)

// Sessions a system set on a component's sends from this computer
// (docs/prd/2026-10-28-member-held-sessions.md): a sign-in whose last step
// only the member's computer reaches answers with cookies, which stay here
// and go only on that component's later sends to that host naming the
// session. Never on disk, never to the command, never to the component.

// maxSessions bounds the jars kept (the oldest is dropped).
const maxSessions = 64

type sessionKey struct{ component, host, name string }

type sessionStore struct {
	mu    sync.Mutex
	jars  map[sessionKey]*cookiejar.Jar
	order []sessionKey
}

// sessions are this process's held sessions (one bridge process per member
// directory).
var sessions = &sessionStore{jars: map[sessionKey]*cookiejar.Jar{}}

// jar is the session's jar, made when absent.
func (s *sessionStore) jar(k sessionKey) *cookiejar.Jar {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jars[k]; ok {
		return j
	}
	j, _ := cookiejar.New(nil)
	if len(s.order) >= maxSessions {
		delete(s.jars, s.order[0])
		s.order = s.order[1:]
	}
	s.jars[k] = j
	s.order = append(s.order, k)
	return j
}

// end drops the session.
func (s *sessionStore) end(k sessionKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jars, k)
	for i, o := range s.order {
		if o == k {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// sessionFor is the jar a send uses (nil: the send names no session).
// A jar serves only its own host: a cookie a system set for a parent
// domain never reaches another host through it.
func sessionFor(service string, u *url.URL, send *component.EgressSend) *cookiejar.Jar {
	if send.Session == "" {
		return nil
	}
	k := sessionKey{service, strings.ToLower(u.Hostname()), send.Session}
	if send.EndSession {
		sessions.end(k)
	}
	return sessions.jar(k)
}

// withSession sets the jar's cookies for u on req (after the command's own
// were removed) and returns how to keep the answer's.
func withSession(jar *cookiejar.Jar, req *http.Request) func(*http.Response) {
	if jar == nil {
		return func(*http.Response) {}
	}
	req.Header.Del("Cookie")
	for _, c := range jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	return func(resp *http.Response) {
		if cs := resp.Cookies(); len(cs) > 0 {
			jar.SetCookies(req.URL, cs)
		}
	}
}

// errBadStep: a sign-in step outside what a step may be.
var errBadStep = errors.New("a sign-in step must be a GET, HEAD or POST of the request's own https host")

// stepMethod reports a method a sign-in step may use.
func stepMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodPost
}

// sendSteps sends a sign-in's steps from this computer with the session,
// in order, redirects not followed, and reports each answer.
func sendSteps(ctx context.Context, in component.EgressRequest, send *component.EgressSend, jar *cookiejar.Jar) ([]component.EgressStepReport, error) {
	if len(send.Steps) == 0 {
		return nil, nil
	}
	if jar == nil || len(send.Steps) > component.MaxEgressSteps {
		return nil, fmt.Errorf("a sign-in needs a session and at most %d steps", component.MaxEgressSteps)
	}
	host, err := url.Parse(in.URL)
	if err != nil {
		return nil, err
	}
	out := make([]component.EgressStepReport, 0, len(send.Steps))
	for _, st := range send.Steps {
		u, err := url.Parse(st.URL)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, host.Host) || !stepMethod(st.Method) || (len(st.Body) > 0 && st.Method != http.MethodPost) {
			return nil, errBadStep
		}
		req, err := http.NewRequestWithContext(ctx, st.Method, u.String(), bytes.NewReader(st.Body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "*/*")
		for k, v := range st.Header {
			if !strings.EqualFold(k, "Cookie") && !strings.EqualFold(k, "Host") {
				req.Header.Set(k, v)
			}
		}
		keep := withSession(jar, req)
		resp, err := hereClient.Do(req)
		if err != nil {
			return nil, err
		}
		keep(resp)
		head, _ := io.ReadAll(io.LimitReader(resp.Body, component.MaxEgressReportBody))
		_ = resp.Body.Close()
		out = append(out, component.EgressStepReport{URL: u.String(), Status: resp.StatusCode, Location: resp.Header.Get("Location"), Body: head})
	}
	return out, nil
}
