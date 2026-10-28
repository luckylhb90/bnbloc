package roomtools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Egress through a service (docs/architecture/service-components.md,
// egress): the room tools server is also the session's HTTPS proxy. A
// session's commands get HTTPS_PROXY with the session's bearer
// (EgressEnv). A CONNECT to a host a service claimed for the session is
// answered with a certificate from the local CA; each request read on it
// goes to the service through the gateway's turn proxy for the bound job
// (POST /v1/turn-proxy/<service>/egress/request, an EgressRequest), and the
// service's answer is the command's. Every other CONNECT is tunnelled,
// never decrypted. A claimed host between turns answers 503: nothing goes
// out unauthenticated to a host a service handles.

// EgressProxyUser is the user name of the proxy URL (its password is the
// session's bearer).
const EgressProxyUser = "bnbloc"

// egressDialTimeout bounds a tunnel's connect.
const egressDialTimeout = 15 * time.Second

// egressIdle bounds how long an intercepted connection waits for its next
// request, and how long a send from this computer, its body and its answer
// may go without a byte moving: activity bounds a send, not a clock.
const egressIdle = 2 * time.Minute

// egressAsk bounds one question to the service (an envelope and its
// answer), never the send it decides.
const egressAsk = 5 * time.Minute

// egressHosts is a session's claimed hosts: host → service.
type egressHosts map[string]string

// SetEgressHosts replaces the hosts services claim for the session (the
// bridge asks at each turn's start), and the services among them that take
// a streamed request body (component.EgressHosts.Streams).
func (s *Session) SetEgressHosts(hosts map[string]string, streams map[string]bool) {
	s.mu.Lock()
	s.egress, s.streams = egressHosts(hosts), maps.Clone(streams)
	s.mu.Unlock()
}

// streamsTo reports whether service takes a streamed request body.
func (s *Session) streamsTo(service string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[service]
}

// EgressHosts is a copy of the hosts services claim for the session.
func (s *Session) EgressHosts() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(map[string]string(s.egress))
}

// egressFor is the service claiming host, and the bound job (zero between
// turns).
func (s *Session) egressFor(host string) (service string, job Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.egress[host], s.job
}

// cardOnce reports whether a card keyed key is new for the bound job (one
// card per job, service and title: an agent retrying a request does not
// repeat it).
func (s *Session) cardOnce(job string, key [2]string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cards == nil || s.cardsJob != job {
		s.cards, s.cardsJob = map[[2]string]bool{}, job
	}
	if s.cards[key] {
		return false
	}
	s.cards[key] = true
	return true
}

// EgressEnv is the environment a session's commands get for egress:
// the proxy (proxyHost "" = this server's loopback address; a sandboxed
// session's commands reach it through the guest's host address) and the
// CA files. A tool that keeps the system's roots only (Go on macOS) does
// not see the CA, and its calls to a claimed host fail their TLS check:
// they never go out unauthenticated.
func (s *Server) EgressEnv(token, proxyHost string) map[string]string {
	if s.ca == nil {
		return nil
	}
	if proxyHost == "" {
		proxyHost = s.Addr()
	}
	proxy := "http://" + url.UserPassword(EgressProxyUser, token).String() + "@" + proxyHost
	env := map[string]string{
		"HTTPS_PROXY": proxy, "https_proxy": proxy,
		"NO_PROXY": "localhost,127.0.0.1,::1", "no_proxy": "localhost,127.0.0.1,::1",
		"NODE_USE_ENV_PROXY": "1",
	}
	if s.ca.caFile != "" {
		env["NODE_EXTRA_CA_CERTS"] = s.ca.caFile
	}
	if s.ca.bundleFile != "" {
		for _, k := range []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO"} {
			env[k] = s.ca.bundleFile
		}
	}
	return env
}

// EgressCA is the local CA's certificate (PEM) and the bundle of the
// system's roots with it ("" without the egress proxy, nil bundle when
// the system keeps no PEM bundle).
func (s *Server) EgressCA() (caPEM, bundle []byte) {
	if s.ca == nil {
		return nil, nil
	}
	if s.ca.bundleFile != "" {
		bundle, _ = readFileQuiet(s.ca.bundleFile)
	}
	return s.ca.certPEM, bundle
}

