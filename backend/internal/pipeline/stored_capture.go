package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

type storedResponseHandler interface {
	processStoredResponse(context.Context, *Document, []int, *backend.Response, int, *slog.Logger) batchResult
}

// StoredResponseSettlement joins publication, member cursors, and capture
// retirement. The ordered dispositions refer to the immutable input archive.
type StoredResponseSettlement struct {
	Header          MainResponseHeader
	Dispositions    []StoredResponseDisposition
	Result          *BatchResult
	GlossaryReceipt []byte
	Install         func()
	Apply           func() error
}

type StoredResponseDisposition struct {
	Index, SegmentID int
	Cursor           WorkCursor
	Accepted         bool
	Reason           string
}

type StoredResponseStore interface {
	SettleStoredResponse(context.Context, *Document, StoredResponseSettlement) error
}

var ErrStoredResponseStale = errors.New("stored response baseline changed")

type frozenStoredMember struct {
	Index        int
	ID           string
	Version      int64
	TargetDigest [sha256.Size]byte
	TargetIsNull bool
}

func storedResponseCaptured(ctx context.Context) bool {
	s, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	return s != nil && s.stored && s.rawResponse != nil
}

func storedTextMode(handler RoundHandler) bool {
	var format string
	switch h := handler.(type) {
	case *SemanticQAHandler:
		format = h.ResponseMode
	case *AdjudicateHandler:
		format = h.ResponseMode
	case *ExtractHandler:
		format = h.ResponseMode
	}
	return prompt.ProtocolFromResponseMode(format).IsText()
}

func freezeStoredRequest(ctx context.Context, s *requestSession, handler RoundHandler, doc *Document) error {
	s.intent.FrozenContext = FrozenContext{Mode: handler.ModeName(), Format: doc.Format, SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Pool: s.intent.Pool, MainAttempt: s.intent.Attempt, TextMode: storedTextMode(handler)}
	epoch := int64(0)
	if store, ok := s.store.(interface{ CaptureEpoch() int64 }); ok {
		epoch = store.CaptureEpoch()
	}
	return s.runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, s.runtime.responseLimit(), s.runtime.handoffLimit())
		if err != nil {
			return err
		}
		if err := backend.ReserveSlice[InputMemberFrame](b, int64(len(s.intent.Indices))); err != nil {
			return err
		}
		s.intent.InputMembers = make([]InputMemberFrame, 0, len(s.intent.Indices))
		hash := sha256.New()
		for ordinal, idx := range s.intent.Indices {
			seg := &doc.Segments[idx]
			member := frozenStoredMember{Index: idx, ID: seg.ID, Version: seg.ContentVersion, TargetDigest: sha256.Sum256([]byte(seg.Target)), TargetIsNull: seg.TargetIsNull}
			payload, err := b.EncodeJSON(member, s.runtime.handoffLimit())
			if err != nil {
				return err
			}
			credit, err := b.RetainedAllocation(int64(cap(payload)))
			if err != nil {
				return err
			}
			if err = credit.DetachBuild(); err != nil {
				credit.Release()
				return err
			}
			var target *string
			if !seg.TargetIsNull {
				target = &seg.Target
			}
			var workID string
			if owner, ok := s.store.(interface{ WorkIdentity(int) string }); ok {
				workID = owner.WorkIdentity(idx)
			}
			digest := memberResponseDigest(ctx, payload)
			_, _ = hash.Write([]byte(digest))
			s.intent.InputMembers = append(s.intent.InputMembers, InputMemberFrame{
				Key:            MemberKey{RequestID: s.intent.ID, Ordinal: ordinal, CaptureEpoch: epoch},
				ResponseMember: ResponseMember{Index: idx, SegmentID: seg.DBID, WorkID: workID, CandidateID: NewWorkID(), BaselineVersion: seg.ContentVersion, BaselineTarget: target, BaselineStatus: seg.Status},
				PromptID:       strconv.Itoa(idx), Payload: payload, Bytes: int64(len(payload)), Digest: digest, Allocation: credit,
			})
		}
		s.intent.InputDigest = hex.EncodeToString(hash.Sum(nil))
		s.intent.SnapshotDigest = s.intent.InputDigest
		return nil
	})
}

