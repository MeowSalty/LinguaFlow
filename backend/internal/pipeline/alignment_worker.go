package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
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
	waitCapacity              *WorkDeferred
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
	backends, budget, templates, textMode, roundIndex := alignmentSettings(round.Handler)
	var remote []*Candidate
	for _, c := range candidates {
		if needsAlignmentAttempt(round, c) {
			remote = append(remote, c)
		}
	}
	var resp *backend.Response
	var callErr error
	var user, system string
	req := backend.Request{}
	var requestedMembers []progress.AlignmentMemberEvent
	var diagnostics []string
	var envelopeInvalid bool
	var unknownMembers int
	var start time.Time
	if len(remote) > 0 {
		batched := len(remote) > 1
		first := remote[0]
		system = templates.JSON
		if textMode {
			system = templates.Text
			req.ResponseFormat = "none"
		}
		if batched {
			system = templates.BatchJSON
			if textMode {
				system = templates.BatchText
			}
		}
		session := &requestSession{runtime: w.runtime, store: w.store, reporter: w.reporter, intent: RequestIntent{Stage: backend.RequestStageAlignment, CandidateID: first.ID, Pool: first.PoolIndex, LogicalAttempt: first.LogicalAttempt, NetworkAttempt: first.NetworkAttempt, Phase: "alignment", RoundIndex: roundIndex, ResourceID: storeResourceID(w.store)}}
		for _, c := range remote {
			session.intent.Indices = append(session.intent.Indices, c.Index)
			session.intent.Members = append(session.intent.Members, RequestMember{Bytes: c.StoredBytes, Index: c.Index, WorkID: c.WorkID, CandidateID: c.ID, CandidateVersion: c.Version, Pool: c.PoolIndex, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: c.NetworkAttempt})
		}
		if batched {
			session.intent.CandidateID = ""
		}
		w.session = session
		frames := make([]CandidateFrame, len(remote))
		for i, c := range remote {
			f, e := loadCandidateFrame(ctx, w.store, c.ID, w.runtime.Window.limits.ItemBytes)
			if e != nil {
				update.err = e
				return update
			}
			if f.Version != c.Version {
				update.err = ErrCandidateStale
				return update
			}
			frames[i] = f
			w.runtime.Admission.ObserveOwned("candidate", c.ID+"/aux-input", int64(cap(f.Payload)))
			defer w.runtime.Admission.ObserveOwned("candidate", c.ID+"/aux-input", 0)
		}
		var measured backend.PromptMeasure
		render := func(count bool) (string, error) {
			var user string
			err := w.runtime.Run(ctx, func() error {
				page := int64(len(system))
				for _, f := range frames {
					var e error
					page, e = boundedSum(page, int64(len(f.Payload)))
					if e != nil {
						return e
					}
				}
				b, e := backend.NewBuildBounds(w.runtime.Window.limits.ItemBytes, page)
				if e != nil {
					return e
				}
				if e = b.ReserveNodes(int64(len(frames)), 64); e != nil {
					return e
				}
				members := make([]ruby.AlignmentBatchMember, 0, len(frames))
				var singleton *Candidate
				for _, f := range frames {
					c, e := DecodeCandidateFrame(b, f)
					if e != nil {
						return e
					}
					singleton = c
					members = append(members, ruby.AlignmentBatchMember{WorkID: c.WorkID, CandidateID: c.ID, Source: rawSource(&c.Segment), Alignment: c.Alignment})
				}
				if !textMode {
					if batched {
						req.JSONSchema, e = ruby.AlignmentBatchJSONSchemaBounded(b)
					} else {
						req.JSONSchema, e = ruby.AlignmentJSONSchemaForProtocolBounded(b, singleton.Alignment.ProtocolVersion)
					}
					if e != nil {
						return e
					}
				}
				claim := func(m backend.PromptMeasure) error {
					if count {
						measured = m
						return errMeasureOnly
					}
					if m != measured {
						return backend.ErrBuildCapacity
					}
					return nil
				}
				maxInput := max(int64(len(system))+page, int64(len(frames))*w.runtime.Window.limits.ItemBytes)
				if batched {
					user, e = ruby.AlignmentBatchRequestWithCreditsBounded(b, members, system, maxInput, claim)
				} else {
					missing, missingErr := singleton.Alignment.MissingBounded(b)
					if missingErr != nil {
						return missingErr
					}
					user, e = singleton.Alignment.AlignmentRequestWithCreditsBounded(b, rawSource(&singleton.Segment), missing, system, maxInput, claim)
				}
				return e
			})
			return user, err
		}
		_, e := render(true)
		if !errors.Is(e, errMeasureOnly) {
			update.err = e
			return update
		}
		callCtx := session.Context(ctx)
		if e = preclaimAux(callCtx, backends[0], session, req, measured); e != nil {
			if d := workDeferred(e); d != nil {
				update.waitCapacity = d
			} else if errors.Is(e, backend.ErrDispatchPaused) {
				update.paused = true
			} else {
				update.err = e
			}
			return update
		}
		user, e = render(false)
		for _, c := range remote {
			w.runtime.Admission.ObserveOwned("candidate", c.ID+"/aux-input", 0)
		}
		frames = nil
		if e != nil {
			update.err = e
			return update
		}
		req.System, req.User = system, user
		start = time.Now()
		resp, callErr = backends[0].Translate(callCtx, req)
		if resp != nil {
			w.runtime.Admission.ObserveOwned("aux", session.intent.ID+"/text", int64(len(resp.Text)))
			defer w.runtime.Admission.ObserveOwned("aux", session.intent.ID+"/text", 0)
		}
		if d := workDeferred(callErr); d != nil {
			update.waitCapacity = d
			return update
		}
		if !session.handoffDeadline.IsZero() {
			ctx = persistence.WithDeadline(callCtx, session.handoffDeadline)
		}
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
		if resp != nil {
			update.inputTokens = resp.Usage.PromptTokens
			update.outputTokens = resp.Usage.CompletionTokens
		}
	}
	isRemote := map[string]int{}
	for i, c := range remote {
		isRemote[c.ID] = i
	}
	for _, original := range candidates {
		frame, e := loadCandidateFrame(ctx, w.store, original.ID, w.runtime.Window.limits.ItemBytes)
		if e != nil {
			update.err = e
			return update
		}
		w.runtime.Admission.ObserveOwned("candidate", original.ID+"/aux-output", int64(cap(frame.Payload)))
		defer w.runtime.Admission.ObserveOwned("candidate", original.ID+"/aux-output", 0)
		if frame.Version != original.Version {
			update.err = ErrCandidateStale
			return update
		}
		var c *Candidate
		e = w.runtime.Run(ctx, func() error {
			input := int64(len(frame.Payload))
			if resp != nil {
				var e error
				input, e = boundedSum(input, int64(len(resp.Text)))
				if e != nil {
					return e
				}
			}
			b, e := backend.NewBuildBounds(w.runtime.Window.limits.ItemBytes, input)
			if e != nil {
				return e
			}
			c, e = DecodeCandidateFrame(b, frame)
			if e != nil {
				return e
			}
			remoteIndex, needsRemote := isRemote[c.ID]
			if !needsRemote {
				c.Ready = true
			} else {
				// The durable frame may already include this dispatched request's
				// debit. Reconcile with the frozen pre-dispatch member baseline;
				// do not add the same network attempt to the persisted counter again.
				c.NetworkAttempt = max(c.NetworkAttempt, w.session.intent.Members[remoteIndex].NetworkAttempt+w.session.calls)
				if w.session.lastID != "" {
					c.LastAlignmentRequestID = w.session.lastID
				}
				if callErr != nil {
					if backend.IsRetryable(callErr) && c.NetworkAttempt < max(1, round.Retry.MaxAttempts) {
						c.NextAttemptAt = time.Now().Add(backend.RetryDelay(round.Retry, c.NetworkAttempt-1, callErr))
					} else {
						c.Ready = true
					}
				} else {
					var entries []ruby.OutputEntry
					fault := false
					if len(remote) > 1 {
						var batch ruby.AlignmentBatchResult
						if textMode {
							batch, e = ruby.ParseAlignmentBatchTextBounded(b, resp.Text)
						} else {
							batch, e = ruby.ParseAlignmentBatchJSONBounded(b, resp.Text)
						}
						if e != nil {
							return e
						}
						key := ruby.AlignmentBatchKey{WorkID: c.WorkID, CandidateID: c.ID}
						entries, needsRemote = batch.Entries[key]
						fault = !needsRemote || batch.Invalid[key]
						if fault {
							entries = nil
						}
						if remoteIndex == 0 {
							diagnostics = append([]string(nil), batch.Diagnostics...)
							envelopeInvalid = batch.EnvelopeInvalid
							requested := map[ruby.AlignmentBatchKey]bool{}
							for _, m := range remote {
								requested[ruby.AlignmentBatchKey{WorkID: m.WorkID, CandidateID: m.ID}] = true
							}
							unknown := map[ruby.AlignmentBatchKey]bool{}
							for k := range batch.Entries {
								if !requested[k] {
									unknown[k] = true
								}
							}
							for k := range batch.Invalid {
								if !requested[k] {
									unknown[k] = true
								}
							}
							unknownMembers = len(unknown)
						}
					} else {
						if textMode {
							entries, e = ruby.ParseAlignmentTextV2Bounded(b, resp.Text)
						} else {
							entries, e = ruby.ParseAlignmentJSONV2Bounded(b, resp.Text)
						}
						if e != nil {
							return e
						}
					}
					missing, e := c.Alignment.MissingBounded(b)
					if e != nil {
						return e
					}
					p, e := c.Alignment.ApplyOutputBounded(b, itemIDs(missing), entries, true)
					if e != nil {
						return e
					}
					missing, e = c.Alignment.MissingBounded(b)
					if e != nil {
						return e
					}
					c.LogicalAttempt++
					c.NetworkAttempt = 0
					c.NextAttemptAt = time.Time{}
					fault = fault || len(remote) > 1 && resp.Truncated && len(missing) > 0
					if fault {
						c.ForceSingleAlignment = true
					}
					c.Ready = len(missing) == 0 || c.LogicalAttempt >= budget || p.Added == 0 && !fault
				}
				missing, e := c.Alignment.MissingBounded(b)
				if e != nil {
					return e
				}
				requestedMembers = append(requestedMembers, progress.AlignmentMemberEvent{WorkID: c.WorkID, CandidateID: c.ID, CandidateVersion: c.Version, ParentRequestID: c.ParentRequestID, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: w.session.intent.Members[remoteIndex].NetworkAttempt + w.session.calls, VerifiedItems: len(c.Alignment.Verified), MissingItems: len(missing), ForceSingle: c.ForceSingleAlignment})
			}
			c.Version++
			c.PendingSave = true
			return c.SealAndCompact(b, w.runtime.Window.limits.ItemBytes)
		})
		frame = CandidateFrame{}
		w.runtime.Admission.ObserveOwned("candidate", original.ID+"/aux-output", 0)
		if e != nil {
			update.err = e
			return update
		}
		*original = *c
		w.runtime.Admission.ObserveOwned("candidate", original.ID+"/aux-saved", int64(cap(original.PreparedPayload)))
		defer w.runtime.Admission.ObserveOwned("candidate", original.ID+"/aux-saved", 0)
		if validator, ok := w.store.(interface {
			ValidateCandidate(context.Context, *Candidate) (bool, error)
		}); ok {
			valid, e := validator.ValidateCandidate(ctx, original)
			if e != nil {
				update.err = e
				return update
			}
			if !valid {
				original.Version--
				if update.stale == nil {
					update.stale = map[int]bool{}
				}
				update.stale[original.Index] = true
				continue
			}
		}
		if e = w.saveCandidate(ctx, original); e != nil {
			if errors.Is(e, ErrCandidateStale) {
				original.Version--
				if update.stale == nil {
					update.stale = map[int]bool{}
				}
				update.stale[original.Index] = true
				continue
			}
			update.err = e
			return update
		}
	}
	if len(remote) > 0 {
		if observer, ok := w.reporter.(progress.BatchObserver); ok {
			evt := progress.BatchEvent{Stage: "ruby_alignment", SegmentCount: len(remote), BackendName: backends[0].Name(), Status: "success", DurationMs: time.Since(start).Milliseconds(), RoundIndex: roundIndex, SystemPrompt: system, UserMessage: user, ResponseFormat: req.ResponseFormat, JSONSchema: req.JSONSchema, InputTokens: update.inputTokens, OutputTokens: update.outputTokens, AlignmentMembers: requestedMembers, ProtocolDiagnostics: diagnostics, EnvelopeInvalid: envelopeInvalid, UnknownAlignmentMembers: unknownMembers}
			for _, c := range remote {
				evt.SegmentIDs = append(evt.SegmentIDs, c.Segment.ID)
			}
			for _, m := range requestedMembers {
				evt.VerifiedItems += m.VerifiedItems
				evt.MissingItems += m.MissingItems
			}
			if len(remote) == 1 {
				c := remote[0]
				evt.ParentRequestID = c.ParentRequestID
				evt.CandidateID = c.ID
				evt.CandidateVersion = c.Version
				evt.LogicalAttempt = c.LogicalAttempt
				evt.NetworkAttempt = w.session.intent.NetworkAttempt + w.session.calls
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
			observer.OnBatchEvent(evt)
		}
	}
	if err := w.finish(nil); err != nil {
		update.err = err
	}
	return update
}

func (w *AlignmentWorker) saveCandidate(ctx context.Context, c *Candidate) error {
	if !c.PendingSave {
		return ErrCandidateStale
	}
	data, err := c.EncodeBounded(w.runtime.Window.limits.ItemBytes, 0)
	if err != nil {
		return err
	}
	if err = w.runtime.Window.ReserveResize(c.ID, int64(len(data))); err != nil {
		return err
	}
	if err = w.runtime.SaveNamed(ctx, candidateSaveMetadata("candidate", w.store, c), func(saveCtx context.Context) error { return w.store.Save(saveCtx, c) }); err != nil {
		return err
	}
	if err := w.runtime.Window.Resize(c.ID, int64(len(data))); err != nil {
		return err
	}
	c.PendingSave = false
	c.PreparedPayload = nil
	w.runtime.Admission.ObserveOwned("candidate", c.ID+"/aux-saved", 0)
	return nil
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