// authenticateProxy resolves Proxy-Authorization (Basic, the bearer as its
// password) to a live session; sandboxed: by its egress alias (a sandboxed
// session's commands).
func (s *Server) authenticateProxy(r *http.Request) (sess *Session, sandboxed bool) {
	h := strings.TrimSpace(r.Header.Get("Proxy-Authorization"))
	raw, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return nil, false
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, false
	}
	_, token, _ := strings.Cut(string(dec), ":")
	if token == "" {
		return nil, false
	}
	k := sha256.Sum256([]byte(token))
	s.mu.Lock()
	direct := s.sessions[k]
	alias, aliased := s.aliases[k]
	s.mu.Unlock()
	switch {
	case direct != nil && subtle.ConstantTimeCompare([]byte(direct.token), []byte(token)) == 1 && !direct.revoked():
		return direct, false
	case aliased && subtle.ConstantTimeCompare([]byte(alias.token), []byte(token)) == 1 && !alias.sess.revoked():
		return alias.sess, true
	}
	return nil, false
}

// sandboxDial connects a sandboxed command's tunnel: to a public address,
// or to a private one only for a host the member opened (PrivateHosts),
// resolved here and dialled by that address, so a second answer cannot
// differ. Loopback, link-local and unspecified addresses stay closed to
// the guest. Private hosts go through the proxy like any other, so a
// connector's host that is also private is still served by its service.
func (s *Server) sandboxDial(ctx context.Context, hostport string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(host, ".")
	private := slices.ContainsFunc(s.cfg.PrivateHosts, func(h string) bool { return strings.EqualFold(h, name) })
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("%s does not resolve", host)
	}
	for _, a := range addrs {
		if !sandboxReaches(a, private) {
			return nil, fmt.Errorf("%s is not a public address: the sandbox does not reach it", host)
		}
	}
	d := net.Dialer{Timeout: egressDialTimeout}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(addrs[0].Unmap().String(), port))
}

// sandboxReaches reports whether a sandboxed tunnel may reach a: a public
// address, or a private one for a host the member opened (private); never
// loopback, link-local or unspecified.
func sandboxReaches(a netip.Addr, private bool) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return false
	}
	return private || !(a.IsPrivate() || cgnat(a))
}

// cgnat reports the shared address space (100.64.0.0/10), private in all
// but name.
func cgnat(a netip.Addr) bool {
	return a.Is4() && a.As4()[0] == 100 && a.As4()[1]&0xc0 == 64
}

// egressAlias is a second bearer of a session for its egress only: a
// sandboxed session's commands get it in their space's environment, which
// is set before the session exists (the bridge derives it from the
// session's workspace).
type egressAlias struct {
	token string
	sess  *Session
}

// AliasEgress lets token authenticate the session's egress (never its room
// tools). It goes with the session's revoke.
func (s *Server) AliasEgress(token string, sess *Session) error {
	if len(token) < minTokenBytes {
		return errors.New("roomtools: egress alias too short")
	}
	k := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[k] != nil {
		return errors.New("roomtools: egress alias is a session bearer")
	}
	s.aliases[k] = egressAlias{token: token, sess: sess}
	return nil
}

// dropAliasesLocked forgets every egress alias of sess.
func (s *Server) dropAliasesLocked(sess *Session) {
	for k, a := range s.aliases {
		if a.sess == sess {
			delete(s.aliases, k)
		}
	}
}

// ProxyListening reports the extra proxy listener sandboxed commands
// reach (Config.ProxyAddr) is up.
func (s *Server) ProxyListening() bool { return s.proxyLn != nil }

