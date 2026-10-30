package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
)

// api is the load generator's gateway client: the same public routes the web
// client calls, nothing internal.
type api struct {
	base    string
	http    *http.Client
	timeout time.Duration
}

// member is one simulated person: a subject and its session bearer.
type member struct {
	subject string
	token   string
	// expires is the session's expiry (zero when unknown).
	expires time.Time
}

// statusError is a non-2xx answer.
type statusError struct {
	method, path string
	status       int
	body         string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.method, e.path, e.status, strings.TrimSpace(e.body))
}

// errKey classifies a request failure for the count maps: "429", "503", or
// "error: timeout" / "error: refused" / "error: reset" / "error: other".
func errKey(err error) string {
	var se *statusError
	if errors.As(err, &se) {
		return fmt.Sprint(se.status)
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "error: timeout"
	case strings.Contains(err.Error(), "connection refused"):
		return "error: refused"
	case strings.Contains(err.Error(), "connection reset"), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "error: reset"
	case strings.Contains(err.Error(), "too many open files"):
		return "error: too many open files"
	}
	return "error: other"
}

// do sends one JSON call and decodes a JSON answer into out (when non-nil).
func (a *api) do(ctx context.Context, m *member, method, path string, hdr http.Header, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if m != nil {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &statusError{method: method, path: path, status: resp.StatusCode, body: truncate(string(raw), 200)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: invalid response: %w", method, path, err)
		}
	}
	return nil
}

// login signs subject in through POST /v1/auth/login, the dev sign-in a
// gateway without OIDC exposes (the dev verifier takes the subject itself as
// the identity token). forwardedFor, when set, is sent as X-Forwarded-For:
// the gateway's per-IP login throttle believes it only when the load
// generator is a trusted proxy (BNBLOC_TRUSTED_PROXIES).
func (a *api) login(ctx context.Context, subject, forwardedFor string) (member, error) {
	hdr := http.Header{}
	if forwardedFor != "" {
		hdr.Set("X-Forwarded-For", forwardedFor)
	}
	var grant struct {
		SessionToken string `json:"sessionToken"`
		Subject      string `json:"subject"`
	}
	if err := a.do(ctx, nil, http.MethodPost, "/v1/auth/login", hdr, map[string]string{"idpToken": subject}, &grant); err != nil {
		return member{}, err
	}
	if grant.SessionToken == "" {
		return member{}, errors.New("POST /v1/auth/login: answer has no sessionToken")
	}
	m := member{subject: grant.Subject, token: grant.SessionToken}
	if _, exp, err := sessionClaims(grant.SessionToken); err == nil {
		m.expires = exp
	}
	if m.subject == "" {
		m.subject = subject
	}
	return m, nil
}

// sessionClaims reads the subject and expiry of a bnbloc session bearer
// (<base64url JSON payload>.<signature>). The gateway verifies the signature
// on every call; this only names the member the token belongs to.
func sessionClaims(token string) (string, time.Time, error) {
	claims, err := auth.PeekSession(token)
	if err != nil {
		return "", time.Time{}, err
	}
	var exp time.Time
	if claims.ExpiresAtUnix > 0 {
		exp = time.Unix(claims.ExpiresAtUnix, 0)
	}
	return claims.Subject, exp, nil
}

// readTokensFile reads one session bearer per line ('#' starts a comment).
func readTokensFile(path string) ([]member, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []member
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		tok := strings.TrimSpace(sc.Text())
		if tok == "" || strings.HasPrefix(tok, "#") {
			continue
		}
		subject, exp, err := sessionClaims(tok)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, member{subject: subject, token: tok, expires: exp})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *api) createRoom(ctx context.Context, owner *member, roomID string) error {
	return a.do(ctx, owner, http.MethodPost, "/v1/rooms", nil, map[string]string{
		"roomId": roomID, "name": roomID, "visibility": "private",
	}, nil)
}

func (a *api) addMember(ctx context.Context, owner *member, roomID, userID string) error {
	return a.do(ctx, owner, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/members", nil,
		map[string]string{"userId": userID, "role": "member"}, nil)
}

func (a *api) deleteRoom(ctx context.Context, owner *member, roomID string) error {
	return a.do(ctx, owner, http.MethodDelete, "/v1/rooms/"+url.PathEscape(roomID), nil, nil, nil)
}

// channels returns the room's channel ids, the default first (the channel
// the web client opens a room on).
func (a *api) channels(ctx context.Context, m *member, roomID string) ([]string, error) {
	var out struct {
		Channels []struct {
			ChannelID string `json:"channelId"`
			IsDefault bool   `json:"isDefault"`
		} `json:"channels"`
	}
	if err := a.do(ctx, m, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/channels", nil, nil, &out); err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range out.Channels {
		if c.IsDefault {
			ids = append([]string{c.ChannelID}, ids...)
		} else {
			ids = append(ids, c.ChannelID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("room %s lists no channel", roomID)
	}
	return ids, nil
}

// createChannel makes a channel in the room as its owner.
func (a *api) createChannel(ctx context.Context, owner *member, roomID, name string) (string, error) {
	var out struct {
		ChannelID string `json:"channelId"`
	}
	if err := a.do(ctx, owner, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/channels", nil, map[string]string{"name": name}, &out); err != nil {
		return "", err
	}
	return out.ChannelID, nil
}

// sendChat posts one chat message exactly as the web client does
// (api.sendChat): text, atUnixMs, clientMsgId, channelId, no recipient.
func (a *api) sendChat(ctx context.Context, m *member, roomID, channelID, clientMsgID, text string) error {
	body := map[string]any{
		"text":        text,
		"atUnixMs":    time.Now().UnixMilli(),
		"clientMsgId": clientMsgID,
	}
	if channelID != "" {
		body["channelId"] = channelID
	}
	return a.do(ctx, m, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/chat", nil, body, nil)
}

// eventsURL is the room stream URL of the web room view (web/src/lib/sse.ts
// subscribeRoom with the room view's scope): the session token as the
// access_token query param (EventSource cannot set headers), a lanes stream
// with the member's channel open (and named as channelId), includePeople=1.
func (a *api) eventsURL(m *member, roomID, channelID, tz string) string {
	q := "access_token=" + url.QueryEscape(m.token) + "&channelId=" + url.QueryEscape(channelID) + "&lanes=1&open=" + url.QueryEscape(channelID) + "&includePeople=1"
	if tz != "" {
		q += "&tz=" + url.QueryEscape(tz)
	}
	return a.base + "/v1/rooms/" + url.PathEscape(roomID) + "/events?" + q
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// tzFor gives stream i the i-th time zone of the list, round-robin; "" when none.
func tzFor(list string, i int) string {
	var zones []string
	for _, z := range strings.Split(list, ",") {
		if z = strings.TrimSpace(z); z != "" {
			zones = append(zones, z)
		}
	}
	if len(zones) == 0 {
		return ""
	}
	return zones[i%len(zones)]
}
