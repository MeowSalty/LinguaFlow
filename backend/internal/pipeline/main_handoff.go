package pipeline

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

type frozenMainMember struct {
	Index       int
	PromptID    string
	CandidateID string
	Segment     Segment
	Mapping     map[string]string
	RubyItems   []ruby.Item
	MetaTypes   map[string]string
}

type frozenMainBatch struct {
	Pool        int
	MainAttempt int
	Mode        string
	Format      string
	SourceLang  string
	TargetLang  string
	TextMode    bool
	Members     []frozenMainMember
}

// preparedMainDocument stores only selected targets and their actual context.
// Global indices are kept separately; it never clones the Document segment tree.
type preparedMainDocument struct {
	doc       *Document
	global    []int
	targets   []int
	context   map[int]struct{}
	truncated map[int]string
	locals    []int
}

func snapshotMainInput(doc *Document, indices []int, handler stagedHandler, runtime *ExecutionRuntime) (*preparedMainDocument, error) {
	if len(indices) == 0 || len(indices) > 65536 {
		return nil, ErrCandidateCapacity
	}
	b, err := backend.NewBuildBounds(runtime.Window.limits.ItemBytes, runtime.handoffLimit())
	if err != nil {
		return nil, err
	}
	if err = b.ReserveNodes(int64(len(indices)), 8); err != nil {
		return nil, err
	}
	expanded := append([]int(nil), indices...)
	truncated := map[int]string{}
	if h, ok := handler.(*TranslateHandler); ok && h.Context.Enabled {
		count, boundErr := boundedSum(int64(max(0, h.Context.Before)), int64(max(0, h.Context.After)), 1)
		if boundErr != nil || count > 65536/int64(max(1, len(indices))) {
			return nil, ErrCandidateCapacity
		}
		e, err := ExpandMainContextBounded(b, doc, indices, len(doc.Segments), max(h.Context.Before, h.Context.After), h.Context.MaxChars, runtime.handoffLimit())
		if err != nil {
			return nil, err
		}
		expanded, truncated = e.Idxs, e.TruncatedSrc
	}
	if len(expanded) > 65536 {
		return nil, ErrCandidateCapacity
	}
	if err = b.ReserveNodes(int64(len(expanded)), 512); err != nil {
		return nil, err
	}
	input := &preparedMainDocument{doc: &Document{SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Format: doc.Format, Vars: doc.Vars}, global: expanded, context: map[int]struct{}{}, truncated: map[int]string{}}
	input.doc.Segments = make([]Segment, len(expanded))
	if err = b.ReserveNodes(int64(len(expanded))+int64(len(indices))*2+2, 32); err != nil {
		return nil, err
	}
	input.locals = make([]int, len(expanded))
	input.targets = make([]int, 0, len(indices))
	want := map[int]bool{}
	for _, idx := range indices {
		want[idx] = true
	}
	for local, idx := range expanded {
		input.locals[local] = local
		input.doc.Segments[local] = doc.Segments[idx]
		if want[idx] {
			input.targets = append(input.targets, local)
		} else {
			input.context[local] = struct{}{}
		}
		if text, ok := truncated[idx]; ok {
			input.truncated[local] = text
		}
	}
	return input, nil
}

func (r *ExecutionRuntime) handoffLimit() int64  { return r.HandoffBytes }
func (r *ExecutionRuntime) responseLimit() int64 { return r.ResponseBytes }

// Run implements the provider's short CPU-build permit. It never encloses a
// network read, admission wait or persistence operation.
func (r *ExecutionRuntime) Run(ctx context.Context, fn func() error) error {
	if r.Admission != nil {
		if err := r.Admission.ChangeStation(0, 1, 0); err != nil {
			return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "aggregate_build", CapacityChanged: r.Admission.RootChanged()}}
		}
		defer r.Admission.ChangeStation(0, -1, 0)
	}
	select {
	case r.parse <- struct{}{}:
		defer func() { <-r.parse }()
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *ExecutionRuntime) retainMainOwner(id string, permits []*backend.RequestPermit) {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	if r.mainOwners == nil {
		r.mainOwners = map[string][]*backend.RequestPermit{}
	}
	r.mainOwners[id] = permits
}

