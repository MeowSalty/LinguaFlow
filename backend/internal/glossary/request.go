package glossary

import "context"

// RequestAdder persists a request's inline glossary result together with its
// entries. Replaying the same request returns the original conflict decisions.
type RequestAdder interface {
	AddForRequest(ctx context.Context, requestID string, entries ...Entry) (AddResult, error)
}

// AddForRequest preserves the simple in-memory/file/preview implementations.
// Database-backed jobs provide RequestAdder for durable idempotence.
func AddForRequest(ctx context.Context, target Glossary, requestID string, entries ...Entry) (AddResult, error) {
	if target == nil {
		return AddResult{}, nil
	}
	if requestID != "" {
		if aware, ok := target.(RequestAdder); ok {
			return aware.AddForRequest(ctx, requestID, entries...)
		}
	}
	return target.Add(ctx, entries...)
}
