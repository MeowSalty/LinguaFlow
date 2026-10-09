package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// AlignmentWorker performs one remote alignment attempt and saves its verified
// progress on a private candidate. It never renders or commits accepted content.
// Each invocation owns one worker; the coordinator must defer finish before Run
// and retain that completion ownership through finalization and confirmation.
type AlignmentWorker struct {
	store    RoundStore
	runtime  *ExecutionRuntime
	reporter progress.Reporter
	session  *requestSession
}

type alignmentUpdate struct {
	inputTokens, outputTokens int64
	paused                    bool
	err                       error
}

// finish propagates any later finalization, retirement or commit failure to the
// request before marking it completed and releasing response-processing capacity.
func (w *AlignmentWorker) finish(err error) error {
	if w.session == nil {
		return nil
	}
	if err != nil {
		w.session.fail(err)
	}
	return w.session.finish()
}

func (w *AlignmentWorker) Run(ctx context.Context, round Round, c *Candidate) (update alignmentUpdate) {
	backends, logicalBudget, templates, textMode, roundIndex := alignmentSettings(round.Handler)
	if len(backends) == 0 || c.LogicalAttempt >= logicalBudget || c.NetworkAttempt >= max(1, round.Retry.MaxAttempts) {
		c.Ready = true
	} else {
		missing := c.Alignment.Missing()
		user := c.Alignment.AlignmentRequest(ruby.StripRubyTags(rawSource(&c.Segment)), missing)
		system := templates.JSON
		req := backend.Request{User: user, JSONSchema: ruby.AlignmentJSONSchemaForProtocol(c.Alignment.ProtocolVersion)}
		if textMode {
			system = templates.Text
			req.ResponseFormat = "none"
			req.JSONSchema = nil
		}
		req.System = system
		session := &requestSession{runtime: w.runtime, store: w.store, reporter: w.reporter, intent: RequestIntent{
			Stage: backend.RequestStageAlignment, Indices: []int{c.Index}, CandidateID: c.ID,
			Pool: c.PoolIndex, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: c.NetworkAttempt,
			Phase: "alignment", RoundIndex: roundIndex, ResourceID: storeResourceID(w.store),
		}}
		w.session = session
		callCtx := context.WithValue(ctx, requestSessionKey{}, session)
		start := time.Now()
		resp, callErr := backends[0].Translate(callCtx, req)
		if session.err != nil {
			update.err = session.err
			return update
		}
		if errors.Is(callErr, backend.ErrDispatchPaused) {
			update.paused = true
			return update
		}
		c.NetworkAttempt += session.calls
		if ctx.Err() != nil {
			update.err = ctx.Err()
			return update
		}
		if callErr != nil {
			if backend.IsRetryable(callErr) && c.NetworkAttempt < max(1, round.Retry.MaxAttempts) {
				c.NextAttemptAt = time.Now().Add(backend.RetryDelay(round.Retry, c.NetworkAttempt-1, callErr))
			} else {
				c.Ready = true
			}
		} else {
			var entries []ruby.OutputEntry
			if c.Alignment.ProtocolVersion == ruby.ProtocolV2 {
				if textMode {
					entries = ruby.ParseAlignmentTextV2(resp.Text)
				} else {
					entries, _ = ruby.ParseAlignmentJSONV2(resp.Text)
				}
			} else {
				if textMode {
					entries = ruby.ParseAlignmentTextLegacy(resp.Text)
				} else {
					entries, _, _ = repair.TryRepairRubyAlignmentForState(resp.Text, repairOptions(round.Handler))
				}
			}
			p := c.Alignment.ApplyOutput(itemIDs(missing), entries, c.Alignment.ProtocolVersion == ruby.ProtocolV2)
			c.LogicalAttempt++
			c.NetworkAttempt = 0
			c.NextAttemptAt = time.Time{}
			c.Ready = p.Added == 0 || len(c.Alignment.Missing()) == 0 || c.LogicalAttempt >= logicalBudget
		}
		if resp != nil {
			update.inputTokens = resp.Usage.PromptTokens
			update.outputTokens = resp.Usage.CompletionTokens
		}
		if obs, ok := w.reporter.(progress.BatchObserver); ok {
			evt := progress.BatchEvent{Stage: "ruby_alignment", SegmentIDs: []string{c.Segment.ID}, SegmentCount: 1, BackendName: backends[0].Name(), Status: "success", DurationMs: time.Since(start).Milliseconds(), RoundIndex: roundIndex, ParentRequestID: c.ParentRequestID, CandidateID: c.ID, CandidateVersion: c.Version, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: session.intent.NetworkAttempt + session.calls, MissingItems: len(c.Alignment.Missing()), SystemPrompt: system, UserMessage: user, ResponseFormat: req.ResponseFormat, JSONSchema: req.JSONSchema, InputTokens: update.inputTokens, OutputTokens: update.outputTokens}
			evt.VerifiedItems = len(c.Alignment.Verified)
			if resp != nil {
				evt.ResponseContent = resp.Text
				evt.ReceivedContent = resp.Text
				evt.Truncated = resp.Truncated
			}
			if callErr != nil {
				evt.Status = "failed"
				evt.ErrorType = "backend_error"
				evt.ErrorMessage = callErr.Error()
				evt.HTTPStatus = httpStatusFromErr(callErr)
			} else if evt.MissingItems > 0 {
				evt.Status = "partial"
			}
			obs.OnBatchEvent(evt)
		}
	}
	c.Version++
	data, err := c.Encode()
	if err != nil {
		update.err = err
		return update
	}
	if err = w.runtime.Window.ReserveResize(c.ID, int64(len(data))); err != nil {
		update.err = err
		return update
	}
	if err = w.runtime.Save(ctx, func(saveCtx context.Context) error { return w.store.Save(saveCtx, c) }); err != nil {
		update.err = err
		return update
	}
	update.err = w.runtime.Window.Resize(c.ID, int64(len(data)))
	return update
}

func alignmentSettings(handler RoundHandler) ([]backend.Backend, int, prompt.RubyTemplates, bool, int) {
	switch h := handler.(type) {
	case *TranslateHandler:
		return h.RubyRetryBackends, h.RubyRetryAttempts, h.RubyTemplates, prompt.ProtocolFromResponseMode(h.ResponseMode).IsText(), h.RoundIndex
	case *ReviseHandler:
		return h.RubyRetryBackends, h.RubyRetryAttempts, h.RubyTemplates, prompt.ProtocolFromResponseMode(h.ResponseMode).IsText(), h.RoundIndex
	}
	return nil, 0, prompt.RubyTemplates{}, false, 0
}

func repairOptions(handler RoundHandler) repair.Options {
	switch h := handler.(type) {
	case *TranslateHandler:
		return h.Repair
	case *ReviseHandler:
		return h.Repair
	}
	return repair.Options{}
}
