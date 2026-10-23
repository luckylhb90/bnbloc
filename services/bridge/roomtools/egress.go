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
// request.
const egressIdle = 2 * time.Minute

// egressHosts is a session's claimed hosts: host → service.
type egressHosts map[string]string

// SetEgressHosts replaces the hosts services claim for the session (the
// bridge asks at each turn's start).
func (s *Session) SetEgressHosts(hosts map[string]string) {
	s.mu.Lock()
	s.egress = egressHosts(hosts)
	s.mu.Unlock()
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
		body, err := io.ReadAll(io.LimitReader(req.Body, component.MaxEgressBody+1))
		_ = req.Body.Close()
		var resp *http.Response
		switch {
		case err != nil:
			return
		case len(body) > component.MaxEgressBody:
			resp = egressError(http.StatusRequestEntityTooLarge, "the request body is larger than 8 MiB")
		default:
			_ = tconn.SetDeadline(time.Time{})
			resp = s.egressRoundTrip(sess, host, service, req, body)
		}
		resp.Close = req.Close
		_ = tconn.SetDeadline(time.Now().Add(egressIdle))
		if err := resp.Write(tconn); err != nil || req.Close {
			return
		}
	}
}

// hopHeaders never cross the proxy.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authorization", "Proxy-Authenticate", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// egressRoundTrip sends one request to the service for the bound job and
// turns its answer into the command's response; a card in the answer is
// posted for the turn's requester. When the service answers Send, the
// bridge sends the request itself, from this computer, with the
// credentials the service gives, and reports the system's answer back
// (the service may ask once more with fresh credentials, or answer itself).
func (s *Server) egressRoundTrip(sess *Session, host, service string, req *http.Request, body []byte) *http.Response {
	_, job := sess.egressFor(host)
	if job.JobID == "" {
		obs.ServerError(nil, "egress between turns: "+host, nil)
		return egressError(http.StatusServiceUnavailable, "no turn is running: the request to "+host+" was not sent")
	}
	header := req.Header.Clone()
	for _, h := range hopHeaders {
		header.Del(h)
	}
	in := component.EgressRequest{Method: req.Method, URL: "https://" + host + req.URL.RequestURI(), Header: header, Body: body}
	scope := sess.Scope()
	path := component.TurnProxyPath + url.PathEscape(service) + "/egress/request?room=" + url.QueryEscape(scope.RoomID) + "&job=" + url.QueryEscape(job.JobID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var sent *http.Response
	for attempt := 0; ; attempt++ {
		raw, err := s.cfg.Gateway.Do(ctx, http.MethodPost, path, in)
		if err != nil {
			var se *StatusError
			msg := "the " + service + " service could not be reached"
			if errors.As(err, &se) {
				msg = fmt.Sprintf("the %s service refused the call (%s): %s", service, httpStatusText(se.Status), strings.TrimSpace(string(bytes.TrimSpace(se.Body))))
			}
			obs.ServerError(nil, "egress call failed: "+service+" "+host+" job "+job.JobID, err)
			return egressError(http.StatusBadGateway, msg)
		}
		var out component.EgressResponse
		valid := json.Unmarshal(raw, &out) == nil && (out.Send != nil || (out.Accept && sent != nil) || (out.Status >= 100 && out.Status <= 599))
		if !valid {
			obs.ServerError(nil, "egress answer invalid: "+service, err)
			return egressError(http.StatusBadGateway, "the "+service+" service answered something that is not an egress response")
		}
		if out.Card != nil {
			s.postCard(sess, job, service, *out.Card)
		}
		switch {
		case out.Accept:
			slog.Info("bnbloc bridge egress", "event", "bnbloc.bridge.egress", "service", service, "host", host, "method", req.Method, "status", sent.StatusCode, "job", job.JobID, "from", "this computer")
			return sent
		case out.Send != nil:
			if err := sendsLeft(attempt); err != nil {
				obs.ServerError(nil, "egress: the "+service+" service", err)
				return egressError(http.StatusBadGateway, "the "+service+" service kept asking to send the request again")
			}
			resp, report, err := sendHere(ctx, in, out.Send)
			if err != nil {
				obs.ServerError(nil, "egress send from this computer: "+host, err)
				return egressError(http.StatusBadGateway, "this computer could not reach "+host+": "+err.Error())
			}
			report.Attempt = attempt + 1
			sent, in.Report = resp, report
			continue
		}
		slog.Info("bnbloc bridge egress", "event", "bnbloc.bridge.egress", "service", service, "host", host, "method", req.Method, "status", out.Status, "job", job.JobID)
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
// system sent it.
var hereClient = &http.Client{
	Timeout:       2 * time.Minute,
	Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, DisableCompression: true, TLSHandshakeTimeout: 15 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// sendHere sends the request with the service's credentials and returns
// the command's response (without cookies) and the report for the service.
func sendHere(ctx context.Context, in component.EgressRequest, send *component.EgressSend) (*http.Response, *component.EgressReport, error) {
	u, err := url.Parse(in.URL)
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
	req, err := http.NewRequestWithContext(ctx, in.Method, u.String(), bytes.NewReader(in.Body))
	if err != nil {
		return nil, nil, err
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
	resp, err := hereClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, component.MaxEgressBody+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > component.MaxEgressBody {
		return nil, nil, errors.New("the answer is larger than 8 MiB")
	}
	out := &http.Response{StatusCode: resp.StatusCode, ProtoMajor: 1, ProtoMinor: 1, Header: resp.Header.Clone()}
	for _, h := range append(hopHeaders, "Content-Length", "Set-Cookie") {
		out.Header.Del(h)
	}
	out.Body = io.NopCloser(bytes.NewReader(raw))
	out.ContentLength = int64(len(raw))
	if in.Method == http.MethodHead {
		out.Body, out.ContentLength = http.NoBody, 0
	}
	report := &component.EgressReport{Status: resp.StatusCode, Location: resp.Header.Get("Location"), Body: raw[:min(len(raw), component.MaxEgressReportBody)]}
	return out, report, nil
}

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
