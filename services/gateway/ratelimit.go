package gateway

import (
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
)

// The gateway's request budgets: each is a ratelimit.Lazy on the Service
// (service.go), spent before a handler runs.

// defaultChatRate / defaultChatBurst bound cost-bearing writes per subject:
// sustained ~2/sec with a burst of 10, enough for brisk human typing and
// legitimate agent addressing, but not spam or runaway automation.
const (
	defaultChatRate  = 2.0
	defaultChatBurst = 10.0
)

// defaultGuessRate / defaultGuessBurst bound brute-forceable attempts (login
// and the API-token exchange per client IP, invite redemption per subject):
// ~5/min sustained with a burst of 10, enough for a human retrying a typo but
// far below a credential-stuffing / code-guessing rate.
const (
	defaultGuessRate  = 5.0 / 60.0
	defaultGuessBurst = 10.0
)

// defaultPublicRate / defaultPublicBurst bound the unauthenticated public
// share reads per client IP: a page load fetches the metadata, the content
// and a site's files, so the burst covers one load of a typical static app.
const (
	defaultPublicRate  = 10.0
	defaultPublicBurst = 40.0
)

// defaultVisitorRate / defaultVisitorBurst bound how many visitor tokens
// one client IP mints (POST /v1/public/shares/{token}/visitor): a page load
// mints one, renewals reuse the visitor, and a crowd behind one NAT still
// fits, while one client cannot mint identities to stuff an app.
const (
	defaultVisitorRate  = 20.0 / 60.0
	defaultVisitorBurst = 30.0
)

// appDim is what an interactive app's call is limited by: the client IP,
// the link it came through (a share link, else the app) or the
// participant (member, agent or visitor).
type appDim int

const (
	appByIP appDim = iota
	appByLink
	appByParticipant
	appDims
)

// appRate is one app budget: rate tokens/sec up to burst.
type appRate struct{ rate, burst float64 }

// appRates are the app data budgets by dimension, read (appReadBudget)
// and write (appWriteBudget): writes are stricter than reads in every
// dimension (TestAppRatesWritesStricter). A link's budget is shared by
// everyone using it, so it is the widest; a participant's the narrowest.
// The change feed's connect spends a read.
var appRates = [appDims]struct{ read, write appRate }{
	appByIP:          {read: appRate{20, 80}, write: appRate{5, 20}},
	appByLink:        {read: appRate{200, 400}, write: appRate{50, 100}},
	appByParticipant: {read: appRate{10, 40}, write: appRate{2, 10}},
}

// appSpend spends one token of an app budget (b is appReadBudget or
// appWriteBudget) for key in dimension d; 429 when it is spent.
func (s *Service) appSpend(w http.ResponseWriter, d appDim, b rateBudget, key string) bool {
	rt, l := appRates[d].read, &s.appLimiters[d].read
	if b == appWriteBudget {
		rt, l = appRates[d].write, &s.appLimiters[d].write
	}
	if !l.Get(rt.rate, rt.burst, s.Peers.Size).Allow(key) {
		httpx.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return false
	}
	return true
}

// visitorAllow consumes one visitor-token mint for a client IP.
func (s *Service) visitorAllow(ip string) bool {
	return s.visitorLimiter.Get(defaultVisitorRate, defaultVisitorBurst, s.Peers.Size).Allow("ip:" + ip)
}