func storedSettlementDispositions(ctx context.Context, round Round, doc *Document, h MainResponseHeader, result *batchResult) ([]StoredResponseDisposition, []int, error) {
	runtime := round.Runtime
	b, err := backend.NewBuildBoundsFromContext(ctx, runtime.responseLimit(), runtime.handoffLimit())
	if err != nil {
		return nil, nil, err
	}
	if err := backend.ReserveSlice[StoredResponseDisposition](b, int64(h.MemberCount)); err != nil {
		return nil, nil, err
	}
	if err := backend.ReserveSlice[int](b, int64(h.MemberCount)); err != nil {
		return nil, nil, err
	}
	dispositions := make([]StoredResponseDisposition, h.MemberCount)
	valid := make([]int, 0, h.MemberCount)
	stale := false
	for ordinal := range dispositions {
		input, err := round.Store.LoadInputMember(ctx, archiveMemberKey(h, ordinal), runtime.handoffLimit())
		if err != nil {
			return nil, nil, err
		}
		idx := input.Index
		if input.Key != archiveMemberKey(h, ordinal) || idx < 0 || idx >= len(doc.Segments) || ordinal > 0 && idx <= dispositions[ordinal-1].Index {
			input.Release()
			return nil, nil, fmt.Errorf("invalid stored response input member")
		}
		var frozen frozenStoredMember
		err = runtime.Run(ctx, func() error {
			dec, err := backend.NewBuildBoundsFromContext(ctx, runtime.responseLimit(), input.Bytes)
			if err != nil {
				return err
			}
			defer dec.CloseRetired()
			return dec.DecodeJSON(input.Payload, &frozen)
		})
		input.Release()
		if err != nil {
			return nil, nil, err
		}
		seg := &doc.Segments[idx]
		if frozen.Index != idx || frozen.Version != input.BaselineVersion || input.SegmentID != seg.DBID || !seg.RoundWork.Member || seg.RoundWork.Completed {
			return nil, nil, fmt.Errorf("stored response member does not match the sealed round")
		}
		d := StoredResponseDisposition{Index: idx, SegmentID: input.SegmentID, Accepted: true}
		if seg.ContentVersion != frozen.Version || seg.ID != frozen.ID || seg.Status != input.BaselineStatus || seg.TargetIsNull != frozen.TargetIsNull || sha256.Sum256([]byte(seg.Target)) != frozen.TargetDigest {
			stale = true
			d.Accepted = false
			d.Reason = ErrStoredResponseStale.Error()
			d.Cursor = WorkCursor{Pool: seg.RoundWork.Cursor.Pool, Attempt: seg.RoundWork.Cursor.Attempt, State: "stale"}
			result.unresolved = append(result.unresolved, idx)
		} else {
			valid = append(valid, idx)
		}
		dispositions[ordinal] = d
	}
	if stale && h.Mode == RoundModeExtract {
		for i := range dispositions {
			d := &dispositions[i]
			if d.Accepted {
				seg := doc.Segments[d.Index]
				d.Accepted, d.Reason = false, ErrStoredResponseStale.Error()
				d.Cursor = WorkCursor{Pool: seg.RoundWork.Cursor.Pool, Attempt: seg.RoundWork.Cursor.Attempt, State: "stale"}
				result.unresolved = append(result.unresolved, d.Index)
			}
		}
		valid = valid[:0]
	}
	return dispositions, valid, nil
}

