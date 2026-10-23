package authhttp

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ErrGateBlocked is returned when the caller attempts to enter a connection
// key that a lifecycle mutation has sealed.
var ErrGateBlocked = errors.New("authentication connection activity is blocked")

type gateContextKey struct{}

type gateInfo struct {
	gate  *Gate
	key   string
	check func(context.Context) error
}

// Gate serialises per-connection external activity against lifecycle
// mutations. Every key tracks in-flight activities; Block seals the key,
// cancels in-flight activities, waits for them to drain, and returns an
// unblock func scoped to that specific sealing round via a generation
// counter so stale unblocks cannot reopen a newer Block.
type Gate struct {
	mu   sync.Mutex
	keys map[string]*gateKeyState
}

type gateKeyState struct {
	blocked    bool
	generation uint64
	drained    chan struct{}
	active     map[*activity]struct{}
	blockDone  chan struct{}
}

type activity struct {
	cancel context.CancelFunc
	once   sync.Once
}

// NewGate creates an empty connection activity gate.
func NewGate() *Gate {
	return &Gate{keys: make(map[string]*gateKeyState)}
}

// Enter registers a cancellable activity for the key BEFORE running the
// admission check so a concurrent Block observes it and waits for release.
// On check failure or activity-context cancellation the activity is released.
func (g *Gate) Enter(ctx context.Context, key string, check func(context.Context) error) (context.Context, func(), error) {
	g.mu.Lock()
	state := g.keys[key]
	if state == nil {
		state = &gateKeyState{active: make(map[*activity]struct{})}
		g.keys[key] = state
	}
	if state.blocked {
		g.mu.Unlock()
		return nil, nil, ErrGateBlocked
	}
	actCtx, cancel := context.WithCancel(ctx)
	act := &activity{cancel: cancel}
	state.active[act] = struct{}{}
	g.mu.Unlock()

	release := func() {
		act.once.Do(func() {
			cancel()
			g.mu.Lock()
			delete(state.active, act)
			if len(state.active) == 0 && state.drained != nil {
				close(state.drained)
				state.drained = nil
			}
			g.cleanupLocked(state, key)
			g.mu.Unlock()
		})
	}

	if check != nil {
		// pass actCtx so Block cancellation also aborts the admission check
		if err := check(actCtx); err != nil {
			release()
			return nil, nil, err
		}
	}
	// Block may have sealed and cancelled actCtx while check was in flight.
	if err := actCtx.Err(); err != nil {
		release()
		return nil, nil, err
	}

	return actCtx, release, nil
}

// Block seals the key against new Enter calls, cancels in-flight activities,
// and waits for drain. If another Block is already in progress the caller
// waits until it completes (or its context expires) and then acquires the
// block itself. The unblock closure only releases the round it was created
// for; stale unblocks are no-ops and safe to call multiple times.
func (g *Gate) Block(ctx context.Context, key string) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g.mu.Lock()
		state := g.keys[key]
		if state == nil {
			state = &gateKeyState{active: make(map[*activity]struct{})}
			g.keys[key] = state
		}
		if state.blocked {
			done := state.blockDone
			g.mu.Unlock()
			if done == nil {
				done = make(chan struct{})
				close(done)
			}
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}

		state.generation++
		gen := state.generation
		state.blocked = true
		for act := range state.active {
			act.cancel()
		}
		var drained chan struct{}
		if len(state.active) > 0 {
			drained = make(chan struct{})
			state.drained = drained
		}
		blockDone := make(chan struct{})
		state.blockDone = blockDone
		g.mu.Unlock()

		if drained != nil {
			select {
			case <-drained:
				// The context may have expired while waiting for drain;
				// roll back this round before returning success.
				if err := ctx.Err(); err != nil {
					g.mu.Lock()
					if state.generation == gen && state.blocked {
						state.blocked = false
						state.drained = nil
						close(blockDone)
						g.cleanupLocked(state, key)
					}
					g.mu.Unlock()
					return nil, err
				}
			case <-ctx.Done():
				g.mu.Lock()
				if state.generation == gen && state.blocked {
					state.blocked = false
					state.drained = nil
					close(blockDone)
					g.cleanupLocked(state, key)
				}
				g.mu.Unlock()
				return nil, ctx.Err()
			}
		}

		unblock := func() {
			g.mu.Lock()
			if state.generation == gen && state.blocked {
				state.blocked = false
				state.drained = nil
				close(blockDone)
				g.cleanupLocked(state, key)
			}
			g.mu.Unlock()
		}
		return unblock, nil
	}
}

// cleanupLocked deletes the key state when it is fully idle (not blocked and
// no active activities) so long-lived gates do not accumulate dead entries.
func (g *Gate) cleanupLocked(state *gateKeyState, key string) {
	if !state.blocked && len(state.active) == 0 && g.keys[key] == state {
		delete(g.keys, key)
	}
}

// WithGate stores the gate, key, and admission check in the context so the
// transport can enforce admission before each outbound request.
func WithGate(ctx context.Context, gate *Gate, key string, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, gateContextKey{}, &gateInfo{gate: gate, key: key, check: check})
}

func gateFromContext(ctx context.Context) *gateInfo {
	info, _ := ctx.Value(gateContextKey{}).(*gateInfo)
	return info
}

// gatedBody releases the gate activity when the response body reaches EOF,
// encounters a read error, or is explicitly closed.
type gatedBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *gatedBody) doRelease() {
	b.once.Do(func() {
		if b.release != nil {
			b.release()
		}
	})
}

func (b *gatedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.doRelease()
	}
	return n, err
}

func (b *gatedBody) Close() error {
	err := b.ReadCloser.Close()
	b.doRelease()
	return err
}
