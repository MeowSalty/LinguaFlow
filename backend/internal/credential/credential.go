// Package credential keeps provider secrets outside execution configuration.
package credential

import (
	"context"
	"errors"
)

type Binding struct {
	ID      int `json:"id" yaml:"id"`
	Version int `json:"version" yaml:"version"`
}

func (b Binding) Valid() bool { return b.ID > 0 && b.Version > 0 }

// Access errors are terminal execution policy failures, not upstream outages.
type AccessError struct{ reason string }

func (e *AccessError) Error() string { return e.reason }
func (*AccessError) Permanent() bool { return true }

var (
	ErrUnavailable    = &AccessError{"credential version is unavailable"}
	ErrRevoked        = &AccessError{"credential version has been revoked"}
	ErrBackendDeleted = &AccessError{"backend no longer exists"}
	ErrEndpoint       = &AccessError{"credential endpoint binding mismatch"}
	ErrOwnership      = &AccessError{"credential ownership mismatch"}
	ErrInvalid        = errors.New("invalid credential input")
	ErrInUse          = errors.New("credential version is still referenced")
	ErrKeyUnavailable = errors.New("credential encryption key is unavailable")
	ErrDecrypt        = errors.New("credential authentication failed")
)

// Reader resolves only an explicitly bound version; it never selects current.
type Reader interface {
	Resolve(context.Context, Binding, string, string) (string, error)
}

// Checker is invoked immediately before every HTTP attempt, including SDK
// retries and redirects. BackendID zero denotes a process-local CLI binding.
type Checker interface {
	Check(context.Context, Binding, int, string, string) error
}
