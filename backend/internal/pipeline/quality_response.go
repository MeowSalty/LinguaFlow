package pipeline

import (
	"context"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func setQualityResponseOutcome(evt *progress.BatchEvent, resp *backend.Response, err error, repaired []string) {
	evt.Status = "success"
	if err != nil {
		evt.Status = "failed"
		evt.ErrorType = "parse_error"
		evt.ErrorMessage = err.Error()
	}
	evt.Truncated, evt.Repaired = resp.Truncated, repaired
}

func retainStoredBatchDiagnostic(ctx context.Context, doc *Document, indices []int, reporter progress.Reporter, diagnostic *storedBatchDiagnostic, mode, name, responseFormat string, start time.Time) bool {
	if !storedResponseCaptured(ctx) {
		return false
	}
	session, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	observer, _ := reporter.(progress.BatchObserver)
	if observer == nil {
		return true
	}
	pool := session.intent.Pool
	evt := diagnostic.event
	evt.Stage, evt.BackendName, evt.TriedBackends = mode, name, []string{name}
	evt.RoundIndex, evt.Attempt = session.intent.RoundIndex, session.intent.Attempt
	evt.ParentRequestID, evt.PoolIndex = session.intent.ID, &pool
	evt.DurationMs, evt.ResponseFormat = time.Since(start).Milliseconds(), responseFormat
	evt.SegmentCount = len(indices)
	if diagnostic.bounds != nil && backend.ReserveSlice[string](diagnostic.bounds, int64(len(indices))) == nil {
		evt.SegmentIDs = segmentIDStringsFromDoc(doc, indices)
	}
	evt.SentContent = evt.UserMessage
	session.mainDiagnostic = &stagedBatchDiagnostic{event: evt, observer: observer, bounds: diagnostic.bounds}
	*diagnostic = storedBatchDiagnostic{}
	return true
}

func emitStoredBatchResponse(ctx context.Context, resp *backend.Response, err error, repaired []string, partial bool) {
	session, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	if session == nil || session.mainDiagnostic == nil {
		return
	}
	bounds := session.mainDiagnostic.bounds
	emitMainBatchEvent(ctx, session, func(evt *progress.BatchEvent) {
		setQualityResponseOutcome(evt, resp, err, repaired)
		if err == nil && partial {
			evt.Status = "partial"
		}
		evt.InputTokens, evt.OutputTokens = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		evt.ReceivedLength = len(resp.Text)
		if bounds != nil {
			_ = runStoredHandlerCPU(ctx, func() error {
				var err error
				evt.ResponseContent, err = cloneBatchContent(bounds, resp.Text)
				evt.ReceivedContent = evt.ResponseContent
				return err
			})
		}
	})
}
