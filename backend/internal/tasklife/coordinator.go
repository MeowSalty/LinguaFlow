// Package tasklife coordinates task controls with queue ownership in one server.
package tasklife

import (
	"context"
	"errors"
	"sync"
)

var ErrBusy = errors.New("task execution is still active")

type key struct {
	kind string
	id   int
}

type entry struct {
	gate   chan struct{}
	refs   int
	active int
}

// Coordinator has a usable zero value. A control guard is short lived; a claim
// remains active through admission, waiting, execution, and writer shutdown.
type Coordinator struct {
	mu      sync.Mutex
	entries map[key]*entry
}

type Guard struct {
	owner *Coordinator
	key   key
	entry *entry
	once  sync.Once
}

// Lock serializes control and registration for one task. Never acquire this
// lock while holding a database transaction or a queue mutex.
func (c *Coordinator) Lock(ctx context.Context, kind string, id int) (*Guard, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Optional injection keeps standalone/offline users independent of a server.
	if c == nil {
		return &Guard{}, nil
	}
	k := key{kind: kind, id: id}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[key]*entry)
	}
	e := c.entries[k]
	if e == nil {
		e = &entry{gate: make(chan struct{}, 1)}
		e.gate <- struct{}{}
		c.entries[k] = e
	}
	e.refs++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		c.unref(k, e, false)
		return nil, ctx.Err()
	case <-e.gate:
		if err := ctx.Err(); err != nil {
			e.gate <- struct{}{}
			c.unref(k, e, false)
			return nil, err
		}
		return &Guard{owner: c, key: k, entry: e}, nil
	}
}

func (c *Coordinator) unref(k key, e *entry, claim bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim {
		e.active--
	}
	e.refs--
	if e.refs == 0 {
		delete(c.entries, k)
	}
}

// Busy is an advisory snapshot. Deletion must use a Guard through commit.
func (c *Coordinator) Busy(kind string, id int) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key{kind: kind, id: id}]
	return e != nil && e.active > 0
}

func (g *Guard) Active() bool {
	if g.owner == nil {
		return false
	}
	g.owner.mu.Lock()
	defer g.owner.mu.Unlock()
	return g.entry.active > 0
}

// Claim must be called while holding the guard. Its release is idempotent and
// cannot release a later execution's ownership.
func (g *Guard) Claim() func() {
	if g.owner == nil {
		return func() {}
	}
	g.owner.mu.Lock()
	g.entry.active++
	g.entry.refs++
	g.owner.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { g.owner.unref(g.key, g.entry, true) }) }
}

func (g *Guard) Release() {
	if g == nil || g.owner == nil {
		return
	}
	g.once.Do(func() {
		g.entry.gate <- struct{}{}
		g.owner.unref(g.key, g.entry, false)
	})
}
