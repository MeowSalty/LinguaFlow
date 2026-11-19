package pipeline

import (
	"context"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"log/slog"
)

type ResponseAdvanceKind uint8

const (
	ResponseAdvanced ResponseAdvanceKind = iota + 1
	ResponseDeferred
	ResponseTerminal
)

type ResponseAdvance struct {
	Header MainResponseHeader
	Kind   ResponseAdvanceKind
	Wait   backend.AdmissionWait
	event  stagedEvent
}

// AdvanceResponse preserves the last confirmed header even when later work waits or fails.
func AdvanceResponse(ctx context.Context, round Round, h stagedHandler, header MainResponseHeader, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger) (ResponseAdvance, error) {
	if !header.Phase.Valid() {
		return ResponseAdvance{Header: header}, fmt.Errorf("invalid response phase %q", header.Phase)
	}
	if header.Phase.Terminal() {
		return ResponseAdvance{Header: header, Kind: ResponseTerminal}, nil
	}
	e := advanceArchivedResponse(ctx, round, h, header, store, runtime, logger)
	if e.responseAdvance != nil {
		header = *e.responseAdvance
	}
	out := ResponseAdvance{Header: header, event: e}
	if d := workDeferred(e.err); d != nil {
		e.err = nil
		e.waitCapacity = d
	}
	if e.err != nil {
		out.event = e
		return out, e.err
	}
	if e.waitCapacity != nil {
		out.Kind = ResponseDeferred
		out.Wait = e.waitCapacity.Wait
		out.event = e
		return out, nil
	}
	out.Kind = ResponseAdvanced
	if header.Phase.Terminal() {
		out.Kind = ResponseTerminal
	}
	out.event = e
	return out, nil
}