// handleConnect serves one CONNECT.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	sess, sandboxed := s.authenticateProxy(r)
	if sess == nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="bnbloc-egress"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "CONNECT needs host:port", http.StatusBadRequest)
		return
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	hj, ok := w.(http.Hijacker)
	if !ok {
		obs.ServerError(w, "egress proxy: the connection cannot be hijacked", nil)
		http.Error(w, "the proxy cannot take the connection", http.StatusInternalServerError)
		return
	}
	service, _ := sess.egressFor(host)
	if service != "" && port == "443" && s.ca != nil {
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		s.serveClaimed(conn, rw.Reader, sess, host, service)
		return
	}
	var up net.Conn
	if sandboxed {
		up, err = s.sandboxDial(r.Context(), r.Host)
	} else {
		up, err = net.DialTimeout("tcp", r.Host, egressDialTimeout)
	}
	if err != nil {
		obs.ServerError(w, "egress tunnel: dial "+r.Host, err)
		http.Error(w, "cannot reach "+r.Host+": "+err.Error(), http.StatusBadGateway)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = rw.Flush()
	go func() {
		_, _ = io.Copy(up, rw.Reader)
		if tc, ok := up.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	_, _ = io.Copy(conn, up)
	_ = up.Close()
	_ = conn.Close()
}

// prefixConn reads what the hijacked reader buffered before the
// connection itself.
type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c prefixConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// serveClaimed terminates TLS for a claimed host and answers each request
// through the service.
func (s *Server) serveClaimed(raw net.Conn, buffered *bufio.Reader, sess *Session, host, service string) {
	defer raw.Close()
	tconn := tls.Server(prefixConn{Conn: raw, r: io.MultiReader(buffered, raw)}, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return s.ca.leaf(host)
		},
	})
	_ = tconn.SetDeadline(time.Now().Add(egressIdle))
	if err := tconn.Handshake(); err != nil {
		slog.Debug("bnbloc bridge egress handshake failed", "event", "bnbloc.bridge.egress_handshake_failed", "host", host, "error", err)
		return
	}
	br := bufio.NewReader(tconn)
	for {
		_ = tconn.SetDeadline(time.Now().Add(egressIdle))
		req, err := http.ReadRequest(br)
		if err != nil || sess.revoked() {
			return
		}
		if strings.EqualFold(req.Header.Get("Expect"), "100-continue") {
			// Answered here, so the command sends its body now rather than
			// after its own wait.
			if _, err := io.WriteString(tconn, "HTTP/1.1 100 Continue\r\n\r\n"); err != nil {
				return
			}
			req.Header.Del("Expect")
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, component.MaxEgressBody+1))
		large := len(body) > component.MaxEgressBody
		var resp *http.Response
		switch {
		case err != nil:
			return
		case large && !sess.streamsTo(service):
			resp = egressError(http.StatusRequestEntityTooLarge, "the request body is larger than 8 MiB")
		case large:
			// The rest of the body streams to the system on a send, once
			// (docs/prd/2026-10-27-streamed-member-sends.md).
			_ = tconn.SetDeadline(time.Time{})
			resp = s.egressRoundTrip(sess, host, service, req, nil, &streamBody{r: idleReader{r: io.MultiReader(bytes.NewReader(body), req.Body), c: tconn}, n: req.ContentLength})
		default:
			_ = req.Body.Close()
			_ = tconn.SetDeadline(time.Time{})
			resp = s.egressRoundTrip(sess, host, service, req, body, nil)
		}
		// After a large body the connection is out of step (the body may be
		// unread): it ends with this answer, and the body is never drained.
		resp.Close = req.Close || large
		_ = tconn.SetDeadline(time.Time{})
		err = resp.Write(idleWriter{c: tconn})
		_ = resp.Body.Close()
		if err != nil || resp.Close {
			return
		}
	}
}

// streamBody is a request body larger than component.MaxEgressBody, sent
// as the command streams it: n bytes (-1: unknown, sent chunked).
type streamBody struct {
	r io.Reader
	n int64
}

// idleReader reads the command's connection, ending a read that waits
// longer than egressIdle.
type idleReader struct {
	r io.Reader
	c net.Conn
}

func (r idleReader) Read(p []byte) (int, error) {
	_ = r.c.SetReadDeadline(time.Now().Add(egressIdle))
	return r.r.Read(p)
}

// idleWriter writes to the command's connection, ending a write that
// waits longer than egressIdle.
type idleWriter struct{ c net.Conn }

func (w idleWriter) Write(p []byte) (int, error) {
	_ = w.c.SetWriteDeadline(time.Now().Add(egressIdle))
	return w.c.Write(p)
}

// idleConn is a connection of a send from this computer: each byte read or
// written moves both deadlines, so a long upload keeps the answer's read
// alive and a connection with nothing moving ends after egressIdle.
type idleConn struct{ net.Conn }

func (c idleConn) Read(p []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(egressIdle))
	n, err := c.Conn.Read(p)
	if n > 0 {
		_ = c.SetDeadline(time.Now().Add(egressIdle))
	}
	return n, err
}

func (c idleConn) Write(p []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(egressIdle))
	return c.Conn.Write(p)
}

