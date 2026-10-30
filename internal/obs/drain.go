package obs

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Draining: a service told to stop (SIGTERM, SIGINT) keeps serving while the
// cluster stops sending it work, then finishes what it holds and exits,
// instead of dropping every request and stream at once
// (docs/production-deployment.md#high-availability).
//
//  1. /readyz answers 503: Kubernetes removes the pod from its Services.
//  2. Draining() is closed: long-lived streams end, so their clients reopen
//     on a pod that stays (they resume from their last sequence).
//  3. After drainDelay the listener stops taking connections and waits for
//     the requests in flight (a chat post's commit) up to shutdownTimeout.
//  4. The OnShutdown hooks run (marks written behind, queued notifications).

// drainDelay is how long a stopping service keeps answering after it said it
// is not ready: the time endpoints take to notice.
var drainDelay = 5 * time.Second

// shutdownTimeout bounds waiting for the requests in flight.
const shutdownTimeout = 20 * time.Second

var (
	drainOnce sync.Once
	drainCh   = make(chan struct{})
	hooksMu   sync.Mutex
	hooks     []func(context.Context)
)

// Draining is closed when this process starts to stop.
func Draining() <-chan struct{} { return drainCh }

// ErrDraining is the readiness answer of a process that is stopping.
var ErrDraining = errors.New("draining")

// IsDraining reports whether this process has started to stop.
func IsDraining() bool { return isDraining() }

func isDraining() bool {
	select {
	case <-drainCh:
		return true
	default:
		return false
	}
}

func startDraining() {
	drainOnce.Do(func() { close(drainCh) })
}

// OnShutdown registers fn to run once the listener has stopped, before the
// process exits; ctx bounds it.
func OnShutdown(fn func(ctx context.Context)) {
	hooksMu.Lock()
	hooks = append(hooks, fn)
	hooksMu.Unlock()
}

func runShutdownHooks(ctx context.Context) {
	hooksMu.Lock()
	fns := append([]func(context.Context){}, hooks...)
	hooksMu.Unlock()
	for _, fn := range fns {
		fn(ctx)
	}
}

// resetDrainForTest makes the process not draining again (tests only).
func resetDrainForTest() {
	drainOnce = sync.Once{}
	drainCh = make(chan struct{})
}
