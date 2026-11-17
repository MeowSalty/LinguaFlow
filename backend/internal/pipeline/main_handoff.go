package pipeline

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

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
	bounds    *backend.BuildBounds
	doc       *Document
	global    []int
	targets   []int
	context   map[int]struct{}
	truncated map[int]string
	locals    []int
}

func snapshotMainInput(ctx context.Context, doc *Document, indices []int, handler stagedHandler, runtime *ExecutionRuntime) (*preparedMainDocument, error) {
	if len(indices) == 0 {
		return nil, ErrCandidateCapacity
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, runtime.Window.limits.ItemBytes, runtime.handoffLimit())
	if err != nil {
		return nil, err
	}
	if err = backend.ReserveSlice[int](b, int64(len(indices))); err != nil {
		return nil, err
	}
	expanded := make([]int, len(indices))
	copy(expanded, indices)
	truncated := map[int]string{}
	if h, ok := handler.(*TranslateHandler); ok && h.Context.Enabled {
		e, err := ExpandMainContextBounded(b, doc, indices, len(doc.Segments), max(h.Context.Before, h.Context.After), h.Context.MaxChars, runtime.handoffLimit())
		if err != nil {
			return nil, err
		}
		expanded, truncated = e.Idxs, e.TruncatedSrc
	}
	if err = backend.ReserveSlice[Segment](b, int64(len(expanded))); err != nil {
		return nil, err
	}
	input := &preparedMainDocument{bounds: b, doc: &Document{SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Format: doc.Format, Vars: doc.Vars}, global: expanded}
	input.doc.Segments = make([]Segment, len(expanded))
	if err = backend.ReserveSlice[int](b, int64(len(expanded))); err != nil {
		return nil, err
	}
	if err = backend.ReserveSlice[int](b, int64(len(indices))); err != nil {
		return nil, err
	}
	input.locals = make([]int, len(expanded))
	input.targets = make([]int, 0, len(indices))
	for i := range input.locals {
		input.locals[i] = i
	}
	input.context, err = backend.MakeMap[int, struct{}](b, input.locals)
	if err != nil {
		return nil, err
	}
	if err = backend.ReserveSlice[int](b, int64(len(truncated))); err != nil {
		return nil, err
	}
	truncatedKeys := make([]int, 0, len(truncated))
	for local, idx := range expanded {
		if _, ok := truncated[idx]; ok {
			truncatedKeys = append(truncatedKeys, local)
		}
	}
	input.truncated, err = backend.MakeMap[int, string](b, truncatedKeys)
	if err != nil {
		return nil, err
	}
	want, err := backend.MakeMap[int, bool](b, indices)
	if err != nil {
		return nil, err
	}
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

func (input *preparedMainDocument) close() {
	if input == nil {
		return
	}
	b := input.bounds
	*input = preparedMainDocument{}
	b.CloseRetired()
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

type mainBackingOwner struct {
	permits   []*backend.RequestPermit
	rawCredit *backend.OwnedAllocation
	ctx       context.Context
}

func (r *ExecutionRuntime) retainMainOwner(id string, permits []*backend.RequestPermit, rawCredit *backend.OwnedAllocation, ctx context.Context) {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	if r.mainOwners == nil {
		r.mainOwners = map[string]mainBackingOwner{}
	}
	r.mainOwners[id] = mainBackingOwner{permits, rawCredit, ctx}
}

// Promotion borrows the original live completion lane. Saved inventory without
// an in-process owner uses its ordinary build callbacks and gains no new R.
func (r *ExecutionRuntime) mainResponseBuildContext(ctx context.Context, id string) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.ownerMu.Lock()
	owner, exists := r.mainOwners[id]
	r.ownerMu.Unlock()
	if !exists || owner.ctx == nil {
		return ctx, nil
	}
	ctx = completionContext{Context: ctx, values: owner.ctx}
	admission, _ := backend.BuildOwnership(ctx)
	if admission != r.Admission || !backend.HasActiveBuildAllocationSource(ctx) {
		return nil, backend.ErrPipelineCapacity
	}
	ctx = backend.WithProviderResponseOwnership(ctx, r.Admission, id, backend.RequestStageMain)
	return backend.WithProviderResponseAllocation(ctx, r.responseLimit())
}

func (r *ExecutionRuntime) releaseMainOwner(id string) error {
	_ = r.Admission.ReleaseBuild("raw:" + id)
	r.Admission.ObserveOwned("freeze", id, 0)
	r.Admission.ObserveOwned("raw", id, 0)
	r.ownerMu.Lock()
	owner := r.mainOwners[id]
	r.ownerMu.Unlock()
	owner.rawCredit.Release()
	if owner.ctx != nil {
		if err := backend.CloseProviderResponseAllocation(owner.ctx); err != nil {
			return err
		}
	}
	r.ownerMu.Lock()
	delete(r.mainOwners, id)
	r.ownerMu.Unlock()
	_ = r.Admission.ReleaseBuild("build:" + id)
	for _, permit := range owner.permits {
		permit.Release()
	}
	if len(owner.permits) == 0 {
		r.Admission.ReleaseDurableResponse(id)
	}
	return nil
}

func prepareMainInvocation(ctx context.Context, h stagedHandler, input *preparedMainDocument, session *requestSession, attempt int, logger *slog.Logger) (result batchResult) {
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
	var requestBounds *backend.BuildBounds
	defer func() {
		req = backend.Request{}
		doc = nil
		input.close()
		if requestBounds == nil {
			return
		}
		// The store/session can still borrow these outputs on failure. Preserve
		// only their exact original backing before retiring rendering temporaries.
		retained, retainErr := backend.CheckedAdd(int64(cap(session.intent.FreezePayload)), int64(len(session.intent.SnapshotDigest)))
		membersBytes, sizeErr := backend.AllocationBytes(int64(cap(session.intent.PromotionMembers)), int64(unsafe.Sizeof(ResponseMember{})))
		if retainErr == nil {
			retainErr = sizeErr
		}
		if retainErr == nil {
			retained, retainErr = backend.CheckedAdd(retained, membersBytes)
		}
		if retainErr == nil && retained > 0 {
			var receipt *backend.OwnedAllocation
			receipt, retainErr = requestBounds.RetainedAllocation(retained)
			if retainErr == nil {
				retainErr = receipt.DetachBuild()
			}
			if retainErr != nil && receipt != nil {
				_ = receipt.ReturnToBuild()
			}
		}
		if retainErr != nil {
			result.err = errors.Join(result.err, retainErr)
			return
		}
		requestBounds.CloseRetired()
	}()
	buildErr := session.runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, session.runtime.Window.limits.ItemBytes, session.runtime.handoffLimit())
		if err != nil {
			return err
		}
		requestBounds = b
		frozen := frozenMainBatch{Mode: h.ModeName(), Format: doc.Format, SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Pool: session.intent.Pool, MainAttempt: attempt + 1}
		switch handler := h.(type) {
		case *TranslateHandler:
			if err = backend.ReserveSlice[string](b, int64(len(input.targets))); err != nil {
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
				if frozen.TextMode {
					ids[i] = strconv.Itoa(i + 1)
				} else {
					ids[i] = strconv.Itoa(local + 1)
				}
				if err = b.ReserveText(int64(len(ids[i])), int64(len(ids[i]))); err != nil {
					return err
				}
			}
			copy.buildClaim = func(skeleton backend.Request, m backend.PromptMeasure) error {
				return freezeMeasuredMain(ctx, b, h, input, session, frozen, ids, nil, remote, skeleton, m)
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
				return freezeMeasuredMain(ctx, b, h, input, session, frozen, ids, states, remote, req, m)
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

func freezeMeasuredMain(ctx context.Context, b *backend.BuildBounds, h stagedHandler, input *preparedMainDocument, session *requestSession, frozen frozenMainBatch, ids []string, states []reviseProtectState, remote backend.Backend, req backend.Request, m backend.PromptMeasure) error {
	doc := input.doc
	if err := backend.ReserveSlice[frozenMainMember](b, int64(len(input.targets))); err != nil {
		return err
	}
	if err := backend.ReserveSlice[ResponseMember](b, int64(len(input.targets))); err != nil {
		return err
	}
	frozen.Members = make([]frozenMainMember, 0, len(input.targets))
	session.intent.PromotionMembers = make([]ResponseMember, 0, len(input.targets))
	for i, local := range input.targets {
		seg := doc.Segments[local]
		global := input.global[local]
		candidateID := NewWorkID()
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
	wireBytes, err := backend.MeasureRequestPromptContext(ctx, remote, req, m)
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
	payload, err := b.EncodeJSONMeasured(frozen, freezeBytes, session.runtime.handoffLimit())
	if err != nil {
		return err
	}
	session.intent.FreezePayload = payload
	session.runtime.Admission.ObserveOwned("freeze", session.intent.ID, int64(cap(payload)))
	session.mode = h.ModeName()
	if err = b.ReserveAllocation(64, 1); err != nil {
		return err
	}
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
	input = nil
	defer func() {
		transferred := session.rawResponse != nil
		// The retained provider context contains this session. After dispatch
		// publishes the header, only the store may retain the immutable payload.
		session.rawResponse = nil
		session.intent.FreezePayload = nil
		session.intent.PromotionMembers = nil
		session.intent.Indices = nil
		session.intent.Members = nil
		if !transferred {
			_ = runtime.Admission.ReleaseBuild("build:" + reservation)
		}
	}()

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
		if result.waitCapacity.Cause == nil && result.waitCapacity.Wait.Reason != "aggregate_capacity" {
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
	buildOwner := "promotion:" + header.ID
	ctx = runtime.BuildContext(ctx, buildOwner)
	defer runtime.Admission.ReleaseBuild(buildOwner)
	e = stagedEvent{rawPromotion: true, cursors: map[int]WorkCursor{}}
	var sourceErr error
	ctx, sourceErr = runtime.mainResponseBuildContext(ctx, header.ID)
	if sourceErr != nil {
		e.err = sourceErr
		return e
	}
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
	responseRelease := response.ReleaseOwned
	releaseResponse := func() {
		release := responseRelease
		responseRelease = nil
		response = MainResponse{}
		if release != nil {
			release()
		}
	}
	defer releaseResponse()
	if err != nil {
		e.err = err
		return e
	}
	runtime.Admission.ObserveOwned("freeze", header.ID+"/promotion", int64(cap(response.FreezePayload)))
	runtime.Admission.ObserveOwned("raw", header.ID+"/promotion", int64(cap(response.Raw)))
	defer runtime.Admission.ObserveOwned("freeze", header.ID+"/promotion", 0)
	defer runtime.Admission.ObserveOwned("raw", header.ID+"/promotion", 0)
	var frozen frozenMainBatch
	var frozenBounds *backend.BuildBounds
	var frozenAllocation *backend.OwnedAllocation
	defer func() { frozen = frozenMainBatch{}; frozenAllocation.Release(); frozenBounds.CloseRetired() }()
	err = runtime.Run(ctx, func() error {
		b, e := backend.NewBuildBoundsFromContext(ctx, runtime.Window.limits.ItemBytes, header.FreezeBytes)
		if e != nil {
			return e
		}
		frozenBounds = b
		defer b.CloseRetired()
		var full frozenMainBatch
		defer func() { full = frozenMainBatch{} }()
		if e = b.DecodeJSON(response.FreezePayload, &full); e != nil {
			return e
		}
		if e = backend.ReserveSlice[frozenMainMember](b, int64(len(full.Members))); e != nil {
			return e
		}
		frozen = full
		frozen.Members = make([]frozenMainMember, len(full.Members))
		for i, m := range full.Members {
			frozen.Members[i] = frozenMainMember{Index: m.Index, PromptID: m.PromptID, CandidateID: m.CandidateID}
		}
		full = frozenMainBatch{}
		retained, e := backend.ValueAllocationBytes(frozen)
		if e != nil {
			frozen = frozenMainBatch{}
			return e
		}
		frozenAllocation, e = b.RetainedAllocation(retained)
		if e == nil {
			e = frozenAllocation.DetachBuild()
		}
		if e != nil {
			frozen = frozenMainBatch{}
			frozenAllocation.Release()
			return e
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
		if err == nil && response.ReleaseOwned != nil {
			previous, businessRelease := responseRelease, response.ReleaseOwned
			responseRelease = func() {
				if businessRelease != nil {
					businessRelease()
				}
				if previous != nil {
					previous()
				}
			}
		}
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
	var candidateBounds *backend.BuildBounds
	var inventoryPayload bool
	var memoryAllocation bool
	var promotionMemory *MemoryRoundStore
	var promotionCandidate *Candidate
	pendingPromotion := ""
	defer func() {
		c = nil
		candidateBounds.CloseRetired()
		if pendingPromotion != "" {
			runtime.Admission.AbortPromotion(pendingPromotion)
		}
		if promotionMemory != nil {
			promotionMemory.finishPromotionAttempt(promotionCandidate)
		}
		promotionCandidate = nil
	}()
	var parseErr error
	next := WorkCursor{Pool: round.Retry.MaxAttempts + 1, State: "unresolved"}
	err = runtime.Run(ctx, func() error {
		b, e := backend.NewBuildBoundsFromContext(ctx, runtime.Window.limits.ItemBytes, header.FreezeBytes+header.ResponseBytes)
		if e != nil {
			return e
		}
		candidateBounds = b
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
			// The metadata map borrows this already-owned slice.
			if member.Segment.Meta == nil {
				member.Segment.Meta = make(map[string]any, 1)
			}
			member.Segment.Meta["ruby_items"] = member.RubyItems
		}
		c, parseErr = parseFrozenMemberBounded(b, h, full, response, header.Cursor, logger)
		if c != nil && parseErr == nil {
			workID := response.Members[header.Cursor].WorkID
			if e = b.ReserveAllocation(int64(len(workID)), 1); e != nil {
				return e
			}
			c.WorkID = strings.Clone(workID)
			if c.Alignment == nil {
				// Without alignment, the candidate retains a private segment tree
				// and distinct original frozen fields. Alignment additionally
				// aliases segment text/items; its owner must keep the parse stage
				// until those shared references have an explicit lifetime owner.
				retained, retainErr := retainedBacking(ctx, b, reflect.ValueOf(c), 0)
				if retainErr != nil {
					return retainErr
				}
				allocation, retainErr := b.RetainedAllocation(retained)
				if retainErr != nil {
					return retainErr
				}
				nextBounds, retainErr := allocation.DetachToBuild()
				if retainErr != nil {
					_ = allocation.ReturnToBuild()
					return retainErr
				}
				member = nil
				full = frozenMainBatch{}
				b.CloseRetired()
				b, candidateBounds = nextBounds, nextBounds
			}
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
		payload, payloadErr := candidateBounds.RetainedAllocation(int64(cap(c.PreparedPayload)))
		if payloadErr != nil {
			e.err = payloadErr
			return e
		}
		defer func() { _ = payload.ReturnToBuild() }()
		granted, capacityErr := runtime.Admission.BeginPromotionWithAllocation(ctx, header.ID, c.ID, size, false, payload)
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
			granted, capacityErr = runtime.Admission.BeginPromotionWithAllocation(ctx, header.ID, c.ID, size, true, payload)
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
		pendingPromotion = c.ID
		inventoryPayload = true
	}
	failure := ""
	if parseErr != nil {
		failure = parseErr.Error()
		if len(failure) > 2048 {
			failure = failure[:2048]
		}
	}
	if err = runtime.SaveNamed(ctx, persistence.Metadata{Operation: "member_promotion", Class: persistence.Reliable, ResourceID: storeResourceID(store), RequestID: header.ID}, func(saveCtx context.Context) error {
		if c != nil {
			if memory, ok := store.(interface{ MemoryStore() *MemoryRoundStore }); ok {
				promotionMemory, promotionCandidate = memory.MemoryStore(), c
				if err := memory.MemoryStore().retainPromotionAllocation(saveCtx, c, candidateBounds); err != nil {
					return err
				}
				memoryAllocation = true
			} else {
				if err := runtime.Admission.BeginPromotionSave(c.ID); err != nil {
					return err
				}
			}
		}
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
		if !memoryAllocation {
			b := candidateBounds
			retained, allocationErr := retainedBacking(ctx, b, reflect.ValueOf(c), 0)
			if allocationErr != nil {
				e.err = allocationErr
				return e
			}
			if inventoryPayload {
				payloadBytes := int64(cap(c.PreparedPayload))
				if retained < payloadBytes {
					e.err = backend.ErrBuildCapacity
					return e
				}
				retained -= payloadBytes
			}
			retainedCredit, allocationErr := b.RetainedAllocation(retained)
			if allocationErr != nil {
				e.err = fmt.Errorf("promote_retained requested=%d live=%d: %w", retained, b.Snapshot().LiveBytes, allocationErr)
				return e
			}
			if allocationErr = backend.TransferBuildAllocationToBuild(ctx, retainedCredit, "candidate:"+c.ID); allocationErr != nil {
				e.err = allocationErr
				return e
			}
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
		releaseResponse()
		frozen = frozenMainBatch{}
		m = frozenMainMember{}
		frozenAllocation.Release()
		frozenBounds.CloseRetired()
		c = nil
		candidateBounds.CloseRetired()
		if err := runtime.releaseMainOwner(header.ID); err != nil {
			e.err = err
			return e
		}
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
		data, err := b.EncodeJSON(value, b.OutputLimit())
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
		if err := backend.ReserveSlice[string](b, int64(len(frozen.Members))); err != nil {
			return nil, err
		}
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
	if err := b.ReserveAllocation(int64(len(response.ID)), 1); err != nil {
		return nil, err
	}
	candidate.ParentRequestID = strings.Clone(response.ID)
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