func consumeStoredResponse(ctx context.Context, round Round, doc *Document, header MainResponseHeader, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger) (out batchResult) {
	handler := round.Handler
	store, ok := round.Store.(StoredResponseStore)
	if !ok {
		return batchResult{err: fmt.Errorf("stored round has no response settlement store")}
	}
	if header.FormatVersion != execution.ResponseFormatVersion || header.Mode != handler.ModeName() || header.TextMode != storedTextMode(handler) || header.Format != doc.Format || header.SourceLang != doc.SourceLang || header.TargetLang != doc.TargetLang || header.Phase != responsecontract.CapturePending || header.MemberCount < 1 || header.Cursor != 0 || header.MemberCount > round.Runtime.Window.limits.Segments {
		return batchResult{err: fmt.Errorf("stored response does not match the sealed execution")}
	}
	runtime := round.Runtime
	ctx = context.WithValue(ctx, responseCapacityKey{}, header.ID)
	ctx = runtime.BuildContext(ctx, "stored-response:"+header.ID)
	defer func() {
		out.err = errors.Join(out.err, runtime.Admission.ReleaseBuild("stored-response:"+header.ID))
	}()
	// The immutable capture now belongs to its store. Parsing and settlement
	// share an independent, bounded build owner for both live and cold reads.
	// The original provider permit still owns the completion slot.
	owned, err := runtime.Admission.ClaimResponseOwner(header.ID)
	if err != nil {
		return batchResult{err: err}
	}
	if !owned {
		return batchResult{deferred: true, waitCapacity: &WorkDeferred{Wait: backend.AdmissionWait{Reason: "response_owner", CapacityChanged: runtime.Admission.Changed()}}}
	}
	dispositions, valid, err := storedSettlementDispositions(ctx, round, doc, header, &out)
	if err != nil {
		return deferredStoredBatch(ctx, err)
	}
	in := StoredResponseSettlement{Header: header, Dispositions: dispositions}
	var parsed batchResult
	var resp *backend.Response
	if len(valid) > 0 {
		capture, err := round.Store.LoadCapture(ctx, header.ID, runtime.responseLimit())
		if err != nil {
			return deferredStoredBatch(ctx, err)
		}
		defer capture.Release()
		if capture.MainResponseHeader != header {
			return batchResult{err: fmt.Errorf("stored response changed before consumption")}
		}
		// The parser and diagnostics borrow the immutable capture only until
		// settlement returns. Any result escaping this scope owns its bytes.
		raw := unsafe.String(unsafe.SliceData(capture.Payload), len(capture.Payload))
		resp = &backend.Response{Text: raw, Truncated: capture.Truncated, Usage: capture.Record.Usage}
		defer func() { resp.Text = "" }()
		parsed = handler.(storedResponseHandler).processStoredResponse(ctx, doc, valid, resp, header.MainAttempt, logger)
		if parsed.err != nil || parsed.deferred || parsed.waitCapacity != nil || ctx.Err() != nil {
			if parsed.err == nil {
				parsed.err = ctx.Err()
			}
			parsed.callbackResult, parsed.extraction = nil, nil
			return parsed
		}
		out.unresolved = append(out.unresolved, parsed.unresolved...)
		out.failedSegments, out.fatalUnresolved, out.retry = parsed.failedSegments, parsed.fatalUnresolved, parsed.retry
		in.Result = parsed.callbackResult
	}
	maxPools := max(1, round.Retry.MaxAttempts+1)
	reason := "captured model response could not be applied"
	parseErr := parsed.storedParseErr
	if parsed.extraction != nil && parsed.extraction.parseErr != nil {
		parseErr = parsed.extraction.parseErr
	}
	if parseErr != nil {
		reason = diagnosticPrefix(parseErr.Error(), 2048)
	}
	for i := range dispositions {
		d := &dispositions[i]
		if !d.Accepted {
			continue
		}
		next := WorkCursor{Pool: header.Pool, Attempt: header.MainAttempt + 1, State: "pending"}
		switch {
		case containsIndex(parsed.failedSegments, d.Index):
			next = WorkCursor{Pool: maxPools, State: "unresolved", Phase: "terminal_failure"}
		case containsIndex(parsed.fatalUnresolved, d.Index):
			next = WorkCursor{Pool: maxPools, State: "unresolved"}
		case parsed.retry != nil && containsIndex(parsed.retry.idxs, d.Index):
			next.Attempt, next.NextAttemptAt = parsed.retry.attempt, parsed.retry.notBefore
		case containsIndex(parsed.unresolved, d.Index):
			next = WorkCursor{Pool: header.Pool + 1, State: "pending"}
			if next.Pool >= maxPools {
				next.State = "unresolved"
			}
		default:
			continue
		}
		d.Accepted, d.Cursor, d.Reason = false, next, reason
	}
	save := func() error {
		return runtime.SaveNamed(ctx, persistence.Metadata{Operation: "stored_response_settlement", Class: persistence.Reliable, ResourceID: storeResourceID(round.Store), RequestID: header.ID}, func(saveCtx context.Context) error {
			settlement := in
			if batchHandler != nil && in.Result != nil && round.Store.PublicationDomain().Medium == responsecontract.PublicationMemory {
				settlement.Apply = func() error { return batchHandler(saveCtx, *in.Result) }
			}
			return store.SettleStoredResponse(saveCtx, doc, settlement)
		})
	}
	if h, ok := handler.(*ExtractHandler); ok && parsed.extraction != nil && parsed.extraction.entries != nil {
		for {
			changed := runtime.Admission.Changed()
			granted, err := runtime.Admission.TryBusinessOwner(header.ID)
			if err != nil {
				return batchResult{err: err}
			}
			if granted {
				break
			}
			select {
			case <-ctx.Done():
				return batchResult{err: ctx.Err()}
			case <-changed:
			}
		}
		err = h.publishCapturedExtraction(ctx, round.Store, in, parsed.extraction)
		if errors.Is(err, ErrStoredResponseStale) {
			for i := range dispositions {
				d := &dispositions[i]
				d.Accepted, d.Reason = false, ErrStoredResponseStale.Error()
				d.Cursor = WorkCursor{Pool: doc.Segments[d.Index].RoundWork.Cursor.Pool, State: "stale"}
				out.unresolved = append(out.unresolved, d.Index)
			}
			parsed.extraction = nil
			err = save()
		}
	} else {
		err = save()
	}
	if err != nil {
		return deferredStoredBatch(ctx, err)
	}
	if h, ok := handler.(*ExtractHandler); ok && resp != nil {
		h.emitStoredExtraction(ctx, resp, parsed)
	}
	for _, d := range dispositions {
		out.idxs = append(out.idxs, d.Index)
		if d.Accepted {
			doc.Segments[d.Index].RoundWork.Completed = true
		} else {
			if header.CaptureEpoch != activeCaptureEpoch(round.Store) && d.Cursor.State != "stale" {
				current := doc.Segments[d.Index].RoundWork.Cursor
				d.Cursor = WorkCursor{Pool: current.Pool, Attempt: current.Attempt, State: "pending"}
			}
			doc.Segments[d.Index].RoundWork.Cursor = d.Cursor
			if d.Cursor.State == "stale" && !containsIndex(out.unresolved, d.Index) {
				out.unresolved = append(out.unresolved, d.Index)
			}
		}
	}
	progress.NotifyWorkState(runtimeStoredReporter(ctx))
	return out
}

