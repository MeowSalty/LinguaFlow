package pipeline

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"strconv"
)

func emitRawBatchEvent(ctx context.Context, session *requestSession, req backend.Request, response CapturedResponse, name string) error {
	observer, ok := session.reporter.(progress.BatchObserver)
	if !ok {
		return nil
	}
	var event progress.BatchEvent
	var bounds *backend.BuildBounds
	defer func() {
		event = progress.BatchEvent{}
		bounds.CloseRetired()
	}()
	err := session.runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, 32<<10, response.InputMemberBytes+response.CaptureBytes)
		if err != nil {
			return err
		}
		bounds = b
		// Observers may retain these fields directly. Their external lifetime is
		// unknown here, so it cannot participate in this temporary live gate.
		preview := func(s string) (string, error) {
			v, _, _ := progress.TruncateSSEContent(s)
			return v, nil
		}
		system, err := preview(req.System)
		if err != nil {
			return err
		}
		user, err := preview(req.User)
		if err != nil {
			return err
		}
		rawCredit, err := b.OwnAllocation(int64(len(response.Payload)), 1)
		if err != nil {
			return err
		}
		raw := string(response.Payload)
		received, err := preview(raw)
		raw = ""
		rawCredit.Release()
		if err != nil {
			return err
		}
		ids := make([]string, len(session.intent.InputMembers))
		for i, m := range session.intent.InputMembers {
			ids[i] = strconv.Itoa(m.SegmentID)
		}
		stage := session.mode
		event = progress.BatchEvent{Stage: stage, SegmentIDs: ids, SegmentCount: len(ids), BackendName: name, Status: "success", DurationMs: response.Record.Duration.Milliseconds(), InputTokens: response.Record.Usage.PromptTokens, OutputTokens: response.Record.Usage.CompletionTokens, SentContent: user, ReceivedContent: received, Truncated: response.Truncated, RoundIndex: session.intent.RoundIndex, Attempt: session.intent.Attempt, SystemPrompt: system, UserMessage: user, ResponseFormat: req.ResponseFormat, ResponseContent: received}
		return nil
	})
	if err != nil {
		return err
	}
	if contextual, ok := observer.(interface {
		OnBatchEventContext(context.Context, progress.BatchEvent)
	}); ok {
		contextual.OnBatchEventContext(ctx, event)
	} else {
		observer.OnBatchEvent(event)
	}
	return nil
}
