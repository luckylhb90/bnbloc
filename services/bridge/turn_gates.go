package bridge

import "context"

// One agent runs turns in parallel (docs/prd/2026-10-14-multi-member-rooms.md
// §2b). The drive lock was the agent slot's, so one agent's turns were
// serialized across every channel and every requester, and with turns of no
// fixed length one long task blocked everyone else for hours. A turn now
// passes three gates, always in this order:
//
//   - its session's (sessionKey): the turns of one conversation queue, never
//     interleaving on one core session;
//   - its agent's (room, core, provider: every channel and seat): at most the
//     core's limits.maxConcurrentTurns turns at once, defaultAgentTurns when
//     it states none;
//   - its workspace's (driveKey): held while a turn starts — the session
//     bound or created (the workspace with it), the .room/ pass, the input
//     sent — so two starts never write one workspace's records at once; a
//     sandboxed turn holds it to its end, since readying the space and the
//     fence's quiesce act on every process in it.
//
// A job waiting at a gate keeps renewing its lease (runBridgeJob renews from
// before the first stage), and a cancelled one stops waiting.

// defaultAgentTurns is how many turns one agent runs at once when its core
// states no limit.
const defaultAgentTurns = 2

// gate admits at most cap(g) holders at once.
type gate chan struct{}

// enter waits for room in g, or for ctx to end.
func (g gate) enter(ctx context.Context) error {
	select {
	case g <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g gate) leave() { <-g }

// turnGates are the gates of the turns this bridge drives, pruned with a
// room (forgetRoom).
type turnGates struct {
	sessions   map[sessionKey]gate
	agents     map[driveKey]gate
	workspaces map[driveKey]gate
}

func newTurnGates() turnGates {
	return turnGates{sessions: map[sessionKey]gate{}, agents: map[driveKey]gate{}, workspaces: map[driveKey]gate{}}
}

// forgetRoom drops room's gates. Callers hold b.mu.
func (g turnGates) forgetRoom(room string) {
	for k := range g.sessions {
		if k.room == room {
			delete(g.sessions, k)
		}
	}
	for k := range g.agents {
		if k.room == room {
			delete(g.agents, k)
		}
	}
	for k := range g.workspaces {
		if k.room == room {
			delete(g.workspaces, k)
		}
	}
}

// agentTurns is how many turns the drive's agent may run at once.
func (d *drive) agentTurns() int {
	if d.caps.maxTurns > 0 {
		return d.caps.maxTurns
	}
	return defaultAgentTurns
}

// enterTurn passes the drive's session and agent gates, waiting as long as
// it takes (or until ctx ends), and returns the leave for both.
func (b *Bridge) enterTurn(ctx context.Context, d *drive) (func(), error) {
	key := d.key()
	agentKey := driveKey{room: key.room, core: key.core, provider: key.provider}
	b.mu.Lock()
	session := b.gates.sessions[d.sessionKey()]
	if session == nil {
		session = make(gate, 1)
		b.gates.sessions[d.sessionKey()] = session
	}
	agent := b.gates.agents[agentKey]
	if n := d.agentTurns(); cap(agent) != n {
		// The core's limit changed (or this is the agent's first turn): the
		// turns running hold the gate they entered and leave it.
		agent = make(gate, n)
		b.gates.agents[agentKey] = agent
	}
	b.mu.Unlock()
	if err := session.enter(ctx); err != nil {
		return nil, err
	}
	if err := agent.enter(ctx); err != nil {
		session.leave()
		return nil, err
	}
	return func() { agent.leave(); session.leave() }, nil
}

// workspaceGate is the gate of slot k's workspace.
func (b *Bridge) workspaceGate(k driveKey) gate {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.gates.workspaces[k]
	if g == nil {
		g = make(gate, 1)
		b.gates.workspaces[k] = g
	}
	return g
}
