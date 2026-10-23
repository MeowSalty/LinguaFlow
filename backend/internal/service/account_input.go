package service

// AccountInputError contains only validator-authored diagnostics, never the
// supplied password, email or username. It lets HTTP identify an invalid field
// without exposing arbitrary underlying database errors.
type AccountInputError struct {
	Field  string
	Reason string
}

func (e *AccountInputError) Error() string { return e.Field + ": " + e.Reason }
func (e *AccountInputError) Unwrap() error { return ErrInvalidInput }

func invalidAccountField(field string, err error) error {
	return &AccountInputError{Field: field, Reason: err.Error()}
}
