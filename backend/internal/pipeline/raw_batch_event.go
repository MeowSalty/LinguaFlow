package pipeline

import (
	"context"
	"strings"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

// A diagnostic belongs to the live request session. Restored captures can be
// promoted without replaying telemetry or retaining prompts in the store.
type stagedBatchDiagnostic struct {
	event          progress.BatchEvent
	observer       progress.BatchObserver
	bounds         *backend.BuildBounds
	coreAllocation *backend.OwnedAllocation
}

// IDs are part of the admitted input snapshot. Optional detail may run out of
// capacity later, while the request identity remains available for its outcome.
func (input *preparedMainDocument) prepareDiagnosticIDs() error {
	bytes, err := backend.AllocationBytes(int64(len(input.targets)), int64(unsafe.Sizeof("")))
	if err != nil {
		return err
	}
	bytes, err = backend.CheckedAdd(bytes, int64(unsafe.Sizeof(stagedBatchDiagnostic{})+unsafe.Sizeof(int(0))+unsafe.Sizeof("")))
	if err != nil {
		return err
	}
	for _, local := range input.targets {
		bytes, err = backend.CheckedAdd(bytes, int64(len(input.doc.Segments[local].ID)))
		if err != nil {
			return err
		}
	}
	credit, err := input.bounds.OwnAllocation(bytes, 1)
	if err != nil {
		return err
	}
	if err := credit.DetachBuild(); err != nil {
		credit.Release()
		return err
	}
	input.diagnosticAllocation = credit
	input.diagnosticIDs = make([]string, len(input.targets))
	for i, local := range input.targets {
		input.diagnosticIDs[i] = strings.Clone(input.doc.Segments[local].ID)
	}
	return nil
}

func prepareMainBatchDiagnostic(ctx context.Context, session *requestSession, input *preparedMainDocument, req backend.Request, name string, glossary []prompt.GlossaryEntry) {
	observer, _ := session.reporter.(progress.BatchObserver)
	pool := session.intent.Pool
	d := &stagedBatchDiagnostic{
		observer:       observer,
		coreAllocation: input.diagnosticAllocation,
		event: progress.BatchEvent{
			Stage: session.mode, BackendName: name, TriedBackends: []string{name},
			RoundIndex: session.intent.RoundIndex, Attempt: session.intent.Attempt,
			SegmentIDs: input.diagnosticIDs, SegmentCount: len(input.targets),
			ParentRequestID: session.intent.ID, PoolIndex: &pool, ResponseFormat: req.ResponseFormat,
		},
	}
	input.diagnosticIDs, input.diagnosticAllocation = nil, nil
	session.mainDiagnostic = d
	if observer == nil {
		return
	}
	d.event.SystemPromptLength, d.event.SentLength = len(req.System), len(req.User)
	b, err := backend.NewBuildBoundsFromContext(ctx, session.runtime.handoffLimit(), 0)
	if err != nil {
		return
	}
	d.bounds = b
	d.event.SystemPrompt, _ = cloneBatchContent(b, req.System)
	d.event.UserMessage, _ = cloneBatchContent(b, req.User)
	d.event.SentContent = d.event.UserMessage
	if len(req.JSONSchema) > 0 {
		cloneBatchDetail(b, req.JSONSchema, &d.event.JSONSchema)
	}
	if len(glossary) > 0 {
		cloneBatchDetail(b, glossary, &d.event.UsedGlossary)
	}
}

func cloneBatchDetail(b *backend.BuildBounds, value, destination any) {
	if _, err := b.MeasureJSONLimited(value, progress.MaxSSEBatchContentBytes); err == nil {
		_ = b.CloneInto(value, destination)
	}
}

func cloneBatchContent(b *backend.BuildBounds, raw string) (string, error) {
	prefix := progress.SSEContentPrefix(raw)
	if err := b.ReserveText(int64(len(prefix)), int64(len(prefix))); err != nil {
		return "", err
	}
	return strings.Clone(prefix), nil
}

func (d *stagedBatchDiagnostic) close() {
	if d != nil {
		d.event = progress.BatchEvent{}
		d.bounds.CloseRetired()
		d.coreAllocation.Release()
	}
}

func releaseMainBatchDiagnostic(session *requestSession) {
	if session == nil {
		return
	}
	d := session.mainDiagnostic
	session.mainDiagnostic = nil
	d.close()
}

func captureMainBatchDiagnostic(session *requestSession, response CapturedResponse) {
	d := session.mainDiagnostic
	if d == nil || d.observer == nil {
		releaseMainBatchDiagnostic(session)
		return
	}
	d.event.Truncated = response.Truncated
	d.event.DurationMs = response.Record.Duration.Milliseconds()
	d.event.InputTokens = response.Record.Usage.PromptTokens
	d.event.OutputTokens = response.Record.Usage.CompletionTokens
	d.event.Status = "success"
}

func emitMainBatchEvent(ctx context.Context, session *requestSession, complete func(*progress.BatchEvent)) {
	if session == nil || session.mainDiagnostic == nil {
		return
	}
	d := session.mainDiagnostic
	session.mainDiagnostic = nil
	defer d.close()
	if d.observer == nil {
		return
	}
	complete(&d.event)
	if observer, ok := d.observer.(interface {
		OnBatchEventContext(context.Context, progress.BatchEvent)
	}); ok {
		observer.OnBatchEventContext(ctx, d.event)
	} else {
		d.observer.OnBatchEvent(d.event)
	}
}
