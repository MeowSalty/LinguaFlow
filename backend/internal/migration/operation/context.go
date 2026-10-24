package operation

import "context"

// ContextCheckpoint observes cancellation on both sides of a durable boundary.
// A committed database receipt stays authoritative if cancellation prevents the
// subsequent filesystem checkpoint from being published.
func ContextCheckpoint(ctx context.Context, checkpoint func(string) error) func(string) error {
	return func(phase string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if checkpoint != nil {
			if err := checkpoint(phase); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
}