// hopHeaders never cross the proxy.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authorization", "Proxy-Authenticate", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// egressRoundTrip sends one request to the service for the bound job and
// turns its answer into the command's response; a card in the answer is
// posted for the turn's requester. When the service answers Send, the
// bridge sends the request itself, from this computer (exchange). A large
// body (stream) is not in the envelope: it goes to the system on a send.
func (s *Server) egressRoundTrip(sess *Session, host, service string, req *http.Request, body []byte, stream *streamBody) *http.Response {
	_, job := sess.egressFor(host)
	if job.JobID == "" {
		obs.ServerError(nil, "egress between turns: "+host, nil)
		return egressError(http.StatusServiceUnavailable, "no turn is running: the request to "+host+" was not sent")
	}
	header := req.Header.Clone()
	for _, h := range hopHeaders {
		header.Del(h)
	}
	in := component.EgressRequest{Method: req.Method, URL: "https://" + host + req.URL.RequestURI(), Header: header, Body: body, Streamed: stream != nil}
	scope := sess.Scope()
	path := component.TurnProxyPath + url.PathEscape(service) + "/egress/request?room=" + url.QueryEscape(scope.RoomID) + "&job=" + url.QueryEscape(job.JobID)
	sent, out, xerr := exchange(context.Background(), s.cfg.Gateway, path, service, host, in, stream, func(c component.Card) { s.postCard(sess, job, service, c) })
	if xerr != nil {
		obs.ServerError(nil, "egress: "+service+" "+host+" job "+job.JobID, xerr.cause)
		return egressError(xerr.status, xerr.msg)
	}
	if out.Accept {
		slog.Info("bnbloc bridge egress", "event", "bnbloc.bridge.egress", "service", service, "host", host, "method", req.Method, "status", sent.StatusCode, "job", job.JobID, "from", "this computer", "streamed", stream != nil)
		return sent
	}
	slog.Info("bnbloc bridge egress", "event", "bnbloc.bridge.egress", "service", service, "host", host, "method", req.Method, "status", out.Status, "job", job.JobID, "streamed", stream != nil)
	resp := &http.Response{StatusCode: out.Status, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}}
	for k, v := range out.Header {
		resp.Header[http.CanonicalHeaderKey(k)] = v
	}
	for _, h := range append(hopHeaders, "Content-Length", "Set-Cookie") {
		resp.Header.Del(h)
	}
	resp.Body = io.NopCloser(bytes.NewReader(out.Body))
	resp.ContentLength = int64(len(out.Body))
	if req.Method == http.MethodHead {
		resp.Body, resp.ContentLength = http.NoBody, 0
	}
	return resp
}

// exchangeError is why an exchange ended without the service's answer:
// the status and message the command gets, and the cause for the log.
type exchangeError struct {
	status int
	msg    string
	cause  error
}

// exchange is the one egress protocol with a service, for a turn's command
// and for a send outside a turn: ask the service about in at path; to a
// Send, send it from this computer with the credentials it gives and ask
// again with the report, at most component.MaxEgressSends times. It ends
// with the service's answer (Accept: sent is the system's, its body still
// streaming, for the caller to close) or why not. A card in an answer goes
// to onCard. A streamed body (stream) is sent once.
func exchange(ctx context.Context, gw Gateway, path, service, host string, in component.EgressRequest, stream *streamBody, onCard func(component.Card)) (*http.Response, component.EgressResponse, *exchangeError) {
	var sent *http.Response
	drop := func() {
		if sent != nil {
			_ = sent.Body.Close()
			sent = nil
		}
	}
	signIns := 0
	for attempt := 0; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, egressAsk)
		raw, err := gw.Do(actx, http.MethodPost, path, in)
		cancel()
		if err != nil {
			drop()
			var se *StatusError
			msg := "the " + service + " service could not be reached"
			if errors.As(err, &se) {
				msg = fmt.Sprintf("the %s service refused the call (%s): %s", service, httpStatusText(se.Status), strings.TrimSpace(string(bytes.TrimSpace(se.Body))))
			}
			return nil, component.EgressResponse{}, &exchangeError{http.StatusBadGateway, msg, err}
		}
		var out component.EgressResponse
		if json.Unmarshal(raw, &out) != nil || !(out.Send != nil || (out.Accept && sent != nil) || (out.Status >= 100 && out.Status <= 599)) {
			drop()
			return nil, component.EgressResponse{}, &exchangeError{http.StatusBadGateway, "the " + service + " service answered something that is not an egress response", nil}
		}
		if out.Card != nil && onCard != nil {
			onCard(*out.Card)
		}
		if out.Send == nil {
			if !out.Accept {
				// The service's own answer replaces the system's.
				drop()
			}
			return sent, out, nil
		}
		drop()
		if out.Send.StepsOnly {
			// A sign-in first (docs/prd/2026-10-28-member-held-sessions.md):
			// its steps are sent with the session and reported; the request
			// itself waits for the next answer.
			u, _ := url.Parse(in.URL)
			if signIns++; signIns > component.MaxEgressSignIns || u == nil {
				return nil, component.EgressResponse{}, &exchangeError{http.StatusBadGateway, "the " + service + " service kept asking to sign in again", nil}
			}
			steps, err := sendSteps(ctx, in, out.Send, sessionFor(service, u, out.Send))
			if err != nil {
				return nil, component.EgressResponse{}, &exchangeError{http.StatusBadGateway, "this computer could not sign in to " + host + ": " + err.Error(), err}
			}
			in.Report = &component.EgressReport{Attempt: attempt, Steps: steps}
			attempt--
			continue
		}
		if err := sendsLeft(attempt); err != nil {
			return nil, component.EgressResponse{}, &exchangeError{http.StatusBadGateway, "the " + service + " service kept asking to send the request again", err}
		}
		if stream != nil && attempt > 0 {
			// decision: docs/prd/2026-10-27-streamed-member-sends.md (3), a
			// streamed body is sent once and never kept for a second send.
			return nil, component.EgressResponse{}, &exchangeError{http.StatusBadGateway, "the " + service + " service asked to send the request again, but its body was streamed once and is not kept: run the command again", nil}
		}
		resp, report, err := sendHere(ctx, service, in, stream, out.Send)
		if err != nil {
			return nil, component.EgressResponse{}, &exchangeError{http.StatusBadGateway, "this computer could not reach " + host + ": " + err.Error(), err}
		}
		report.Attempt = attempt + 1
		sent, in.Report = resp, report
	}
}

