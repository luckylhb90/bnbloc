package roomtools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/luckylhb90/bnbloc/component"
)

// Sessions a system set on a component's sends from this computer
// (docs/prd/2026-10-28-member-held-sessions.md): a sign-in whose last step
// only the member's computer reaches answers with cookies, which stay here
// and go only on that component's later sends to that host naming the
// session. Never on disk, never to the command, never to the component.

// maxSessions bounds the jars kept (the oldest is dropped).
const maxSessions = 64

// sessionsFile keeps the held sessions across a restart of the bridge, in
// the member directory, sealed with a key kept beside it (0600 both): a
// restart signing the person in again everywhere would be visible on the
// systems (an OAuth authorization each time) and can hit their limits.
const (
	sessionsFile    = "egress-sessions.sealed"
	sessionsKeyFile = "egress-sessions.key"
)

type sessionKey struct{ component, host, name string }

type sessionStore struct {
	mu    sync.Mutex
	jars  map[sessionKey]*cookiejar.Jar
	order []sessionKey
	// kept are the cookies set in each jar (a cookiejar cannot list its
	// own), with their URL, for the file; dir "" keeps nothing on disk.
	kept map[sessionKey]map[string]keptCookie
	dir  string
}

// keptCookie is one cookie a system set, as kept on disk.
type keptCookie struct {
	URL    string    `json:"url"`
	Name   string    `json:"name"`
	Value  string    `json:"value"`
	Path   string    `json:"path,omitempty"`
	Domain string    `json:"domain,omitempty"`
	Exp    time.Time `json:"exp,omitempty"`
	Secure bool      `json:"secure,omitempty"`
	HTTP   bool      `json:"httpOnly,omitempty"`
}

// sessions are this process's held sessions (one bridge process per member
// directory).
var sessions = &sessionStore{jars: map[sessionKey]*cookiejar.Jar{}, kept: map[sessionKey]map[string]keptCookie{}}

// UseSessionsDir keeps the held sessions in dir (the member directory)
// from now on, reading what an earlier run kept.
func UseSessionsDir(dir string) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.dir = dir
	sessions.loadLocked()
}

type keptSession struct {
	Component string       `json:"component"`
	Host      string       `json:"host"`
	Name      string       `json:"name"`
	Cookies   []keptCookie `json:"cookies"`
}

// loadLocked reads the kept sessions (an unreadable file is ignored: the
// systems sign in again).
func (s *sessionStore) loadLocked() {
	if s.dir == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, sessionsFile))
	if err != nil {
		return
	}
	plain, err := s.openLocked(raw)
	if err != nil {
		slog.Warn("bnbloc bridge held sessions unreadable: signing in again", "event", "bnbloc.bridge.sessions_unreadable", "error", err)
		return
	}
	var all []keptSession
	if json.Unmarshal(plain, &all) != nil {
		return
	}
	now := time.Now()
	for _, ks := range all {
		k := sessionKey{ks.Component, ks.Host, ks.Name}
		j, _ := cookiejar.New(nil)
		for _, kc := range ks.Cookies {
			if !kc.Exp.IsZero() && kc.Exp.Before(now) {
				continue
			}
			u, err := url.Parse(kc.URL)
			if err != nil {
				continue
			}
			j.SetCookies(u, []*http.Cookie{{Name: kc.Name, Value: kc.Value, Path: kc.Path, Domain: kc.Domain, Expires: kc.Exp, Secure: kc.Secure, HttpOnly: kc.HTTP}})
			if s.kept[k] == nil {
				s.kept[k] = map[string]keptCookie{}
			}
			s.kept[k][kc.Domain+"|"+kc.Path+"|"+kc.Name] = kc
		}
		s.jars[k] = j
		s.order = append(s.order, k)
	}
}

// saveLocked writes the kept sessions (best effort).
func (s *sessionStore) saveLocked() {
	if s.dir == "" {
		return
	}
	all := make([]keptSession, 0, len(s.kept))
	for k, cs := range s.kept {
		ks := keptSession{Component: k.component, Host: k.host, Name: k.name}
		for _, c := range cs {
			ks.Cookies = append(ks.Cookies, c)
		}
		all = append(all, ks)
	}
	plain, _ := json.Marshal(all)
	sealed, err := s.sealLocked(plain)
	if err != nil {
		return
	}
	tmp := filepath.Join(s.dir, sessionsFile+".tmp")
	if os.WriteFile(tmp, sealed, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(s.dir, sessionsFile))
	}
}

// key is the sealing key kept in the member directory (made once).
func (s *sessionStore) key() ([]byte, error) {
	p := filepath.Join(s.dir, sessionsKeyFile)
	if k, err := os.ReadFile(p); err == nil && len(k) == 32 {
		return k, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, k, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func (s *sessionStore) sealLocked(plain []byte) ([]byte, error) {
	k, err := s.key()
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, nil), nil
}

func (s *sessionStore) openLocked(sealed []byte) ([]byte, error) {
	k, err := s.key()
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize() {
		return nil, errors.New("short")
	}
	return aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], nil)
}

// remember notes the cookies a system set in a jar, for the file.
func (s *sessionStore) remember(k sessionKey, u *url.URL, cs []*http.Cookie) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kept[k] == nil {
		s.kept[k] = map[string]keptCookie{}
	}
	for _, c := range cs {
		kc := keptCookie{URL: u.String(), Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain, Secure: c.Secure, HTTP: c.HttpOnly}
		switch {
		case c.MaxAge < 0:
			delete(s.kept[k], kc.Domain+"|"+kc.Path+"|"+kc.Name)
			continue
		case c.MaxAge > 0:
			kc.Exp = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
		default:
			kc.Exp = c.Expires
		}
		s.kept[k][kc.Domain+"|"+kc.Path+"|"+kc.Name] = kc
	}
	s.saveLocked()
}

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
		delete(s.kept, s.order[0])
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
	if _, had := s.kept[k]; had {
		delete(s.kept, k)
		s.saveLocked()
	}
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
func sessionFor(service string, u *url.URL, send *component.EgressSend) *heldJar {
	if send.Session == "" {
		return nil
	}
	k := sessionKey{service, strings.ToLower(u.Hostname()), send.Session}
	if send.EndSession {
		sessions.end(k)
	}
	return &heldJar{Jar: sessions.jar(k), key: k}
}

// heldJar is a session's jar and its key (what it sets is kept on disk).
type heldJar struct {
	*cookiejar.Jar
	key sessionKey
}

// withSession sets the jar's cookies for u on req (after the command's own
// were removed) and returns how to keep the answer's.
func withSession(jar *heldJar, req *http.Request) func(*http.Response) {
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
			sessions.remember(jar.key, req.URL, cs)
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
func sendSteps(ctx context.Context, in component.EgressRequest, send *component.EgressSend, jar *heldJar) ([]component.EgressStepReport, error) {
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