func runtimeStoredReporter(ctx context.Context) progress.Reporter {
	if s, _ := ctx.Value(requestSessionKey{}).(*requestSession); s != nil {
		return s.reporter
	}
	return nil
}

func consumeLiveStoredResponse(ctx context.Context, round Round, doc *Document, session *requestSession, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger) batchResult {
	capture := session.rawResponse
	atomic.AddInt64(&doc.InputTokens, capture.Record.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, capture.Record.Usage.CompletionTokens)
	return advanceStoredResponse(ctx, round, doc, capture.MainResponseHeader, batchHandler, logger)
}

func advanceStoredResponse(ctx context.Context, round Round, doc *Document, h MainResponseHeader, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger) batchResult {
	ctx = round.Runtime.responseAttemptContext(ctx, h.ID)
	ctx, cancel := context.WithDeadline(ctx, persistence.Deadline(ctx))
	defer cancel()
	for {
		result := consumeStoredResponse(ctx, round, doc, h, batchHandler, logger)
		if result.waitCapacity == nil {
			if result.deferred && result.err == nil {
				result.err = ctx.Err()
				if result.err == nil {
					result.err = fmt.Errorf("stored response settlement interrupted")
				}
			}
			return result
		}
		if err := waitPreparation(ctx, result.waitCapacity.Wait); err != nil {
			return batchResult{err: err, deferred: true}
		}
	}
}

type storedWindowConsumerKey struct{}