// SendFromHere runs a send outside a turn (docs/prd/2026-10-25-member-sends.md):
// the platform queued it for this computer's owner; the bridge runs the
// egress protocol for it through component.SendProxyPath. Its answer is
// the service's to read; nothing reaches an agent.
func SendFromHere(ctx context.Context, gw Gateway, id, service, method, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	in := component.EgressRequest{Method: method, URL: u.String(), Header: map[string][]string{}}
	// The component's name keys its sessions, as on a turn's sends.
	sent, _, xerr := exchange(ctx, gw, component.SendProxyPath+url.PathEscape(id), service, u.Host, in, nil, nil)
	if xerr != nil {
		return errors.Join(errors.New(xerr.msg), xerr.cause)
	}
	if sent != nil {
		_ = sent.Body.Close()
	}
	slog.Info("bnbloc bridge send", "event", "bnbloc.bridge.send", "host", u.Host, "method", method, "send", id)
	return nil
}

// sendsLeft refuses a send past component.MaxEgressSends.
func sendsLeft(attempt int) error {
	if attempt >= component.MaxEgressSends {
		return errors.New("asked for one send too many")
	}
	return nil
}

// hereClient sends a request from this computer: to the claimed host only
// (the request's own URL), redirects never followed, the answer as the
// system sent it. Activity bounds it (idleConn), not a clock.
var hereClient = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableCompression: true, TLSHandshakeTimeout: 15 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{Timeout: egressDialTimeout}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return idleConn{Conn: c}, nil
		}},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// sendHere sends the request with the service's credentials and returns
