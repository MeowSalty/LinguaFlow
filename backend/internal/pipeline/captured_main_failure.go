package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"text/template"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func capturedMainInterrupted(ctx context.Context, err error) bool {
	return errors.Is(err, backend.ErrDispatchPaused) || (ctx.Err() != nil &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)))
}

func capturedProviderFailure(ctx context.Context, err error) bool {
	if err == nil || capturedMainInterrupted(ctx, err) || storageFailure(err) {
		return false
	}
	if httpStatusFromErr(err) > 0 {
		return true
	}
	var policy *credential.AccessError
	return !errors.As(err, &policy) && !errors.Is(err, backend.ErrBuildCapacity) &&
		!errors.Is(err, backend.ErrResponseTooLarge) && !errors.Is(err, backend.ErrPipelineCapacity) &&
		!errors.Is(err, backend.ErrNoCapacityDrain) && !errors.Is(err, backend.ErrAdmissionClosed)
}

func retryCapturedMain(mode string, intent RequestIntent, retry backend.RetryPolicy, err error) bool {
	return intent.Phase != promptUpgradePhase && intent.Attempt+1 < transientBudgetFor(retry) &&
		(backend.IsRetryable(err) || mode == RoundModeRevise && errors.Is(err, context.DeadlineExceeded))
}

func fatalCapturedMain(intent RequestIntent, err error) bool {
	return intent.Phase != promptUpgradePhase && isFatalBackendError(err)
}

func capturedMainFailureCursor(mode string, intent RequestIntent, retry backend.RetryPolicy, callErr error) WorkCursor {
	maxPools := max(1, retry.MaxAttempts+1)
	if fatalCapturedMain(intent, callErr) {
		return WorkCursor{Pool: maxPools, Phase: "initial", State: "unresolved"}
	}
	if retryCapturedMain(mode, intent, retry, callErr) {
		deadline := time.Now().Add(backoffDuration(intent.Attempt, retry, callErr)).UTC().Truncate(time.Microsecond)
		return WorkCursor{Pool: intent.Pool, Attempt: intent.Attempt + 1, Phase: "initial", State: "pending", NextAttemptAt: deadline}
	}
	if intent.Pool+1 >= maxPools {
		return WorkCursor{Pool: maxPools, Phase: "initial", State: "unresolved"}
	}
	return WorkCursor{Pool: intent.Pool + 1, Phase: "initial", State: "pending"}
}

func (s *requestSession) reportCapturedMainFailure(ctx context.Context, intent RequestIntent, name string, callErr error, resp *backend.Response, duration time.Duration) {
	var segmentIDs []string
	if s.mainDiagnostic != nil {
		segmentIDs = s.mainDiagnostic.event.SegmentIDs
	}
	status := httpStatusFromErr(callErr)
	level, message := slog.LevelWarn, "backend failed for batch, deferring to next pool"
	switch {
	case fatalCapturedMain(intent, callErr):
		level, message = slog.LevelError, "backend returned fatal error"
	case s.mainFailure != nil && !s.mainFailure.NextAttemptAt.IsZero():
		message = "backend returned retryable error, will backoff and retry"
	}
	s.mainLogger.LogAttrs(ctx, level, message,
		slog.String("mode", s.mode), slog.Int("round", intent.RoundIndex), slog.Int("pool", intent.Pool),
		slog.Int("attempt", intent.Attempt), slog.String("phase", intent.Phase), slog.String("request_id", intent.ID),
		slog.String("backend", name), slog.Int("backend_id", intent.BackendID), slog.Int("batch_size", len(intent.Indices)),
		slog.Any("segment_ids", segmentIDs), slog.Int("http_status", status), slog.Any("err", callErr))
	emitMainBatchEvent(ctx, s, func(event *progress.BatchEvent) {
		event.Status = "failed"
		event.DurationMs = duration.Milliseconds()
		event.ErrorType = "backend_error"
		event.ErrorMessage = callErr.Error()
		event.HTTPStatus = status
		if resp != nil {
			event.InputTokens, event.OutputTokens = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
			event.Truncated = resp.Truncated
		}
	})
}

func reviseMainBuildFailure(ctx context.Context, err error) bool {
	return !capturedMainInterrupted(ctx, err) && !errors.Is(err, backend.ErrBuildCapacity) &&
		!errors.Is(err, backend.ErrMainBatchTooLarge) && !errors.Is(err, backend.ErrResponseTooLarge) &&
		!errors.Is(err, backend.ErrPipelineCapacity) && !errors.Is(err, backend.ErrNoCapacityDrain) &&
		!storageFailure(err) && workDeferred(err) == nil
}

func emitReviseMainBuildFailure(ctx context.Context, s *requestSession, input *preparedMainDocument, logger *slog.Logger, name, phase string, err error, start time.Time) {
	ids := segmentIDStringsFromDoc(input.doc, input.targets)
	logger.ErrorContext(ctx, "revise "+phase+" failed", "round", s.intent.RoundIndex, "pool", s.intent.Pool,
		"attempt", s.intent.Attempt, "backend", name, "segment_ids", ids, "err", err)
	if observer, ok := s.reporter.(progress.BatchObserver); ok {
		pool := s.intent.Pool
		observer.OnBatchEvent(progress.BatchEvent{
			Stage: RoundModeRevise, SegmentIDs: ids, SegmentCount: len(ids), BackendName: name, TriedBackends: []string{name},
			Status: "failed", DurationMs: time.Since(start).Milliseconds(), ErrorType: phase + "_error", ErrorMessage: err.Error(),
			RoundIndex: s.intent.RoundIndex, PoolIndex: &pool, Attempt: s.intent.Attempt,
		})
	}
}

func reviseTemplateFailure(err error) bool {
	var execErr template.ExecError
	return errors.As(err, &execErr)
}