func processCapturedStoredBatch(ctx context.Context, round Round, handler batchRoundHandler, doc *Document, job batchJob, pool int, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger, reporter progress.Reporter) (out batchResult) {
	runtime, store := round.Runtime, round.Store
	id := NewWorkID()
	resource := storeResourceID(store)
	consumer, _ := ctx.Value(storedWindowConsumerKey{}).(*WindowConsumer)
	if consumer == nil {
		return batchResult{err: fmt.Errorf("stored round has no window consumer")}
	}
	intent := prepareIntent(handler, store, backend.RequestStageMain)
	ticket, err := runtime.Preparation.Try(ctx, intent, round.Gate)
	if err != nil {
		return deferredStoredBatch(ctx, err)
	}
	defer ticket.Release()
	ctx = withPreparationTicket(withCoordinatorPreparation(ctx), ticket)
	session := &requestSession{stored: true, mode: handler.ModeName(), mainRetry: round.Retry, runtime: runtime, store: store, reporter: reporter,
		intent: RequestIntent{ID: id, Stage: backend.RequestStageMain, RoundIndex: handlerRoundIndex(handler), ResourceID: resource, Indices: job.idxs, Pool: pool, Attempt: job.attempt, Phase: "main"}}
	callCtx := session.Context(ctx)
	defer func() {
		for i := range session.intent.InputMembers {
			session.intent.InputMembers[i].Allocation.Release()
		}
		session.intent.InputMembers = nil
		if out.err != nil {
			session.fail(out.err)
		}
		if err := session.finish(); err != nil {
			out.err = errors.Join(out.err, err)
		}
		releaseMainBatchDiagnostic(session)
		if session.rawResponse == nil {
			out.err = errors.Join(out.err, runtime.Admission.ReleaseBuild("build:"+id))
		}
	}()
	if err = freezeStoredRequest(callCtx, session, handler, doc); err != nil {
		return deferredStoredBatch(ctx, err)
	}
	keys := memberReservationKeys(store, handler.ModeName(), job.idxs)
	var reservation WindowReservationResult
	var wake <-chan struct{}
	if runtime.BudgetLimits().Segments == 1 {
		reservation, wake = runtime.Window.TryReserveOwned(id, len(job.idxs), resource, consumer, keys)
	} else {
		reservation, wake = runtime.Window.TryPrepareOwned(id, len(job.idxs), resource, consumer, keys)
	}
	switch reservation {
	case WindowBusy:
		return batchResult{deferred: true, waitCapacity: &WorkDeferred{Wait: backend.AdmissionWait{Reason: "candidate_window", CapacityChanged: wake}, Yield: !runtime.Window.CanDrainFor(consumer) && resource > 0}}
	case WindowTooLarge, WindowPinnedLimit:
		return batchResult{err: ErrCandidateCapacity}
	}
	reservationFinished := false
	defer func() {
		if !reservationFinished && session.rawResponse == nil {
			runtime.Window.Release(id)
		}
	}()
	result := handler.ProcessBatch(callCtx, doc, job.idxs, job.attempt, logger)
	if session.err != nil {
		return batchResult{err: session.err, deferred: true}
	}
	if session.rawResponse != nil {
		if err := runtime.Window.RenameReservation(id, id); err != nil {
			return batchResult{err: err}
		}
		result = consumeLiveStoredResponse(callCtx, round, doc, session, batchHandler, logger)
		if result.err == nil {
			if err = releaseStoredWindow(ctx, round, doc, id, result.idxs); err != nil {
				result.err = err
			}
			result.err = errors.Join(result.err, runtime.releaseMainOwner(id))
			reservationFinished = true
		}
		return result
	}
	if session.deferred != nil {
		return batchResult{waitCapacity: session.deferred, deferred: true}
	}
	if session.mainFailure != nil && result.err == nil && !result.deferred {
		next := *session.mainFailure
		if store, ok := store.(interface{ PersistsMainFailure() bool }); !ok || !store.PersistsMainFailure() {
			transitions := make([]CursorTransition, 0, len(job.idxs))
			for _, idx := range job.idxs {
				transitions = append(transitions, CursorTransition{Index: idx, Cursor: next})
			}
			if err := runtime.Save(callCtx, func(saveCtx context.Context) error { return round.Store.Cursors(saveCtx, transitions) }); err != nil {
				return batchResult{err: errors.Join(err, session.providerFailure), deferred: true}
			}
		}
		for _, idx := range job.idxs {
			doc.Segments[idx].RoundWork.Cursor = next
		}
		if err = releaseStoredWindow(ctx, round, doc, id, job.idxs); err != nil {
			return batchResult{err: err}
		}
		reservationFinished = true
		if next.Pool == pool && next.State == "pending" {
			return batchResult{retry: &batchJob{idxs: job.idxs, attempt: next.Attempt, notBefore: next.NextAttemptAt}}
		}
		return batchResult{unresolved: job.idxs}
	}
	if ctx.Err() != nil {
		return batchResult{deferred: true}
	}
	if result.err != nil || result.waitCapacity != nil || result.deferred {
		return result
	}
	// A render failure precedes reservation and does not consume a provider
	// attempt. Move it forward using the same cursor contract as other failures.
	maxPools := max(1, round.Retry.MaxAttempts+1)
	next := WorkCursor{Pool: pool + 1, State: "pending"}
	if len(result.failedSegments) > 0 || len(result.fatalUnresolved) > 0 {
		next.Pool = maxPools
	}
	if next.Pool >= maxPools {
		next.State = "unresolved"
	}
	if len(result.failedSegments) > 0 {
		next.Phase = "terminal_failure"
	}
	if len(result.failedSegments)+len(result.fatalUnresolved)+len(result.unresolved) == 0 {
		return batchResult{err: fmt.Errorf("stored request returned without a capture or a failure")}
	}
	transitions := make([]CursorTransition, 0, len(job.idxs))
	for _, idx := range job.idxs {
		transitions = append(transitions, CursorTransition{Index: idx, Cursor: next})
	}
	if err := runtime.Save(callCtx, func(saveCtx context.Context) error { return round.Store.Cursors(saveCtx, transitions) }); err != nil {
		return batchResult{err: errors.Join(err, session.providerFailure), deferred: true}
	}
	return result
}

