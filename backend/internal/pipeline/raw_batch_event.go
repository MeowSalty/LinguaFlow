package pipeline

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"strconv"
)

func emitRawBatchEvent(ctx context.Context, session *requestSession, req backend.Request, response MainResponse, name string) error {
	observer, ok := session.reporter.(progress.BatchObserver)
	if !ok {
		return nil
	}
	var event progress.BatchEvent
	err := session.runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBounds(32<<10, response.FreezeBytes+response.ResponseBytes)
		if err != nil {
			return err
		}
		preview := func(s string) (string, error) {
			n := min(len(s), progress.MaxSSEBatchContentBytes)
			if err := b.ReserveText(int64(n), int64(n)); err != nil {
				return "", err
			}
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
		if err = b.ReserveText(response.ResponseBytes, response.ResponseBytes); err != nil {
			return err
		}
		raw := string(response.Raw)
		received, err := preview(raw)
		if err != nil {
			return err
		}
		if err = b.ReserveNodes(1, 1024); err != nil {
			return err
		}
		if err = b.ReserveNodes(int64(len(response.Members)), 64); err != nil {
			return err
		}
		ids := make([]string, len(response.Members))
		for i, m := range response.Members {
			ids[i] = strconv.Itoa(m.SegmentID)
		}
		stage := session.mode
		event = progress.BatchEvent{Stage: stage, SegmentIDs: ids, SegmentCount: len(ids), BackendName: name, Status: "success", DurationMs: response.Record.Duration.Milliseconds(), InputTokens: response.Record.Usage.PromptTokens, OutputTokens: response.Record.Usage.CompletionTokens, SentContent: user, ReceivedContent: received, Truncated: response.Truncated, RoundIndex: session.intent.RoundIndex, Attempt: session.intent.Attempt, SystemPrompt: system, UserMessage: user, ResponseFormat: req.ResponseFormat, ResponseContent: received}
		return nil
	})
	if err != nil {
		return err
	}
	observer.OnBatchEvent(event)
	return nil
}