// the command's response (without cookies; its body streams, and closing
// it ends the send) and the report for the service: the status, the
// Location and the answer's first component.MaxEgressReportBody bytes. A
// streamed body is the command's own, as it arrives.
func sendHere(ctx context.Context, service string, in component.EgressRequest, stream *streamBody, send *component.EgressSend) (*http.Response, *component.EgressReport, error) {
	u, err := url.Parse(in.URL)
	if err != nil {
		return nil, nil, err
	}
	jar := sessionFor(service, u, send)
	steps, err := sendSteps(ctx, in, send, jar)
	if err != nil {
		return nil, nil, err
	}
	if len(send.Query) > 0 {
		q := u.Query()
		for k, v := range send.Query {
			q[k] = v
		}
		u.RawQuery = q.Encode()
	}
	if send.RawQuery != "" {
		u.RawQuery = send.RawQuery
	}
	var body io.Reader = bytes.NewReader(in.Body)
	length := int64(len(in.Body))
	switch {
	case stream != nil && send.ReplaceBody:
		return nil, nil, errors.New("the service replaced a body it never saw")
	case stream != nil:
		body, length = stream.r, stream.n
	case send.ReplaceBody:
		body, length = bytes.NewReader(send.Body), int64(len(send.Body))
	}
	sctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(sctx, in.Method, u.String(), body)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req.ContentLength = max(length, -1)
	if length == 0 {
		req.Body = http.NoBody
	}
	for k, v := range in.Header {
		req.Header[http.CanonicalHeaderKey(k)] = v
	}
	for _, h := range append(send.Remove, "Content-Length") {
		req.Header.Del(h)
	}
	for k, v := range send.Header {
		req.Header[http.CanonicalHeaderKey(k)] = v
	}
	keep := withSession(jar, req)
	resp, err := hereClient.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	keep(resp)
	head := make([]byte, component.MaxEgressReportBody)
	n, err := io.ReadFull(resp.Body, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		_ = resp.Body.Close()
		cancel()
		return nil, nil, err
	}
	head = head[:n]
	out := &http.Response{StatusCode: resp.StatusCode, ProtoMajor: 1, ProtoMinor: 1, Header: resp.Header.Clone(), ContentLength: resp.ContentLength}
	for _, h := range append(hopHeaders, "Content-Length", "Set-Cookie") {
		out.Header.Del(h)
	}
	if out.ContentLength < 0 {
		// Chunked to the command: a body of unknown length otherwise ends
		// only with the connection, which stays open for its next request.
		out.TransferEncoding = []string{"chunked"}
	}
	out.Body = &relayBody{r: io.MultiReader(bytes.NewReader(head), resp.Body), close: func() error { defer cancel(); return resp.Body.Close() }}
	if in.Method == http.MethodHead {
		_ = out.Body.Close()
		out.Body, out.ContentLength = http.NoBody, 0
	}
	report := &component.EgressReport{Status: resp.StatusCode, Location: resp.Header.Get("Location"), Body: head, Steps: steps}
	return out, report, nil
}

// relayBody is a system's answer on its way to the command: its first bytes
// (already read for the report), then the rest as it arrives.
type relayBody struct {
	r     io.Reader
	close func() error
}

func (b *relayBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *relayBody) Close() error               { return b.close() }

// egressError is the proxy's own answer: JSON naming what happened.
func egressError(status int, msg string) *http.Response {
	raw, _ := json.Marshal(map[string]string{"error": msg, "from": "bnbloc egress proxy"})
	return &http.Response{
		StatusCode: status, ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(raw)),
		ContentLength: int64(len(raw)),
	}
}

// cardKey is what makes a card another one: its title and what its
// actions open (two held changes of one system share a title, not the
// change their Review opens).
func cardKey(c component.Card) string {
	key := c.Title
	for _, a := range c.Actions {
		key += "\n" + a.Page + "?" + a.Query
	}
	return key
}

// postCard puts a service's card in the room for the turn's requester (a
// component-card notice under the job), once per job, service and card
// (cardKey).
func (s *Server) postCard(sess *Session, job Job, service string, c component.Card) {
	if err := component.CheckCard(c); err != nil {
		slog.Warn("bnbloc bridge service card refused", "event", "bnbloc.bridge.card_refused", "service", service, "error", err)
		return
	}
	if !sess.cardOnce(job.JobID, [2]string{service, cardKey(c)}) {
		return
	}
	card := wire.ComponentCard{Component: service, For: job.RequesterUserID, Title: c.Title, Text: c.Text}
	for _, a := range c.Actions {
		card.Actions = append(card.Actions, wire.ComponentCardAction{Kind: a.Kind, Label: a.Label, Page: a.Page, Query: a.Query})
	}
	seq, _ := sess.notes.nextLine(job.JobID, false)
	scope := sess.Scope()
	body := wire.JobNotice{
		RoomID: scope.RoomID, ChannelID: job.ChannelID, JobID: job.JobID, SourceSeq: job.MessageSeq,
		Kind: wire.NoticeComponentCard, Text: c.Title, OwnerUserID: scope.Owner, Provider: scope.Provider,
		Extra:   map[string]any{"card": card},
		Session: ProgressSessionID(job.JobID), Seq: seq,
		MessageID: wire.NoticeComponentCard + "-" + job.JobID + "-" + strconv.FormatInt(seq, 10),
		AtUnixMs:  time.Now().UnixMilli(),
	}.Report()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.cfg.Gateway.Do(ctx, http.MethodPost, "/v1/rooms/"+url.PathEscape(scope.RoomID)+"/agent-activity/messages", body); err != nil {
		slog.Warn("bnbloc bridge service card not posted", "event", "bnbloc.bridge.card_failed", "service", service, "job", job.JobID, "error", err)
	}
}