func (r *ExecutionRuntime) releaseMainOwner(id string) {
	r.Admission.ObserveOwned("freeze", id, 0)
	r.Admission.ObserveOwned("raw", id, 0)
	r.ownerMu.Lock()
	permits := r.mainOwners[id]
	delete(r.mainOwners, id)
	r.ownerMu.Unlock()
	for _, permit := range permits {
		permit.Release()
	}
	if len(permits) == 0 {
		r.Admission.ReleaseDurableResponse(id)
	}
}

func prepareMainInvocation(ctx context.Context, h stagedHandler, input *preparedMainDocument, session *requestSession, attempt int, logger *slog.Logger) batchResult {
	doc := input.doc
	var hintPayload MainHintPayload
	hintCredit := int64(0)
	defer session.runtime.Admission.ReleasePrepared(session.intent.ID)
	defer session.runtime.Admission.ObserveOwned("attachment", session.intent.ID, 0)
	if handler, ok := h.(*TranslateHandler); ok {
		var err error
		hintPayload, err = LookupMainHintsWithCreditsBounded(ctx, handler, doc, input.globalLocal(), session.runtime.handoffLimit(), session.runtime.Run, logger, func(n int64) error {
			if hintCredit == 0 {
				ok, err := session.runtime.Admission.PrepareInputAttachment(session.intent.ID, session.intent.RoundIndex, session.intent.ResourceID, n)
				if err != nil {
					return err
				}
				if !ok {
					return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "hint_attachment", CapacityChanged: session.runtime.Admission.Changed()}}
				}
			} else if err := session.runtime.Admission.RepriceInputAttachment(session.intent.ID, hintCredit, n); err != nil {
				return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "hint_attachment", CapacityChanged: session.runtime.Admission.Changed()}}
			}
			hintCredit = n
			return nil
		})
		if err != nil {
			if d := workDeferred(err); d != nil {
				return batchResult{waitCapacity: d, deferred: true}
			}
			return batchResult{err: err}
		}
		if hintCredit > 0 {
			if err = session.runtime.Admission.RepriceInputAttachment(session.intent.ID, hintCredit, int64(len(hintPayload.JSON))); err != nil {
				return batchResult{err: err}
			}
			hintCredit = int64(len(hintPayload.JSON))
		}
		session.runtime.Admission.ObserveOwned("attachment", session.intent.ID, int64(cap(hintPayload.JSON)))
	}
	var req backend.Request
	var remote backend.Backend
	buildErr := session.runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBounds(session.runtime.Window.limits.ItemBytes, session.runtime.handoffLimit())
		if err != nil {
			return err
		}
		frozen := frozenMainBatch{Mode: h.ModeName(), Format: doc.Format, SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Pool: session.intent.Pool, MainAttempt: attempt + 1}
		switch handler := h.(type) {
		case *TranslateHandler:
			if err = b.ReserveNodes(int64(len(input.targets)), 64); err != nil {
				return err
			}
			glos, hints, err := DecodeMainHintsBounded(b, hintPayload)
			if err != nil {
				return err
			}
			defer func() { glos = nil; hints = nil }()
			copy := *handler
			copy.buildBounds = b
			copy.buildMaxBytes = session.runtime.handoffLimit()
			remote = handler.Backend
			frozen.TextMode = prompt.ProtocolFromResponseMode(handler.ResponseMode).IsText()
			ids := make([]string, len(input.targets))
			for i, local := range input.targets {
				if err = b.ReserveText(20, 20); err != nil {
					return err
				}
				if frozen.TextMode {
					ids[i] = strconv.Itoa(i + 1)
				} else {
					ids[i] = strconv.Itoa(local + 1)
				}
			}
			copy.buildClaim = func(skeleton backend.Request, m backend.PromptMeasure) error {
				return freezeMeasuredMain(b, h, input, session, frozen, ids, nil, remote, skeleton, m)
			}
			_, _, request, _, _, _, err := copy.buildRequestWithHints(doc, input.globalLocal(), input.context, input.truncated, glos, hints)
			req = request
			return err
		case *ReviseHandler:
			copy := *handler
			copy.buildBounds = b
			remote = handler.Backend
			frozen.TextMode = prompt.ProtocolFromResponseMode(handler.ResponseMode).IsText()
			segments, states, anns, err := copy.buildProtectedInputs(doc, input.targets, handler.issueCodeSet())
			if err != nil {
				return err
			}
			ids := make([]string, len(segments))
			for i, seg := range segments {
				ids[i] = seg.ID
			}
			if frozen.TextMode {
				req.ResponseFormat = "none"
			} else {
				req.JSONSchema, err = prompt.ReviseRevisionSchemaBounded(b, handler.RubyMode != "", ids, handler.RubyProtocolVersion)
				if err != nil {
					return err
				}
			}
			sys, user, err := handler.Renderer.RenderWithCreditsBounded(b, prompt.ReviseData{SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Segments: segments, Protocol: prompt.ProtocolFromResponseMode(handler.ResponseMode), RubyAnnotations: anns, RubyMode: handler.RubyMode}, session.runtime.handoffLimit(), func(m backend.PromptMeasure) error {
				return freezeMeasuredMain(b, h, input, session, frozen, ids, states, remote, req, m)
			})
			req.System, req.User = sys, user
			return err
		default:
			return fmt.Errorf("handler has no main response contract")
		}
	})
	if !errors.Is(buildErr, backend.ErrMainBatchTooLarge) {
		input.doc = nil
	}
	doc = nil
	hintPayload.JSON = nil
	if buildErr != nil {
		if d := workDeferred(buildErr); d != nil {
			return batchResult{waitCapacity: d, deferred: true}
		}
		return batchResult{err: buildErr}
	}
	if hintCredit > 0 {
		if err := session.runtime.Admission.ReleaseInputAttachment(session.intent.ID, hintCredit); err != nil {
			return batchResult{err: err}
		}
	}
	session.runtime.Admission.ObserveOwned("attachment", session.intent.ID, 0)
	_, err := remote.Translate(ctx, req)
	req.System, req.User = "", ""
	req.JSONSchema = nil
	if err != nil {
		if d := workDeferred(err); d != nil {
			return batchResult{waitCapacity: d, deferred: true}
		}
		if errors.Is(err, backend.ErrNoCapacityDrain) || storageFailure(err) || errors.Is(err, backend.ErrBuildCapacity) || errors.Is(err, backend.ErrResponseTooLarge) {
			return batchResult{err: err}
		}
		if backend.IsRetryable(err) {
			return batchResult{retry: &batchJob{attempt: attempt + 1, notBefore: time.Now().Add(backoffDuration(attempt, handlerRetry(h), err))}}
		}
		return batchResult{unresolved: session.intent.Indices}
	}
	if session.rawResponse == nil {
		return batchResult{err: fmt.Errorf("main backend did not hand off its response")}
	}
	return batchResult{}
}
func (input *preparedMainDocument) globalLocal() []int {
	return input.locals
}

