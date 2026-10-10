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
	stale                     map[int]bool
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
	return w.RunBatch(ctx, round, []*Candidate{c})
}

// RunBatch performs exactly one external invocation. All counters and saved
// progress remain member-local, while admission, usage and completion are shared.
func (w *AlignmentWorker) RunBatch(ctx context.Context, round Round, candidates []*Candidate) (update alignmentUpdate) {
	if len(candidates) == 0 {
		return update
	}
	backends, logicalBudget, templates, textMode, roundIndex := alignmentSettings(round.Handler)
	var remote []*Candidate
	for _, c := range candidates {
		if needsAlignmentAttempt(round, c) {
			remote = append(remote, c)
		} else {
			c.Ready = true
		}
	}
	if len(remote) > 0 {
		batched := len(remote) > 1
		c := remote[0]
		user := c.Alignment.AlignmentRequest(rawSource(&c.Segment), c.Alignment.Missing())
		system := templates.JSON
		req := backend.Request{User: user, JSONSchema: ruby.AlignmentJSONSchemaForProtocol(c.Alignment.ProtocolVersion)}
		if textMode {
			system = templates.Text
			req.ResponseFormat = "none"
			req.JSONSchema = nil
		}
		if batched {
			members := make([]ruby.AlignmentBatchMember, 0, len(remote))
			for _, member := range remote {
				members = append(members, ruby.AlignmentBatchMember{WorkID: member.WorkID, CandidateID: member.ID, Source: rawSource(&member.Segment), Alignment: member.Alignment})
			}
			var err error
			user, err = ruby.AlignmentBatchRequest(members)
			if err != nil {
				update.err = err
				return update
			}
			system = templates.BatchJSON
			req.User, req.JSONSchema = user, ruby.AlignmentBatchJSONSchema()
			if textMode {
				system = templates.BatchText
				req.JSONSchema = nil
			}
		}
		req.System = system
		session := &requestSession{runtime: w.runtime, store: w.store, reporter: w.reporter, intent: RequestIntent{
			Stage: backend.RequestStageAlignment, CandidateID: c.ID,
			Pool: c.PoolIndex, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: c.NetworkAttempt,
			Phase: "alignment", RoundIndex: roundIndex, ResourceID: storeResourceID(w.store),
		}}
		for _, member := range remote {
			session.intent.Indices = append(session.intent.Indices, member.Index)
			session.intent.Members = append(session.intent.Members, RequestMember{Index: member.Index, WorkID: member.WorkID, CandidateID: member.ID, CandidateVersion: member.Version, Pool: member.PoolIndex, LogicalAttempt: member.LogicalAttempt, NetworkAttempt: member.NetworkAttempt})
		}
		if batched {
			session.intent.CandidateID = ""
		}
		w.session = session
		callCtx := context.WithValue(ctx, requestSessionKey{}, session)
		start := time.Now()
		resp, callErr := backends[0].Translate(callCtx, req)
		if resp == nil && callErr == nil {
			callErr = &backend.EmptyResponseError{BackendName: backends[0].Name()}
		}
		if session.err != nil {
			update.err = session.err
			return update
		}
		if errors.Is(callErr, backend.ErrDispatchPaused) {
			update.paused = true
			return update
		}
		if ctx.Err() != nil {
			update.err = ctx.Err()
			return update
		}
		var batchResult ruby.AlignmentBatchResult
		if callErr == nil && batched {
			if textMode {
				batchResult = ruby.ParseAlignmentBatchText(resp.Text)
			} else {
				batchResult = ruby.ParseAlignmentBatchJSON(resp.Text)
			}
		}
		for _, c := range remote {
			c.NetworkAttempt += session.calls
			if session.lastID != "" {
				c.LastAlignmentRequestID = session.lastID
			}
			if callErr != nil {
				if backend.IsRetryable(callErr) && c.NetworkAttempt < max(1, round.Retry.MaxAttempts) {
					c.NextAttemptAt = time.Now().Add(backend.RetryDelay(round.Retry, c.NetworkAttempt-1, callErr))
				} else {
					c.Ready = true
				}
				continue
			}
			var entries []ruby.OutputEntry
			batchFault := false
			if batched {
				key := ruby.AlignmentBatchKey{WorkID: c.WorkID, CandidateID: c.ID}
				var found bool
				entries, found = batchResult.Entries[key]
				batchFault = !found || batchResult.Invalid[key]
				if batchFault {
					entries = nil
				}
			} else if c.Alignment.ProtocolVersion == ruby.ProtocolV2 {
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
			p := c.Alignment.ApplyOutput(itemIDs(c.Alignment.Missing()), entries, c.Alignment.ProtocolVersion == ruby.ProtocolV2)
			c.LogicalAttempt++
			c.NetworkAttempt = 0
			c.NextAttemptAt = time.Time{}
			remaining := len(c.Alignment.Missing())
			batchFault = batchFault || (batched && resp.Truncated && remaining > 0)
			if batchFault {
				c.ForceSingleAlignment = true
			}
			c.Ready = remaining == 0 || c.LogicalAttempt >= logicalBudget || (p.Added == 0 && !batchFault)
		}
		if resp != nil {
			update.inputTokens = resp.Usage.PromptTokens
			update.outputTokens = resp.Usage.CompletionTokens
		}
		if obs, ok := w.reporter.(progress.BatchObserver); ok {
			evt := progress.BatchEvent{Stage: "ruby_alignment", SegmentCount: len(remote), BackendName: backends[0].Name(), Status: "success", DurationMs: time.Since(start).Milliseconds(), RoundIndex: roundIndex, SystemPrompt: system, UserMessage: user, ResponseFormat: req.ResponseFormat, JSONSchema: req.JSONSchema, InputTokens: update.inputTokens, OutputTokens: update.outputTokens}
			if batched {
				evt.ProtocolDiagnostics = append([]string(nil), batchResult.Diagnostics...)
				evt.EnvelopeInvalid = batchResult.EnvelopeInvalid
				requested := make(map[ruby.AlignmentBatchKey]bool, len(remote))
				for _, member := range remote {
					requested[ruby.AlignmentBatchKey{WorkID: member.WorkID, CandidateID: member.ID}] = true
				}
				unknown := make(map[ruby.AlignmentBatchKey]bool)
				for key := range batchResult.Entries {
					if !requested[key] {
						unknown[key] = true
					}
				}
				for key := range batchResult.Invalid {
					if !requested[key] {
						unknown[key] = true
					}
				}
				evt.UnknownAlignmentMembers = len(unknown)
			}
			for i, member := range remote {
				evt.SegmentIDs = append(evt.SegmentIDs, member.Segment.ID)
				evt.VerifiedItems += len(member.Alignment.Verified)
				evt.MissingItems += len(member.Alignment.Missing())
				evt.AlignmentMembers = append(evt.AlignmentMembers, progress.AlignmentMemberEvent{WorkID: member.WorkID, CandidateID: member.ID, CandidateVersion: member.Version, ParentRequestID: member.ParentRequestID, LogicalAttempt: member.LogicalAttempt, NetworkAttempt: session.intent.Members[i].NetworkAttempt + session.calls, VerifiedItems: len(member.Alignment.Verified), MissingItems: len(member.Alignment.Missing()), ForceSingle: member.ForceSingleAlignment})
			}
			if !batched {
				evt.ParentRequestID, evt.CandidateID, evt.CandidateVersion = c.ParentRequestID, c.ID, c.Version
				evt.LogicalAttempt, evt.NetworkAttempt = c.LogicalAttempt, session.intent.NetworkAttempt+session.calls
			}
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
	for _, c := range candidates {
		if validator, ok := w.store.(interface {
			ValidateCandidate(context.Context, *Candidate) (bool, error)
		}); ok {
			valid, err := validator.ValidateCandidate(ctx, c)
			if err != nil {
				update.err = err
				return update
			}
			if !valid {
				if update.stale == nil {
					update.stale = make(map[int]bool)
				}
				update.stale[c.Index] = true
				continue
			}
		}
		if err := w.saveCandidate(ctx, c); err != nil {
			if errors.Is(err, ErrCandidateStale) {
				// Retire from the previous version after saving other members.
				// RetireStale checks the durable version if an earlier save
				// committed but its acknowledgement was lost before this edit.
				c.Version--
				if update.stale == nil {
					update.stale = make(map[int]bool)
				}
				update.stale[c.Index] = true
				continue
			}
			update.err = err
			return update
		}
	}
	return update
}

func (w *AlignmentWorker) saveCandidate(ctx context.Context, c *Candidate) error {
	c.Version++
	c.DTOVersion = CandidateDTOVersion
	data, err := c.Encode()
	if err != nil {
		return err
	}
	if err = w.runtime.Window.ReserveResize(c.ID, int64(len(data))); err != nil {
		return err
	}
	if err = w.runtime.Save(ctx, func(saveCtx context.Context) error { return w.store.Save(saveCtx, c) }); err != nil {
		return err
	}
	return w.runtime.Window.Resize(c.ID, int64(len(data)))
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
