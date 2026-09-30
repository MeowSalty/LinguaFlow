package credential

import (
	"context"
	"sync"
)

type memoryEntry struct {
	provider, endpoint, secret string
	revoked                    bool
}

// Memory is a process-local registry for independent CLI execution.
type Memory struct {
	mu      sync.RWMutex
	next    int
	entries map[Binding]memoryEntry
}

func NewMemory() *Memory { return &Memory{entries: make(map[Binding]memoryEntry)} }
func (m *Memory) Register(provider, endpoint, secret string) (Binding, error) {
	ep, err := NormalizeEndpoint(provider, endpoint)
	if err != nil {
		return Binding{}, err
	}
	if secret == "" {
		return Binding{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[Binding]memoryEntry)
	}
	m.next++
	b := Binding{m.next, 1}
	m.entries[b] = memoryEntry{provider: provider, endpoint: ep, secret: secret}
	return b, nil
}
func (m *Memory) Resolve(ctx context.Context, b Binding, provider, endpoint string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ep, err := NormalizeEndpoint(provider, endpoint)
	if err != nil {
		return "", err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[b]
	if !ok {
		return "", ErrUnavailable
	}
	if e.revoked {
		return "", ErrRevoked
	}
	if e.provider != provider || e.endpoint != ep {
		return "", ErrEndpoint
	}
	return e.secret, nil
}
func (m *Memory) Check(ctx context.Context, b Binding, _ int, provider, endpoint string) error {
	_, err := m.Resolve(ctx, b, provider, endpoint)
	return err
}
func (m *Memory) Revoke(b Binding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[b]
	if !ok {
		return ErrUnavailable
	}
	e.revoked = true
	e.secret = ""
	m.entries[b] = e
	return nil
}

// Close drops process-local secrets and invalidates clients still holding a
// binding. It does not promise erasure of Go runtime copies already in memory.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.entries)
	return nil
}
