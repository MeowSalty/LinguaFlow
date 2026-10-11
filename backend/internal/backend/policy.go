package backend

import "context"

// PolicyBackend checks execution validity outside capacity admission. Its HTTP
// client must also have an attempt-level guard to cover SDK retries/redirects.
type PolicyBackend struct {
	inner Backend
	check func(context.Context) error
}

func NewPolicyBackend(inner Backend, check func(context.Context) error) *PolicyBackend {
	return &PolicyBackend{inner: inner, check: check}
}
func (b *PolicyBackend) Name() string     { return b.inner.Name() }
func (b *PolicyBackend) Backend() Backend { return b.inner }
func (b *PolicyBackend) Close() error     { return b.inner.Close() }

// Check lets outer joint admission retain the same policy-before-capacity
// boundary and prefer a revocation/deletion error over a missing RPM handle.
func (b *PolicyBackend) Check(ctx context.Context) error { return b.check(ctx) }
func (b *PolicyBackend) Translate(ctx context.Context, req Request) (*Response, error) {
	if err := b.check(ctx); err != nil {
		return nil, err
	}
	response, err := b.inner.Translate(ctx, req)
	if err != nil {
		if policyErr := b.check(ctx); policyErr != nil {
			return nil, policyErr
		}
	}
	return response, err
}