func deferredStoredBatch(ctx context.Context, err error) batchResult {
	if storedDispatchDeferred(ctx, err) {
		return batchResult{deferred: true}
	}
	if d := workDeferred(err); d != nil {
		return batchResult{deferred: true, waitCapacity: d}
	}
	return batchResult{err: err, deferred: true}
}

func releaseStoredWindow(ctx context.Context, round Round, doc *Document, id string, indices []int) error {
	var pending []int
	for _, idx := range indices {
		w := doc.Segments[idx].RoundWork
		if !w.Completed && w.Cursor.State == "pending" {
			pending = append(pending, idx)
		}
	}
	if err := round.Runtime.Window.SplitOwnedMembers(id, memberReservationKeys(round.Store, round.Handler.ModeName(), pending)); err != nil {
		return err
	}
	return round.Runtime.RefreshEpisode(ctx)
}

func activeCaptureEpoch(store RoundStore) int64 {
	if s, ok := store.(interface{ CaptureEpoch() int64 }); ok {
		return s.CaptureEpoch()
	}
	return 0
}

func recoverStoredResponses(ctx context.Context, round Round, doc *Document, batchHandler func(context.Context, BatchResult) error, logger *slog.Logger, reporter progress.Reporter, consumer *WindowConsumer) (failures int, err error) {
	runtime, store := round.Runtime, round.Store
	for after := 0; ; {
		if err := ctx.Err(); err != nil || round.Gate.Paused() {
			return failures, err
		}
		headers, next, err := store.ResponseHeaders(ctx, after, 16)
		if err != nil {
			return failures, &StorageError{Operation: "load_stored_responses", Err: err}
		}
		if len(headers) == 0 {
			return failures, nil
		}
		for _, h := range headers {
			if err := ctx.Err(); err != nil || round.Gate.Paused() {
				return failures, err
			}
			if h.MetadataBytes > 0 {
				if err := runtime.Admission.RestoreRequestMetadata(h.ID, h.MemberCount, h.MetadataBytes); err != nil {
					return failures, fmt.Errorf("restore stored request %s metadata: %w", h.ID, err)
				}
			}
			if err := runtime.Admission.RestoreResponseOwnership(h.ID, handlerRoundIndex(round.Handler), storeResourceID(store), h.Facts(), h.Phase, false); err != nil {
				return failures, fmt.Errorf("restore stored response %s: %w", h.ID, err)
			}
			if err := runtime.Window.RestoreRaw(h.ID, h.MemberCount, storeResourceID(store), storeRoundID(store), consumer); err != nil {
				return failures, fmt.Errorf("restore stored request %s member window: %w", h.ID, err)
			}
			result := advanceStoredResponse(ctx, round, doc, h, batchHandler, logger)
			if result.err != nil {
				return failures, result.err
			}
			if len(result.failedSegments) > 0 {
				failures++
			}
			if result.retry == nil {
				notifyResolvedSegments(result.idxs, result, reporter)
			}
			if err := releaseStoredWindow(ctx, round, doc, h.ID, result.idxs); err != nil {
				return failures, err
			}
			if err := runtime.releaseMainOwner(h.ID); err != nil {
				return failures, err
			}
			progress.NotifyWorkState(reporter)
		}
		after = next
	}
}
