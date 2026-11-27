package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type capturedRoundHandler interface {
	batchRoundHandler
	storedResponseHandler
}

func runStoredRound(ctx context.Context, round Round, doc *Document, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger, reporter progress.Reporter) (result RunRoundResult, err error) {
	ctx = WithExecutionRuntime(ctx, round.Runtime)
	if logger == nil {
		logger = slog.Default()
	}
	if reporter == nil {
		reporter = progress.Nop{}
	}
	if round.Shrink == 0 {
		round.Shrink = 1
	}
	if round.Store == nil {
		memory := NewMemoryRoundStore(nil)
		defer func() { err = errors.Join(err, memory.Close()) }()
		round.Store = memory
	}
	round.Gate = round.Runtime.Gate
	return runCanonicalStoredRound(ctx, round, doc, batchHandler, logger, reporter)
}

func emitStoredPoolAdvance(reporter progress.Reporter, poolEvent progress.PoolEvent, pending int) {
	if poolEvent.Batches == 0 || pending == 0 {
		return
	}
	poolEvent.Phase = "pool_advance"
	poolEvent.Pending = pending
	emitPoolEvent(reporter, poolEvent)
}

func storedDispatchDeferred(ctx context.Context, err error) bool {
	runtime := ExecutionRuntimeFromContext(ctx)
	if err == nil || runtime == nil {
		return false
	}
	if errors.Is(err, backend.ErrDispatchPaused) {
		if runtime.Gate != nil {
			runtime.Gate.Pause()
		}
		return true
	}
	ctxErr := ctx.Err()
	return ctxErr != nil && errors.Is(err, ctxErr) && !storageFailure(err)
}

func storedProviderFailure(ctx context.Context) error {
	if session, ok := ctx.Value(requestSessionKey{}).(*requestSession); ok {
		return session.providerFailure
	}
	return nil
}

func observedStoredProviderFailure(ctx context.Context, remote backend.Backend) bool {
	_, bound := remote.(*requestBackend)
	session, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	return !bound || session == nil || session.providerFailure != nil
}

// storedBackoffRetry moves runtime-managed waiting after durable cursor saving.
// Legacy callers keep their existing inline wait without a request session.
func storedBackoffRetry(ctx context.Context, indices []int, attempt int, wait time.Duration) *batchJob {
	if _, ok := ctx.Value(requestSessionKey{}).(*requestSession); !ok {
		return nil
	}
	return &batchJob{idxs: append([]int(nil), indices...), attempt: attempt + 1, notBefore: time.Now().Add(wait)}
}

func containsIndex(indices []int, index int) bool {
	for _, idx := range indices {
		if idx == index {
			return true
		}
	}
	return false
}

func handlerRoundIndex(handler RoundHandler) int {
	switch h := handler.(type) {
	case *TranslateHandler:
		return h.RoundIndex
	case *ReviseHandler:
		return h.RoundIndex
	case *ExtractHandler:
		return h.RoundIndex
	case *SemanticQAHandler:
		return h.RoundIndex
	case *AdjudicateHandler:
		return h.RoundIndex
	default:
		return 0
	}
}
