package pipeline

import (
	"context"
	"errors"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// RenderStoredRequestBounded grants the exact known prompt/wire credit before
// rendering their backing buffers. The callback borrows immutable input only.
func RenderStoredRequestBounded(ctx context.Context, remote backend.Backend, req backend.Request, render func(*backend.BuildBounds, func(backend.PromptMeasure) error) (string, string, error)) (backend.Request, error) {
	runtime := ExecutionRuntimeFromContext(ctx)
	output, input := backend.DefaultMaxResponseBytes, int64(64<<20)
	if runtime != nil {
		output, input = runtime.responseLimit(), runtime.handoffLimit()
	}
	var measured backend.PromptMeasure
	pass := func(count bool) error {
		build := func() error {
			b, err := backend.NewBuildBounds(output, input)
			if err != nil {
				return err
			}
			claim := func(m backend.PromptMeasure) error {
				if count {
					measured = m
					return errMeasureOnly
				}
				if measured != m {
					return backend.ErrBuildCapacity
				}
				return nil
			}
			req.System, req.User, err = render(b, claim)
			return err
		}
		if runtime != nil {
			return runtime.Run(ctx, build)
		}
		return build()
	}
	err := pass(true)
	if !errors.Is(err, errMeasureOnly) {
		return req, err
	}
	session, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	if session != nil {
		if err = preclaimMeasured(ctx, remote, session, req, measured); err != nil {
			return req, err
		}
	}
	if err = pass(false); err != nil {
		return req, err
	}
	return req, nil
}
