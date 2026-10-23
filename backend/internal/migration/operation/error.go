// Package operation owns durable offline plans, receipts and filesystem boundaries.
package operation

import "fmt"

type Error struct {
	Code string `json:"code"`
	Next string `json:"next"`
}

func (e *Error) Error() string     { return fmt.Sprintf("%s: %s", e.Code, e.Next) }
func Fail(code, next string) error { return &Error{Code: code, Next: next} }

const (
	UnsupportedSource = "unsupported_source"
	UnsupportedTarget = "unsupported_target"
	BaselineChanged   = "baseline_changed"
	MappingRequired   = "mapping_required"
	BackupIncomplete  = "backup_incomplete"
	TargetNotEmpty    = "target_not_empty"
	Incompatible      = "operation_incompatible"
	NeedsAction       = "needs_action"
)
