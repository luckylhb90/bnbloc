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
//  1. /readyz answers 503: Kubernetes removes the pod from its Services and
//     realtime's peers stop routing rooms to it (they probe /readyz).
//  2. Draining() is closed: long-lived streams end, so their clients reopen
//     on a pod that stays (they resume from their last sequence).
//  3. After drainDelay the listener stops taking connections and waits for
//     the requests in flight (a chat post's commit) up to shutdownTimeout.
//  4. The OnShutdown hooks run (marks written behind, queued notifications).

// drainDelay is how long a stopping service keeps answering after it said it
// is not ready: the time endpoints and peers take to notice.
var drainDelay = 5 * time.Second

// shutdownTimeout bounds waiting for the requests in flight.
const shutdownTimeout = 20 * time.Second

var (
	drainOnce  sync.Once
	drainCh    = make(chan struct{})
	hooksMu    sync.Mutex
	hooks      []func(context.Context)
	drainHooks []func()
)

// Draining is closed when this process starts to stop.
func Draining() <-chan struct{} { return drainCh }

// ErrDraining is the readiness answer of a process that is stopping.
var ErrDraining = errors.New("draining")

// ReadyUnlessDraining is check, refused while the process drains: what a
// replica asks of itself when it decides whether it still owns keys
// (shard.Set.CheckSelfWith), so its keys move before it stops.
func ReadyUnlessDraining(check func() error) func() error {
	return func() error {
		if isDraining() {
			return ErrDraining
		}
		if check == nil {
			return nil
		}
		return check()
	}
}

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
	drainOnce.Do(func() {
		close(drainCh)
		hooksMu.Lock()
		fns := append([]func(){}, drainHooks...)
		hooksMu.Unlock()
		for _, fn := range fns {
			fn()
		}
	})
}

// OnDrain registers fn to run as soon as the process starts to stop, before
// the drain delay (a replica leaving its peer set: shard.Set.Leave).
func OnDrain(fn func()) {
	hooksMu.Lock()
	drainHooks = append(drainHooks, fn)
	hooksMu.Unlock()
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