func freezeMeasuredMain(b *backend.BuildBounds, h stagedHandler, input *preparedMainDocument, session *requestSession, frozen frozenMainBatch, ids []string, states []reviseProtectState, remote backend.Backend, req backend.Request, m backend.PromptMeasure) error {
	if err := b.ReserveFormatWorkspace(); err != nil {
		return err
	}
	doc := input.doc
	if err := b.ReserveNodes(int64(len(input.targets)), 768); err != nil {
		return err
	}
	for i, local := range input.targets {
		seg := doc.Segments[local]
		global := input.global[local]
		candidateID := NewWorkID()
		if err := b.ReserveNodes(int64(len(seg.Meta)), 512); err != nil {
			return err
		}
		member := frozenMainMember{Index: global, PromptID: ids[i], CandidateID: candidateID, Segment: seg, RubyItems: extractRubyItemsFromSeg(&seg), MetaTypes: metadataTypes(seg.Meta)}
		if len(states) > 0 {
			member.Mapping = states[i].mapping
			member.RubyItems = states[i].rubyItems
		}
		frozen.Members = append(frozen.Members, member)
		var target *string
		if !seg.TargetIsNull {
			text := seg.Target
			target = &text
		}
		workID := fmt.Sprintf("%s:%d", frozen.Mode, global)
		if owner, ok := session.store.(interface{ WorkIdentity(int) string }); ok {
			workID = owner.WorkIdentity(global)
		}
		session.intent.PromotionMembers = append(session.intent.PromotionMembers, ResponseMember{Index: global, SegmentID: seg.DBID, WorkID: workID, CandidateID: candidateID, BaselineVersion: seg.ContentVersion, BaselineTarget: target, BaselineStatus: seg.Status})
	}
	// The persisted member order follows stable segment identity, not map order.
	sort.Slice(frozen.Members, func(i, j int) bool {
		return frozen.Members[i].Segment.DBID < frozen.Members[j].Segment.DBID || frozen.Members[i].Segment.DBID == frozen.Members[j].Segment.DBID && frozen.Members[i].Index < frozen.Members[j].Index
	})
	sort.Slice(session.intent.PromotionMembers, func(i, j int) bool {
		return session.intent.PromotionMembers[i].SegmentID < session.intent.PromotionMembers[j].SegmentID || session.intent.PromotionMembers[i].SegmentID == session.intent.PromotionMembers[j].SegmentID && session.intent.PromotionMembers[i].Index < session.intent.PromotionMembers[j].Index
	})
	if handler, ok := h.(*TranslateHandler); ok && handler.InlineBootstrap && handler.InlineConflictStrategy == InlineConflictRewriteLocal {
		bound, e := backend.CheckedProduct(int64(len(frozen.Members)), session.runtime.Window.limits.ItemBytes)
		if e != nil {
			return e
		}
		session.intent.BusinessBound = bound
	}
	freezeBytes, err := b.MeasureJSON(frozen)
	if err != nil {
		return err
	}
	wireBytes, err := backend.MeasureRequestPrompt(remote, req, m)
	if err != nil {
		return err
	}
	inputBytes, err := boundedSum(m.SystemBytes, m.UserBytes, wireBytes)
	if err != nil {
		return err
	}
	progress := session.runtime.Admission.RuntimeState().ProgressID == session.intent.ID
	granted, err := session.runtime.Admission.PrepareMain(session.intent.ID, session.intent.RoundIndex, session.intent.ResourceID, freezeBytes, inputBytes, session.intent.BusinessBound, progress, len(session.intent.Indices))
	if errors.Is(err, backend.ErrAggregateCapacity) {
		return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "aggregate_capacity", CapacityChanged: session.runtime.Admission.Changed()}}
	}
	if err != nil {
		return err
	}
	if !granted {
		return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "prepared_bytes", CapacityChanged: session.runtime.Admission.Changed()}}
	}
	payload, err := b.EncodeJSON(frozen, session.runtime.handoffLimit())
	if err != nil {
		return err
	}
	session.intent.FreezePayload = payload
	session.runtime.Admission.ObserveOwned("freeze", session.intent.ID, int64(cap(payload)))
	session.mode = h.ModeName()
	session.intent.SnapshotDigest = responseDigest(payload)
	return nil
}

func handlerRetry(h stagedHandler) backend.RetryPolicy {
	switch v := h.(type) {
	case *TranslateHandler:
		return v.Retry
	case *ReviseHandler:
		return v.Retry
	}
	return backend.RetryPolicy{}
}

func prepareCapturedMain(ctx context.Context, round Round, h stagedHandler, source *Document, input *preparedMainDocument, indices []int, pool, attempt int, reservation string, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporter progress.Reporter) (e stagedEvent) {
	parentCtx := ctx
	e = stagedEvent{main: true, indices: indices, cursors: map[int]WorkCursor{}}
	session := &requestSession{runtime: runtime, store: store, reporter: reporter, intent: RequestIntent{ID: reservation, Stage: backend.RequestStageMain, Indices: indices, Pool: pool, Attempt: attempt, Phase: "main", RoundIndex: prepareIntent(h, store, backend.RequestStageMain).RoundIndex, ResourceID: storeResourceID(store)}}
	ctx = session.Context(ctx)
	defer runtime.Admission.ObserveOwned("freeze", reservation, 0)
	defer runtime.Admission.ObserveOwned("raw", reservation, 0)
	result := prepareMainInvocation(ctx, h, input, session, attempt, logger)

	if result.err != nil {
		session.fail(result.err)
	}
	if err := session.finish(); err != nil {
		e.err = err
		runtime.Window.Release(reservation)
		return e
	}
	ctx = parentCtx
	if !session.handoffDeadline.IsZero() {
		ctx = persistence.WithDeadline(ctx, session.handoffDeadline)
	}
	if session.rawResponse != nil {
		response := session.rawResponse
		// Promotion/recovery only consumes the saved raw. This dispatch event is
		// the one place that adds the already-recorded HTTP usage to this run's
		// document totals, independent of the number of promoted members.
		e.inputTokens = response.Record.Usage.PromptTokens
		e.outputTokens = response.Record.Usage.CompletionTokens
		if err := runtime.Window.RenameReservation(reservation, response.ID); err != nil {
			e.err = err
			return e
		}
		e.responseHeaders = []MainResponseHeader{response.MainResponseHeader}
		for _, idx := range indices {
			e.cursors[idx] = WorkCursor{Pool: pool, Attempt: attempt + 1, State: "response_pending"}
		}
		return e
	}
	if result.waitCapacity != nil {
		runtime.Window.Release(reservation)
		if result.waitCapacity.Wait.Reason != "aggregate_capacity" {
			result.waitCapacity.Wait.CapacityChanged = runtime.Admission.Changed()
		}
		e.waitCapacity = result.waitCapacity
		e.waitIndices = append([]int(nil), indices...)
		e.waitPool = pool
		e.waitAttempt = attempt
		return e
	}
	if result.err != nil {
		if session.calls > 0 {
			_ = runtime.Window.SplitOwnedMembers(reservation, memberReservationKeys(store, h.ModeName(), indices))
		} else {
			runtime.Window.Release(reservation)
		}
		e.err = result.err
		return e
	}
	var transitions []CursorTransition
	for _, idx := range indices {
		cursor := WorkCursor{Pool: pool + 1, State: "pending"}
		if result.retry != nil {
			cursor = WorkCursor{Pool: pool, Attempt: attempt + 1, State: "pending", NextAttemptAt: result.retry.notBefore}
		}
		e.cursors[idx] = cursor
		transitions = append(transitions, CursorTransition{Index: idx, Cursor: cursor})
	}
	e.err = runtime.SaveNamed(ctx, persistence.Metadata{Operation: "main_failure", Class: persistence.Reliable, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error {
		if failed, ok := store.(interface {
			FailureCursors(context.Context, string, []CursorTransition) error
		}); ok {
			return failed.FailureCursors(saveCtx, session.intent.ID, transitions)
		}
		return store.Cursors(saveCtx, transitions)
	})
	if e.err == nil && session.calls > 0 {
		var owned []int
		for _, t := range transitions {
			if t.Cursor.State == "pending" {
				owned = append(owned, t.Index)
			}
		}
		e.err = runtime.Window.SplitOwnedMembers(reservation, memberReservationKeys(store, h.ModeName(), owned))
		if e.err == nil {
			e.err = runtime.RefreshEpisode(ctx)
		}
	} else {
		runtime.Window.Release(reservation)
	}
	return e
}

func promoteCapturedMain(ctx context.Context, round Round, h stagedHandler, header MainResponseHeader, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger) (e stagedEvent) {
	e = stagedEvent{rawPromotion: true, cursors: map[int]WorkCursor{}}
	owned, ownerErr := runtime.Admission.ClaimResponseOwner(header.ID)
	if ownerErr != nil {
		e.err = ownerErr
		return e
	}
	if !owned {
		e.responseAdvance = &header
		e.waitCapacity = &WorkDeferred{Wait: backend.AdmissionWait{Reason: "response_owner", CapacityChanged: runtime.Admission.Changed()}}
		return e
	}
	response, err := store.Response(ctx, header.ID, runtime.handoffLimit(), runtime.responseLimit(), runtime.handoffLimit())
	if err != nil {
		e.err = err
		return e
	}
	runtime.Admission.ObserveOwned("freeze", header.ID+"/promotion", int64(cap(response.FreezePayload)))
	runtime.Admission.ObserveOwned("raw", header.ID+"/promotion", int64(cap(response.Raw)))
	defer runtime.Admission.ObserveOwned("freeze", header.ID+"/promotion", 0)
	defer runtime.Admission.ObserveOwned("raw", header.ID+"/promotion", 0)
	var frozen frozenMainBatch
	err = runtime.Run(ctx, func() error {
		b, e := backend.NewBuildBounds(runtime.Window.limits.ItemBytes, header.FreezeBytes)
		if e != nil {
			return e
		}
		var full frozenMainBatch
		if e = b.DecodeJSON(response.FreezePayload, &full); e != nil {
			return e
		}
		if e = b.ReserveNodes(int64(len(full.Members)), 128); e != nil {
			return e
		}
		frozen = full
		frozen.Members = make([]frozenMainMember, len(full.Members))
		for i, m := range full.Members {
			frozen.Members[i] = frozenMainMember{Index: m.Index, PromptID: m.PromptID, CandidateID: m.CandidateID}
		}
		return nil
	})
	if err != nil {
		e.err = err
		return e
	}
	if header.Cursor >= len(frozen.Members) || header.Cursor >= len(response.Members) {
		e.err = fmt.Errorf("raw cursor is outside frozen members")
		return e
	}
	m := frozen.Members[header.Cursor]
	if handler, ok := h.(*TranslateHandler); ok && handler.InlineBootstrap && handler.Glossary != nil && response.Phase == "response_pending" {
		granted, err := runtime.Admission.TryBusinessOwner(header.ID)
		if err != nil {
			e.err = err
			return e
		}
		if !granted {
			granted, err = runtime.TransferBusiness(ctx, runtime.Admission.RuntimeState().BusinessOwner, header.ID, storeResourceID(store), prepareIntent(h, store, backend.RequestStageMain).RoundIndex)
			if err != nil {
				e.err = err
				return e
			}
		}
		if !granted {
			e.responseAdvance = &header
			e.waitCapacity = &WorkDeferred{Wait: backend.AdmissionWait{Reason: "business_lane", CapacityChanged: runtime.Admission.Changed()}}
			return e
		}
		response, err = processFrozenGlossary(ctx, handler, frozen, response, store, runtime, logger)
		if err != nil {
			e.err = err
			return e
		}
		if err := runtime.Admission.RestoreResponseBusiness(header.ID, response.CorrectedBytes); err != nil {
			e.err = err
			return e
		}
		header.Phase = response.Phase
	}
	var c *Candidate
	var parseErr error
	next := WorkCursor{Pool: round.Retry.MaxAttempts + 1, State: "unresolved"}
	err = runtime.Run(ctx, func() error {
		b, e := backend.NewBuildBounds(runtime.Window.limits.ItemBytes, header.FreezeBytes+header.ResponseBytes)
		if e != nil {
			return e
		}
		var full frozenMainBatch
		if e = b.DecodeJSON(response.FreezePayload, &full); e != nil {
			return e
		}
		if header.Cursor >= len(full.Members) {
			return ErrCandidateStale
		}
		member := &full.Members[header.Cursor]
		if e = restoreMetadataTypes(&member.Segment, member.MetaTypes, b); e != nil {
			return e
		}
		if len(member.RubyItems) > 0 {
			// A frozen member can keep ruby outside its optional metadata map.
			// Account for map creation/growth and the boxed slice before insertion.
			if e = b.ReserveNodes(int64(len(member.Segment.Meta))+4, 256); e != nil {
				return e
			}
			if member.Segment.Meta == nil {
				member.Segment.Meta = make(map[string]any, 1)
			}
			member.Segment.Meta["ruby_items"] = member.RubyItems
		}
		c, parseErr = parseFrozenMemberBounded(b, h, full, response, header.Cursor, logger)
		if c != nil && parseErr == nil {
			c.WorkID = response.Members[header.Cursor].WorkID
			if e = c.SealAndCompact(b, runtime.Window.limits.ItemBytes); e != nil {
				parseErr = e
				c = nil
			} else {
				next = WorkCursor{Pool: c.PoolIndex, Attempt: c.MainAttempt, State: "candidate"}
			}
		}
		return nil
	})
	if err != nil {
		e.err = err
		return e
	}
	// Decode trees end with the short permit. Only fixed member metadata and
	// one already bounded canonical encoding survive W/J waits or DB saves.
	if c != nil {
		size := c.StoredBytes
		runtime.Admission.ObserveOwned("candidate", c.ID+"/promotion", int64(cap(c.PreparedPayload)))
		defer runtime.Admission.ObserveOwned("candidate", c.ID+"/promotion", 0)
		granted, capacityErr := runtime.Admission.BeginPromotion(header.ID, c.ID, size, false)
		if errors.Is(capacityErr, backend.ErrProgressRequired) {
			if err := runtime.EnsureProgress(ctx, c.ID, header.ID, storeResourceID(store), prepareIntent(h, store, backend.RequestStageMain).RoundIndex); err != nil {
				if errors.Is(err, backend.ErrProgressRequired) {
					e.yield = errors.Is(err, backend.ErrEarlierScope)
					e.responseAdvance = &header
					e.waitCapacity = &WorkDeferred{Wait: backend.AdmissionWait{Reason: "progress_owner", CapacityChanged: runtime.Admission.Changed()}}
					return e
				}
				e.err = err
				return e
			}
			granted, capacityErr = runtime.Admission.BeginPromotion(header.ID, c.ID, size, true)
		}
		if capacityErr != nil {
			e.err = capacityErr
			return e
		}
		if !granted {
			e.responseAdvance = &header
			e.waitCapacity = &WorkDeferred{Wait: backend.AdmissionWait{Reason: "candidate_bytes", CapacityChanged: runtime.Admission.Changed()}}
			return e
		}
		defer runtime.Admission.AbortPromotion(c.ID)
	}
	failure := ""
	if parseErr != nil {
		failure = parseErr.Error()
		if len(failure) > 2048 {
			failure = failure[:2048]
		}
	}
	if err = runtime.SaveNamed(ctx, persistence.Metadata{Operation: "member_promotion", Class: persistence.Reliable, ResourceID: storeResourceID(store), RequestID: header.ID}, func(saveCtx context.Context) error {
		return store.PromoteMember(saveCtx, header.ID, header.Cursor, c, next, failure)
	}); err != nil {
		e.err = err
		return e
	}
	if c != nil {
		if err = runtime.Window.Transfer(header.ID, c.ID, c.StoredBytes); err != nil {
			e.err = err
			return e
		}
		e.candidates = []*Candidate{c}
	} else {
		if err = runtime.Window.DropRawMember(header.ID); err != nil {
			e.err = err
			return e
		}
	}
	e.cursors[m.Index] = next
	header.Cursor++
	e.responseAdvance = &header
	if header.Cursor == header.MemberCount {
		runtime.releaseMainOwner(header.ID)
		if err := runtime.RefreshEpisode(ctx); err != nil {
			e.err = err
		}
	}
	return e
}

func metadataTypes(meta map[string]any) map[string]string {
	out := map[string]string{}
	for key, value := range meta {
		switch value.(type) {
		case int:
			out[key] = "int"
		case []int:
			out[key] = "ints"
		case []ruby.Item:
			out[key] = "ruby_items"
		case int64:
			out[key] = "int64"
		case float64:
			out[key] = "float64"
		case float32:
			out[key] = "float32"
		}
	}
	return out
}

func restoreMetadataTypes(seg *Segment, kinds map[string]string, b *backend.BuildBounds) error {
	for key, kind := range kinds {
		value, ok := seg.Meta[key]
		if !ok {
			continue
		}
		data, err := b.EncodeJSON(value, b.Snapshot().TextLimit)
		if err != nil {
			return err
		}
		switch kind {
		case "int":
			var v int
			if err := b.DecodeJSON(data, &v); err != nil {
				return err
			}
			seg.Meta[key] = v
		case "int64":
			var v int64
			if err := b.DecodeJSON(data, &v); err != nil {
				return err
			}
			seg.Meta[key] = v
		case "float64":
			var v float64
			if err := b.DecodeJSON(data, &v); err != nil {
				return err
			}
			seg.Meta[key] = v
		case "float32":
			var v float32
			if err := b.DecodeJSON(data, &v); err != nil {
				return err
			}
			seg.Meta[key] = v
		case "ints":
			var v []int
			if err := b.DecodeJSON(data, &v); err != nil {
				return err
			}
			seg.Meta[key] = v
		case "ruby_items":
			var v []ruby.Item
			if err := b.DecodeJSON(data, &v); err != nil {
				return err
			}
			seg.Meta[key] = v
		default:
			return fmt.Errorf("invalid frozen metadata type")
		}
	}
	return nil
}

// parseFrozenMember is pure: it cannot invoke a provider or re-read a baseline.
func parseFrozenMember(h stagedHandler, frozen frozenMainBatch, response MainResponse, member int, logger *slog.Logger) (*Candidate, error) {
	b, err := backend.NewBuildBounds(DefaultCandidateLimits().ItemBytes, int64(len(response.Raw))+int64(len(response.FreezePayload)))
	if err != nil {
		return nil, err
	}
	return parseFrozenMemberBounded(b, h, frozen, response, member, logger)
}
func parseFrozenMemberBounded(b *backend.BuildBounds, h stagedHandler, frozen frozenMainBatch, response MainResponse, member int, logger *slog.Logger) (*Candidate, error) {
	switch handler := h.(type) {
	case *TranslateHandler:
		copy := *handler
		copy.buildBounds = b
		copy.Repair.Bounds = b
		h = &copy
	case *ReviseHandler:
		copy := *handler
		copy.buildBounds = b
		copy.Repair.Bounds = b
		h = &copy
	}
	if member < 0 || member >= len(frozen.Members) {
		return nil, fmt.Errorf("invalid frozen member")
	}
	m := frozen.Members[member]
	var candidate *Candidate
	switch handler := h.(type) {
	case *TranslateHandler:
		ids := make([]string, len(frozen.Members))
		for i, v := range frozen.Members {
			ids[i] = v.PromptID
		}
		parsed, parseErr := repair.ParseMainMemberBounded(b, string(response.Raw), frozen.TextMode, ids, m.PromptID, handler.Repair)
		if parseErr != nil {
			return nil, parseErr
		}
		if parsed.ParseErr != nil {
			return nil, parsed.ParseErr
		}
		text, ok := parsed.Trans[m.PromptID]
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("missing response member")
		}
		doc := &Document{Format: frozen.Format, Segments: []Segment{m.Segment}}
		if response.BusinessBound > 0 && len(response.CorrectedIndex) > 0 {
			start := binary.LittleEndian.Uint64(response.CorrectedIndex[member*16:])
			n := binary.LittleEndian.Uint64(response.CorrectedIndex[member*16+8:])
			if start > uint64(len(response.CorrectedTargets)) || n > uint64(len(response.CorrectedTargets))-start {
				return nil, ErrCandidateCapacity
			}
			if err := b.ReserveText(int64(n), int64(n)); err != nil {
				return nil, err
			}
			text = string(response.CorrectedTargets[start : start+n])
			if text == "" {
				return nil, ErrCandidateCapacity
			}
		}
		result := handler.prepareTranslatedCandidates(doc, []int{0}, []string{m.PromptID}, map[string]string{m.PromptID: text}, parsed.RubyOutput, nil, logger, m.CandidateID)
		if result.err != nil {
			return nil, result.err
		}
		if len(result.candidates) == 0 {
			return nil, fmt.Errorf("invalid response member")
		}
		candidate = result.candidates[0]
	case *ReviseHandler:
		revision, outputs, err := repair.ParseReviseMemberBounded(b, string(response.Raw), frozen.TextMode, m.PromptID, handler.Repair)
		if err != nil {
			return nil, err
		}
		if revision != nil {
			candidate, err = handler.prepareRevisionCandidate(&m.Segment, m.Index, *revision, &reviseProtectState{mapping: m.Mapping, rubyItems: m.RubyItems}, outputs, frozen.Format, m.CandidateID)
			if err != nil {
				return nil, err
			}
		}
		if candidate == nil {
			return nil, fmt.Errorf("missing or invalid revision member")
		}
	}
	candidate.Index = m.Index
	candidate.ID = m.CandidateID
	candidate.ParentRequestID = response.ID
	candidate.PoolIndex = frozen.Pool
	candidate.MainAttempt = frozen.MainAttempt
	return candidate, nil
}

func boundedSum(values ...int64) (int64, error) {
	var sum int64
	for _, v := range values {
		if v < 0 || v > math.MaxInt64-sum {
			return 0, backend.ErrPipelineCapacity
		}
		sum += v
	}
	return sum, nil
}

func memberReservationKeys(store RoundStore, mode string, indices []int) []string {
	out := make([]string, len(indices))
	for i, idx := range indices {
		work := fmt.Sprintf("%s:%d", mode, idx)
		if identity, ok := store.(interface{ WorkIdentity(int) string }); ok {
			work = identity.WorkIdentity(idx)
		}
		out[i] = "member:" + work
	}
	return out
}
