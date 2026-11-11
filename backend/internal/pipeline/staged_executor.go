package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type stagedHandler interface {
	RoundHandler
	PrepareBatch(context.Context, *Document, []int, int, *slog.Logger) batchResult
}

type stagedEvent struct {
	rawPromotion          bool
	yield                 bool
	responseHeaders       []MainResponseHeader
	responsePage          bool
	responseAdvance       *MainResponseHeader
	waitCapacity          *WorkDeferred
	waitIndices           []int
	waitPool, waitAttempt int
	page                  bool
	nextPage              int
	lastPage              bool
	main                  bool
	indices               []int
	cursors               map[int]WorkCursor
	candidates            []*Candidate
	completed             *TranslatedSegment
	candidate             *Candidate
	outcome               CommitOutcome
	err                   error
	inputTokens           int64
	outputTokens          int64
	members               []stagedEvent
}

// ErrResourceYield suspends an unfinished resource after all its callbacks have
// joined. Its persisted cursors remain authoritative on re-admission.
var ErrResourceYield = errors.New("resource yields to recovered candidates")

// RunStagedRound keeps the pool barrier until every accepted candidate has a
// durable destination. Main workers finish at reliable handoff, not alignment.
// Only this goroutine applies confirmed results to the resource Document.
func RunStagedRound(ctx context.Context, round Round, doc *Document, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporter progress.Reporter) (RunRoundResult, error) {
	ctx = WithExecutionRuntime(ctx, runtime)
	h, ok := round.Handler.(stagedHandler)
	if !ok {
		return RunRound(ctx, round, doc, nil, logger, reporter)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if reporter == nil {
		reporter = progress.Nop{}
	}
	if store == nil {
		store = NewMemoryRoundStore(nil)
	}
	payloadStore, ok := store.(CandidatePayloadStore)
	if !ok {
		return RunRoundResult{}, fmt.Errorf("round store has no bounded candidate payload contract")
	}
	consumer := runtime.Window.EnterScope(storeResourceID(store), storeRoundID(store), prepareIntent(round.Handler, store, backend.RequestStageMain).RoundIndex)
	defer consumer.Leave()
	canonical, ok := store.(CanonicalRoundStore)
	if !ok {
		return RunRoundResult{}, fmt.Errorf("round store has no canonical membership contract")
	}
	sealed, total, err := canonical.LoadCanonical(ctx, doc)
	if err != nil {
		return RunRoundResult{}, err
	}
	if !sealed {
		total, err = prepareCanonicalMembership(ctx, h, doc, runtime)
		if err != nil {
			return RunRoundResult{}, err
		}
		err = runtime.SaveNamed(ctx, persistence.Metadata{Operation: "seal", Class: persistence.Normal, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error {
			return canonical.SealCanonical(saveCtx, doc)
		})
		if err != nil {
			return RunRoundResult{}, err
		}
	} else if handler, ok := h.(*TranslateHandler); ok {
		// Durable membership is authoritative on recovery, while a reloaded
		// document still contains original source text. Prepare each unfinished
		// member before fresh dispatch without resetting its durable cursor.
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			if !seg.RoundWork.Member || seg.RoundWork.Completed {
				continue
			}
			if err := prepareCanonicalTranslateSegment(ctx, handler, seg, runtime); err != nil {
				return RunRoundResult{}, err
			}
		}
	}
	if total == 0 {
		return RunRoundResult{}, nil
	}
	reporter.StageStart(h.ModeName(), total)
	defer reporter.StageDone()
	if checked, ok := reporter.(interface{ StageError() error }); ok {
		if err := checked.StageError(); err != nil {
			return RunRoundResult{}, err
		}
	}
	remaining := func(yield func(int) bool) { canonicalRemaining(doc, yield) }
	completed := func(yield func(int) bool) { canonicalCompleted(doc, yield) }
	candidates := map[int]*Candidate{}
	responses := map[string]MainResponseHeader{}
	responseActive := map[string]bool{}
	responseBlocked := map[string]*WorkDeferred{}
	responseAfter, responsePageActive, responseRecoveryDone := 0, false, false
	alignmentReadyAt := map[int]time.Time{}
	// Decode at most one page of recovery payloads. The Job has reconstructed
	// aggregate occupancy from small headers before any resource is admitted.
	pageAfter, pageActive, recoveryDone := 0, false, false
	maxPools := max(1, round.Retry.MaxAttempts+1)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan stagedEvent, 32)
	var wg sync.WaitGroup
	active := map[int]bool{}
	mainActive, candidateActive, promotionActive := 0, 0, 0
	var firstErr error
	yielded := false
	type waitingWindowBatch struct {
		indices       []int
		pool, attempt int
		wake          <-chan struct{}
		reason        string
	}
	var waitingBatch *waitingWindowBatch
	paused := func() bool { return runtime.Gate != nil && runtime.Gate.Paused() }
	applyMember := func(e stagedEvent) {
		for _, header := range e.responseHeaders {
			responses[header.ID] = header
		}
		if e.responseAdvance != nil {
			header := *e.responseAdvance
			responseActive[header.ID] = false
			delete(responseBlocked, header.ID)
			if header.Cursor >= header.MemberCount {
				delete(responses, header.ID)
			} else {
				responses[header.ID] = header
			}
		}
		for idx, cursor := range e.cursors {
			doc.Segments[idx].RoundWork.Cursor = cursor
		}
		for _, c := range e.candidates {
			if !doc.Segments[c.Index].RoundWork.Member || doc.Segments[c.Index].RoundWork.Completed {
				if firstErr == nil {
					firstErr = fmt.Errorf("candidate is outside unfinished manifest")
					cancel()
				}
				continue
			}
			candidates[c.Index] = HeaderForCandidate(c).Candidate()
			alignmentReadyAt[c.Index] = time.Now()
		}
		if e.candidate != nil {
			c := e.candidate
			candidates[c.Index] = HeaderForCandidate(c).Candidate()
			alignmentReadyAt[c.Index] = maxTime(time.Now(), c.NextAttemptAt)
			if e.outcome == CommitAccepted || e.outcome == CommitExisting || e.outcome == CommitNoop {
				if e.completed != nil {
					if e.completed.PreparedPayload != nil {
						err := runtime.Run(ctx, func() error {
							b, e2 := backend.NewBuildBounds(runtime.Window.limits.ItemBytes, int64(len(e.completed.PreparedPayload)))
							if e2 != nil {
								return e2
							}
							full, e2 := restoreCommitResult(b, *e.completed)
							if e2 != nil {
								return e2
							}
							// Parser positions remain typed in the canonical input. The
							// compact JSON handoff preserves their values, then this short
							// confirmation step restores their native representation.
							if e2 = b.ReserveNodes(int64(len(doc.Segments[c.Index].Meta))+1, 1024); e2 != nil {
								return e2
							}
							meta := Segment{Meta: full.Meta}
							if e2 = restoreMetadataTypes(&meta, metadataTypes(doc.Segments[c.Index].Meta), b); e2 != nil {
								return e2
							}
							full.Meta = meta.Meta
							*e.completed = full
							doc.Segments[c.Index].Target = full.TargetText
							doc.Segments[c.Index].Issues = full.Issues
							doc.Segments[c.Index].Meta = full.Meta
							doc.Segments[c.Index].Protected = full.Protected
							return nil
						})
						if err != nil {
							if firstErr == nil {
								firstErr = err
								cancel()
							}
							return
						}
					}
					applied := false
					if local, ok := store.(interface {
						ApplyConfirmed(context.Context, *Candidate, TranslatedSegment) (bool, error)
					}); ok {
						var err error
						applied, err = local.ApplyConfirmed(ctx, c, *e.completed)
						if err != nil {
							if firstErr == nil {
								firstErr = err
								cancel()
							}
							return
						}
					}
					if !applied {
						doc.Segments[c.Index].Target = e.completed.TargetText
						doc.Segments[c.Index].Issues = e.completed.Issues
						doc.Segments[c.Index].Status = c.Segment.Status
						doc.Segments[c.Index].ContentVersion = c.Segment.ContentVersion
						doc.Segments[c.Index].TargetIsNull = false
					}
				}
				doc.Segments[c.Index].RoundWork.Completed = true
				delete(candidates, c.Index)
				delete(alignmentReadyAt, c.Index)
				runtime.Window.Release(c.ID)
				if err := runtime.RefreshEpisode(ctx); err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				reporter.SegmentDone()
			} else if e.outcome == CommitRejected || e.outcome == CommitStale {
				delete(candidates, c.Index)
				delete(alignmentReadyAt, c.Index)
				if cursor, ok := e.cursors[c.Index]; ok && cursor.State == "pending" {
					if err := runtime.Window.RetireToMember(c.ID, memberReservationKeys(store, h.ModeName(), []int{c.Index})[0]); err != nil && firstErr == nil {
						firstErr = err
						cancel()
					}
				} else {
					runtime.Window.Release(c.ID)
				}
				if err := runtime.RefreshEpisode(ctx); err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				if _, persisted := e.cursors[c.Index]; !persisted {
					doc.Segments[c.Index].RoundWork.Cursor = WorkCursor{Pool: maxPools, State: "unresolved"}
				}
			}
		}
		// Terminal storage failures stop shared dispatch.
		// Preserve that cause when the coordinator observed the derived closure
		// first; independent errors and caller cancellation remain authoritative.
		if storageFailure(e.err) && ctx.Err() == nil && !errors.Is(e.err, context.Canceled) && !errors.Is(e.err, backend.ErrDispatchPaused) {
			// Candidate commits can fail without a request session, whose failure
			// hook normally stops shared dispatch. Wake other resource producers
			// that are waiting on this response's full ownership, while keeping
			// the reliable saver and already admitted response facts available.
			runtime.stopDispatch()
		}
		if e.err != nil && (firstErr == nil || storageFailure(e.err) && ctx.Err() == nil && (firstErr == backend.ErrAdmissionClosed || firstErr == context.Canceled)) {
			firstErr = e.err
			cancel()
		}
	}
	apply := func(e stagedEvent) {
		if e.yield || e.waitCapacity != nil && e.waitCapacity.Yield {
			yielded = true
		}
		atomic.AddInt64(&doc.InputTokens, e.inputTokens)
		atomic.AddInt64(&doc.OutputTokens, e.outputTokens)
		if e.responsePage {
			responsePageActive = false
			responseAfter = e.nextPage
			responseRecoveryDone = e.lastPage && e.err == nil
		} else if e.page {
			pageActive = false
			pageAfter = e.nextPage
			recoveryDone = e.lastPage && e.err == nil && runCtx.Err() == nil
			if recoveryDone {
				consumer.RecoveryComplete()
			}
		} else if e.rawPromotion {
			promotionActive--
		} else if e.main {
			mainActive--
		} else {
			candidateActive--
		}
		for _, idx := range e.indices {
			delete(active, idx)
		}
		if e.waitCapacity != nil {
			if e.main && len(e.waitIndices) > 0 {
				waitingBatch = &waitingWindowBatch{indices: e.waitIndices, pool: e.waitPool, attempt: e.waitAttempt, wake: e.waitCapacity.Wait.CapacityChanged, reason: e.waitCapacity.Wait.Reason}
			}
			if e.responseAdvance != nil {
				applyMember(e)
				responseBlocked[e.responseAdvance.ID] = e.waitCapacity
			}
			return
		}
		applyMember(e)
		for _, member := range e.members {
			applyMember(member)
		}
	}
	for {
		if runCtx.Err() != nil || paused() || yielded || firstErr != nil {
			runtime.Admission.YieldMainPriority(storeResourceID(store), prepareIntent(round.Handler, store, backend.RequestStageMain).RoundIndex)
		}
		windowChanged := runtime.Window.Changed()
		preparationChanged := runtime.Preparation.Changed()
		admissionChanged := runtime.Admission.Changed()
		globalPolicyChanged := runtime.Admission.PolicyChanged()
		var capacityWaits []*WorkDeferred
		var windowWake <-chan struct{}
		var batchDeadline time.Time
		if runCtx.Err() == nil && !paused() && !yielded {
			if !responseRecoveryDone && !responsePageActive && len(responses) == 0 {
				responsePageActive = true
				wg.Add(1)
				go func(after int) {
					defer wg.Done()
					headers, next, err := store.ResponseHeaders(runCtx, after, 16)
					for _, header := range headers {
						if err == nil {
							i := prepareIntent(round.Handler, store, backend.RequestStageMain)
							err = runtime.Admission.RestoreResponseOwner(header.ID, i.RoundIndex, storeResourceID(store), header.FreezeBytes, header.ResponseBytes, header.BusinessBound)
							if err == nil && header.MetadataBytes > 0 {
								err = runtime.Admission.RestoreRequestMetadata(header.ID, header.MemberCount, header.MetadataBytes)
							}
						}
						if err == nil {
							err = runtime.Window.RestoreRaw(header.ID, header.MemberCount-header.Cursor, storeResourceID(store), storeRoundID(store), consumer)
						}
					}
					events <- stagedEvent{responsePage: true, nextPage: next, lastPage: len(headers) == 0, responseHeaders: headers, err: err}
				}(responseAfter)
			}
			if len(responses) > 0 {
				var ready []MainResponseHeader
				businessOwner := runtime.Admission.RuntimeState().BusinessOwner
				for id, header := range responses {
					if deferred := responseBlocked[id]; deferred != nil {
						ready := false
						select {
						case <-deferred.Wait.CapacityChanged:
							ready = true
						default:
						}
						if !deferred.Wait.AvailableAt.IsZero() && !time.Now().Before(deferred.Wait.AvailableAt) {
							ready = true
						}
						if !ready {
							capacityWaits = append(capacityWaits, deferred)
							continue
						}
						delete(responseBlocked, id)
					}
					if !responseActive[id] && (businessOwner == "" || businessOwner == id || header.Phase == "glossary_processed" || runtime.Window.TransferAllowed(businessOwner, storeResourceID(store), prepareIntent(round.Handler, store, backend.RequestStageMain).RoundIndex)) {
						ready = append(ready, header)
					}
				}
				sort.Slice(ready, func(i, j int) bool {
					if (ready[i].ID == businessOwner) != (ready[j].ID == businessOwner) {
						return ready[i].ID == businessOwner
					}
					return ready[i].DatabaseID < ready[j].DatabaseID || ready[i].DatabaseID == ready[j].DatabaseID && ready[i].ID < ready[j].ID
				})
				for _, header := range ready {
					if promotionActive >= 2 {
						break
					}
					responseActive[header.ID] = true
					promotionActive++
					wg.Add(1)
					go func(header MainResponseHeader) {
						defer wg.Done()
						events <- promoteCapturedMain(runCtx, round, h, header, store, runtime, logger)
					}(header)
				}
			}
			if !recoveryDone && !pageActive && len(candidates) == 0 {
				pageActive = true
				wg.Add(1)
				go func(after int) {
					defer wg.Done()
					page, next, err := payloadStore.CandidateHeaders(runCtx, after, 16)
					e := stagedEvent{page: true, nextPage: next, lastPage: len(page) == 0, err: err}
					if err == nil && len(page) > 0 && next <= after {
						e.err = fmt.Errorf("candidate page did not advance")
					}
					for _, c := range page {
						bytes := c.StoredBytes
						var err error
						if bytes == 0 {
							var payload []byte
							payload, err = c.Encode()
							bytes = int64(len(payload))
						}
						if err == nil {
							err = runtime.Window.ClaimRecovered(c.ID, bytes, consumer)
						}
						if err != nil {
							e.err = err
							break
						}
						e.candidates = append(e.candidates, c)
					}
					events <- e
				}(pageAfter)
			}
			// Restored/accepted drafts have priority over producing new payloads.
			keys := make([]int, 0, len(candidates))
			for idx := range candidates {
				keys = append(keys, idx)
			}
			sort.Ints(keys)
			batchConfig := alignmentBatchSettings(round.Handler)
			mayProduce := !recoveryDone || mainActive > 0
			if !mayProduce {
				for idx := range remaining {
					if candidates[idx] == nil && doc.Segments[idx].RoundWork.Cursor.Pool < maxPools && doc.Segments[idx].RoundWork.Cursor.State != "unresolved" {
						mayProduce = true
						break
					}
				}
			}
			considered := make(map[int]bool)
			for _, idx := range keys {
				if candidateActive >= max(2, runtime.Admission.StageCapacity(backend.RequestStageAlignment, 0)) {
					break
				}
				c := candidates[idx]
				if active[idx] || considered[idx] || time.Now().Before(c.NextAttemptAt) {
					continue
				}
				ready := []*Candidate{c}
				if needsAlignmentAttempt(round, c) && !c.ForceSingleAlignment && batchConfig.enabled() {
					for _, other := range keys {
						next := candidates[other]
						if other != idx && !active[other] && !considered[other] && !time.Now().Before(next.NextAttemptAt) && needsAlignmentAttempt(round, next) {
							ready = append(ready, next)
						}
					}
				}
				batch, full := readyAlignmentBatch(ready, batchConfig)
				due := alignmentReadyAt[c.Index].Add(batchConfig.Wait)
				for _, member := range batch {
					considered[member.Index] = true
					at := alignmentReadyAt[member.Index].Add(batchConfig.Wait)
					if at.Before(due) {
						due = at
					}
				}
				if !full && needsAlignmentAttempt(round, c) && mayProduce && batchConfig.Wait > 0 && time.Now().Before(due) {
					if batchDeadline.IsZero() || due.Before(batchDeadline) {
						batchDeadline = due
					}
					continue
				}
				var ticket *PreparationTicket
				if needsAlignmentAttempt(round, c) {
					var e error
					ticket, e = runtime.Preparation.Try(runCtx, prepareIntent(round.Handler, store, backend.RequestStageAlignment), runtime.Gate)
					if d := workDeferred(e); d != nil {
						capacityWaits = append(capacityWaits, d)
						continue
					}
					if e != nil {
						if !errors.Is(e, backend.ErrDispatchPaused) {
							firstErr = e
							cancel()
						}
						break
					}
				}
				for _, member := range batch {
					if !member.Ready && !needsAlignmentAttempt(round, member) {
						ok, err := runtime.Admission.ReserveCandidateGrowth(member.ID)
						if errors.Is(err, backend.ErrEarlierScope) {
							yielded = true
						}
						if errors.Is(err, backend.ErrProgressRequired) {
							err = runtime.EnsureProgress(runCtx, member.ID, member.ParentRequestID, storeResourceID(store), prepareIntent(round.Handler, store, backend.RequestStageMain).RoundIndex)
							if err == nil {
								ok, err = runtime.Admission.ReserveCandidateGrowth(member.ID)
							}
						}
						if errors.Is(err, backend.ErrProgressRequired) {
							err = nil
							ok = false
						}
						if err != nil {
							firstErr = err
							cancel()
							break
						}
						if !ok {
							capacityWaits = append(capacityWaits, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "candidate_growth", CapacityChanged: runtime.Admission.Changed()}})
							batch = nil
							break
						}
					}
				}
				if len(batch) == 0 || firstErr != nil {
					if ticket != nil {
						ticket.Release()
					}
					continue
				}
				for _, member := range batch {
					active[member.Index] = true
				}
				candidateActive++
				wg.Add(1)
				go func(batch []*Candidate, ticket *PreparationTicket) {
					defer wg.Done()
					callCtx := runCtx
					if ticket != nil {
						callCtx = withPreparationTicket(withCoordinatorPreparation(runCtx), ticket)
					}
					var e stagedEvent
					if len(batch) == 1 {
						e = processCandidate(callCtx, round, batch[0], store, runtime, logger, reporter)
					} else {
						e = processCandidates(callCtx, round, batch, store, runtime, logger, reporter)
					}
					if ticket != nil && ticket.Active() && e.err == nil {
						ticket.Release()
					}
					events <- e
				}(batch, ticket)
			}
			for recoveryDone && responseRecoveryDone && mainActive < max(1, round.Concurrency) {
				if waitingBatch != nil {
					// Policy refresh can cool a retained window waiter before any
					// stock is released. Recheck RPM so its fairness turn yields to
					// warm producers instead of monopolizing all main scopes.
					_, hintErr := runtime.Admission.ReadyHint(runCtx, prepareIntent(round.Handler, store, backend.RequestStageMain), runtime.Gate)
					if hintErr != nil && !errors.Is(hintErr, backend.ErrDispatchPaused) {
						firstErr = hintErr
						cancel()
						break
					}
					ready := false
					select {
					case <-waitingBatch.wake:
						ready = true
					default:
					}
					if !ready {
						windowWake = waitingBatch.wake
						capacityWaits = append(capacityWaits, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "candidate_window", CapacityChanged: windowWake}})
						break
					}
				}
				pool := -1
				attempt := 0
				for idx := range remaining {
					cur := doc.Segments[idx].RoundWork.Cursor
					if active[idx] || candidates[idx] != nil || cur.Pool >= maxPools || cur.State == "unresolved" || cur.State == "candidate" || cur.State == "response_pending" || time.Now().Before(cur.NextAttemptAt) {
						continue
					}
					if pool < 0 || cur.Pool < pool {
						pool = cur.Pool
						attempt = cur.Attempt
					}
				}
				if pool < 0 {
					break
				}
				// Pools are resource-local barriers, including drafts and callbacks.
				blocked := false
				for idx := range remaining {
					if doc.Segments[idx].RoundWork.Cursor.Pool < pool && doc.Segments[idx].RoundWork.Cursor.State != "unresolved" {
						blocked = true
						break
					}
				}
				if blocked {
					break
				}
				var built [][]int
				var e error
				if waitingBatch == nil {
					e = runtime.Run(runCtx, func() error {
						b, err := backend.NewBuildBounds(runtime.Window.limits.ItemBytes, runtime.handoffLimit())
						if err != nil {
							return err
						}
						visit := func(yield func(int) bool) {
							for idx := range remaining {
								cur := doc.Segments[idx].RoundWork.Cursor
								if !active[idx] && candidates[idx] == nil && cur.Pool == pool && cur.Attempt == attempt && cur.State != "unresolved" && cur.State != "candidate" && cur.State != "response_pending" && !time.Now().Before(cur.NextAttemptAt) {
									if !yield(idx) {
										return
									}
								}
							}
						}
						batch, err := PlanNextMainBatchBounded(b, h, doc, visit, pool, runtime.Window.limits.Segments)
						if err != nil {
							return err
						}
						if len(batch) > 0 {
							built = [][]int{batch}
						}
						return nil
					})
				}
				if waitingBatch != nil {
					pool, attempt = waitingBatch.pool, waitingBatch.attempt
					built = [][]int{waitingBatch.indices}
				}
				if e != nil {
					firstErr = e
					cancel()
					break
				}
				if len(built) == 0 {
					firstErr = fmt.Errorf("unfinished manifest has no executable batch at pool %d", pool)
					cancel()
					break
				}
				idxs := built[0]
				// Proven fixed recovery inventory can leave no member slot at all.
				// Decide this before a preparation ticket: aggregate recovery stock
				// may itself prevent that ticket from ever reaching the window probe.
				if runtime.Window.fixedBatchLimit(consumer) == 0 {
					firstErr = ErrCandidateCapacity
					cancel()
					break
				}
				// Only an empty entry map can produce aggregateOnly; there are no
				// replacement keys to subtract in that case.
				probe, wake, aggregateOnly := runtime.Window.probePrepareOwned(len(idxs), storeResourceID(store), consumer, nil)
				if probe == WindowBusy && aggregateOnly && mainActive+candidateActive+promotionActive == 0 && !pageActive && !responsePageActive && len(candidates) == 0 && len(responses) == 0 {
					// The selected manifest really needs production, but this scope
					// has no restored work to drain the unclaimed aggregate stock.
					// Give queued resources a chance before a root preparation ticket
					// hides this member-capacity decision behind its own overage.
					waitingBatch = &waitingWindowBatch{indices: append([]int(nil), idxs...), pool: pool, attempt: attempt, wake: wake}
					capacityWaits = append(capacityWaits, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "candidate_window", CapacityChanged: wake}})
					select {
					case <-wake:
					default:
						yielded = true
					}
					break
				}
				ticket, e := runtime.Preparation.Try(runCtx, prepareIntent(round.Handler, store, backend.RequestStageMain), runtime.Gate)
				if d := workDeferred(e); d != nil {
					capacityWaits = append(capacityWaits, d)
					break
				}
				if e != nil {
					if !errors.Is(e, backend.ErrDispatchPaused) {
						firstErr = e
						cancel()
					}
					break
				}
				reservation := NewWorkID()
				result, wake := runtime.Window.TryPrepareOwned(reservation, len(idxs), storeResourceID(store), consumer, memberReservationKeys(store, h.ModeName(), idxs))
				for result == WindowPinnedLimit {
					limit := runtime.Window.fixedBatchLimit(consumer)
					if limit < 1 {
						result = WindowTooLarge
						break
					}
					// Retain the already selected public prefix and its attempt.
					// Only proven fixed inventory changes this ceiling; ordinary
					// in-flight ownership below it is still a Busy wait.
					idxs = idxs[:min(len(idxs), limit)]
					result, wake = runtime.Window.TryPrepareOwned(reservation, len(idxs), storeResourceID(store), consumer, memberReservationKeys(store, h.ModeName(), idxs))
				}
				if result == WindowTooLarge {
					ticket.Release()
					firstErr = ErrCandidateCapacity
					cancel()
					break
				}
				if result == WindowBusy {
					ticket.Release()
					// Ignore the grant/release notification generated by this failed
					// window probe; actual later preparation changes still wake us.
					preparationChanged = runtime.Preparation.Changed()
					waitingBatch = &waitingWindowBatch{indices: append([]int(nil), idxs...), pool: pool, attempt: attempt, wake: wake}
					windowWake = wake
					capacityWaits = append(capacityWaits, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "candidate_window", CapacityChanged: wake}})
					if mainActive+candidateActive+promotionActive == 0 && !pageActive && len(candidates) == 0 && !runtime.Window.CanDrainFor(consumer) {
						// A consumer may have released/left after the Busy probe. Its
						// locked wake proves occupancy changed; retry the retained
						// batch before yielding a now-runnable sole resource.
						select {
						case <-wake:
						default:
							yielded = true
						}
					}
					break
				}
				waitingBatch = nil
				var input *preparedMainDocument
				snapshotErr := runtime.Run(runCtx, func() error { var err error; input, err = snapshotMainInput(doc, idxs, h, runtime); return err })
				if snapshotErr != nil {
					runtime.Window.Release(reservation)
					ticket.Release()
					firstErr = snapshotErr
					cancel()
					break
				}
				for _, idx := range idxs {
					active[idx] = true
				}
				mainActive++
				wg.Add(1)
				go func(idxs []int, pool, attempt int, input *preparedMainDocument, key string, ticket *PreparationTicket) {
					defer wg.Done()
					e := prepareCapturedMain(withPreparationTicket(withCoordinatorPreparation(runCtx), ticket), round, h, doc, input, idxs, pool, attempt, key, store, runtime, logger, reporter)
					if ticket.Active() && e.err == nil {
						ticket.Release()
					}
					events <- e
				}(idxs, pool, attempt, input, reservation, ticket)
			}
		}
		if mainActive+candidateActive+promotionActive == 0 && !pageActive && !responsePageActive {
			if firstErr != nil || runCtx.Err() != nil || paused() || yielded {
				break
			}
			if recoveryDone && responseRecoveryDone && waitingBatch != nil && len(candidates) == 0 && !runtime.Window.CanDrainFor(consumer) {
				select {
				case <-waitingBatch.wake:
				default:
					yielded = true
				}
				if yielded {
					break
				}
			}
			pending := false
			for idx := range remaining {
				if candidates[idx] != nil || (doc.Segments[idx].RoundWork.Cursor.Pool < maxPools && doc.Segments[idx].RoundWork.Cursor.State != "unresolved") {
					pending = true
					break
				}
			}
			if !pending && recoveryDone {
				break
			}
			if pending && recoveryDone && len(candidates) == 0 {
				future := false
				for idx := range remaining {
					if doc.Segments[idx].RoundWork.Cursor.NextAttemptAt.After(time.Now()) {
						future = true
						break
					}
				}
				if !future && len(capacityWaits) == 0 {
					firstErr = fmt.Errorf("unfinished manifest has no candidate, runnable batch, or retry deadline")
					break
				}
			}
		}
		// Stopping a producer yields its fair turn before slow paid writers join.
		// Their member/response/payload ownership remains intact until completion.
		if runCtx.Err() != nil || paused() || yielded || firstErr != nil {
			runtime.Admission.YieldMainPriority(storeResourceID(store), prepareIntent(round.Handler, store, backend.RequestStageMain).RoundIndex)
		}
		// Only real deadlines use a timer; capacity changes wake immediately.
		deadline := batchDeadline
		var policyChanged <-chan struct{}
		for _, d := range capacityWaits {
			w := d.Wait
			if policyChanged == nil {
				policyChanged = w.PolicyChanged
			}
			if !w.AvailableAt.IsZero() && (deadline.IsZero() || w.AvailableAt.Before(deadline)) {
				deadline = w.AvailableAt
			}
		}
		for idx := range remaining {
			if active[idx] {
				continue
			}
			at := doc.Segments[idx].RoundWork.Cursor.NextAttemptAt
			if c := candidates[idx]; c != nil {
				at = c.NextAttemptAt
			}
			if at.After(time.Now()) && (deadline.IsZero() || at.Before(deadline)) {
				deadline = at
			}
		}
		var timer *time.Timer
		var timerC <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(max(time.Until(deadline), time.Nanosecond))
			timerC = timer.C
		}
		changed := windowChanged
		if runCtx.Err() != nil || paused() {
			apply(<-events)
		} else {
			select {
			case e := <-events:
				apply(e)
			case <-changed:
			case <-windowWake:
			case <-preparationChanged:
			case <-admissionChanged:
			case <-policyChanged:
			case <-globalPolicyChanged:
			case <-timerC:
			case <-runtime.Gate.Done():
			case <-runCtx.Done():
			}
		}
		if timer != nil {
			timer.Stop()
		}
	}
	wg.Wait()
	var result RunRoundResult
	for idx := range completed {
		result.Resolved = append(result.Resolved, idx)
	}
	for idx := range remaining {
		result.Unresolved = append(result.Unresolved, idx)
	}
	sort.Ints(result.Resolved)
	sort.Ints(result.Unresolved)
	if firstErr != nil {
		// A caller cancellation can follow the internally observed closure
		// while its storage worker is still joining. Do not expose that
		// dispatch control error as the caller's terminal result.
		if firstErr == backend.ErrAdmissionClosed && ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, firstErr
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if yielded {
		return result, ErrResourceYield
	}
	if !paused() {
		if err := h.Finalize(ctx, doc, result.Unresolved); err != nil {
			return result, err
		}
	}
	return result, nil
}

func prepareMain(ctx context.Context, round Round, h stagedHandler, doc *Document, idxs []int, pool, attempt int, reservation string, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporters ...progress.Reporter) (e stagedEvent) {
	e = stagedEvent{main: true, indices: idxs, cursors: map[int]WorkCursor{}}
	defer func() {
		e.inputTokens = atomic.LoadInt64(&doc.InputTokens)
		e.outputTokens = atomic.LoadInt64(&doc.OutputTokens)
	}()
	defer runtime.Window.Release(reservation)
	if attempt >= transientBudgetFor(round.Retry) {
		var transitions []CursorTransition
		for _, idx := range idxs {
			cursor := WorkCursor{Pool: pool + 1, State: "pending"}
			transitions = append(transitions, CursorTransition{Index: idx, Cursor: cursor})
			e.cursors[idx] = cursor
		}
		if err := runtime.SaveNamed(ctx, persistence.Metadata{Operation: "pool_advance", Class: persistence.Reliable, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error { return store.Cursors(saveCtx, transitions) }); err != nil {
			e.err = err
			return e
		}
		return e
	}
	_, _, _, _, roundIndex := alignmentSettings(round.Handler)
	session := &requestSession{runtime: runtime, store: store, intent: RequestIntent{Stage: backend.RequestStageMain, Indices: idxs, Pool: pool, Attempt: attempt, Phase: "main", RoundIndex: roundIndex, ResourceID: storeResourceID(store)}}
	if len(reporters) > 0 {
		session.reporter = reporters[0]
	}
	defer func() {
		if e.err != nil {
			session.fail(e.err)
		}
		if err := session.finish(); err != nil && (e.err == nil || storageFailure(err)) {
			e.err = err
		}
	}()
	ctx = session.Context(ctx)
	result := h.PrepareBatch(ctx, doc, idxs, attempt, logger)
	if result.waitCapacity != nil {
		e.waitCapacity = result.waitCapacity
		return e
	}
	if session.err != nil {
		e.err = session.err
		return e
	}
	if ctx.Err() != nil {
		e.err = ctx.Err()
		return e
	}
	if result.err != nil {
		e.err = result.err
		return e
	}
	for _, c := range result.candidates {
		if identity, ok := store.(interface{ WorkIdentity(int) string }); ok {
			c.WorkID = identity.WorkIdentity(c.Index)
		}
		c.PoolIndex = pool
		c.MainAttempt = attempt + 1
		c.ParentRequestID = session.lastID
		data, err := c.Encode()
		if err != nil {
			e.err = err
			return e
		}
		if int64(len(data)) > runtime.Window.limits.ItemBytes {
			e.err = ErrCandidateCapacity
			return e
		}
		if err = runtime.SaveNamed(ctx, candidateSaveMetadata("candidate", store, c), func(saveCtx context.Context) error { return store.Save(saveCtx, c) }); err != nil {
			e.err = err
			return e
		}
		if err = runtime.Window.Transfer(reservation, c.ID, int64(len(data))); err != nil {
			e.err = err
			return e
		}
		e.candidates = append(e.candidates, c)
	}
	accepted := map[int]bool{}
	for _, c := range e.candidates {
		accepted[c.Index] = true
		e.cursors[c.Index] = WorkCursor{Pool: pool, Attempt: attempt + 1, State: "candidate"}
	}
	var transitions []CursorTransition
	for _, idx := range idxs {
		if accepted[idx] {
			continue
		}
		cursor := WorkCursor{Pool: pool + 1, State: "pending"}
		if result.retry != nil && attempt+1 < transientBudgetFor(round.Retry) {
			cursor.Pool = pool
			cursor.Attempt = attempt + 1
			cursor.NextAttemptAt = result.retry.notBefore
		}
		if len(result.fatalUnresolved) > 0 {
			cursor.Pool = max(1, round.Retry.MaxAttempts+1)
			cursor.State = "unresolved"
		}
		if runtime.Gate != nil && runtime.Gate.Paused() {
			cursor.Pool = pool
			cursor.Attempt = attempt
			if session.calls > 0 {
				cursor.Attempt++
			}
		}
		if cursor.Attempt >= transientBudgetFor(round.Retry) {
			cursor.Pool++
			cursor.Attempt = 0
		}
		transitions = append(transitions, CursorTransition{Index: idx, Cursor: cursor})
		e.cursors[idx] = cursor
	}
	if len(transitions) > 0 {
		if err := runtime.Save(ctx, func(saveCtx context.Context) error { return store.Cursors(saveCtx, transitions) }); err != nil {
			e.err = err
			return e
		}
	}
	return e
}

func processCandidate(ctx context.Context, round Round, c *Candidate, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporter progress.Reporter) (e stagedEvent) {
	e = stagedEvent{indices: []int{c.Index}, candidate: c}
	defer progress.NotifyWorkState(reporter)
	if validator, ok := store.(interface {
		ValidateCandidate(context.Context, *Candidate) (bool, error)
	}); ok {
		valid, err := validator.ValidateCandidate(ctx, c)
		if err != nil {
			e.err = err
			return e
		}
		if !valid {
			return retireCandidate(ctx, round, c, store, runtime, true)
		}
	}
	if !c.Ready {
		worker := &AlignmentWorker{store: store, runtime: runtime, reporter: reporter}
		defer func() {
			if err := worker.finish(e.err); err != nil && (e.err == nil || storageFailure(err)) {
				e.err = err
			}
		}()
		update := worker.Run(ctx, round, c)
		if update.waitCapacity != nil {
			e.waitCapacity = update.waitCapacity
			return e
		}
		if worker.session != nil && !worker.session.handoffDeadline.IsZero() {
			ctx = persistence.WithDeadline(ctx, worker.session.handoffDeadline)
		}
		e.inputTokens, e.outputTokens = update.inputTokens, update.outputTokens
		if update.err != nil || update.paused {
			e.err = update.err
			return e
		}
		if update.stale[c.Index] {
			retired := retireCandidate(ctx, round, c, store, runtime, true)
			retired.inputTokens, retired.outputTokens = e.inputTokens, e.outputTokens
			return retired
		}
	}
	if !c.Ready {
		return e
	}
	var result TranslatedSegment
	var accepted bool
	frame, loadErr := loadCandidateFrame(ctx, store, c.ID, runtime.Window.limits.ItemBytes)
	if loadErr == nil {
		runtime.Admission.ObserveOwned("candidate", c.ID+"/frame", int64(cap(frame.Payload)))
		defer runtime.Admission.ObserveOwned("candidate", c.ID+"/frame", 0)
	}
	if loadErr != nil {
		e.err = loadErr
		return e
	}
	err := runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBounds(runtime.Window.limits.ItemBytes, int64(len(frame.Payload)))
		if err != nil {
			return err
		}
		full, decodeErr := DecodeCandidateFrame(b, frame)
		if decodeErr != nil {
			return decodeErr
		}
		result, accepted, err = FinalizeCandidateBounded(b, full)
		if err != nil {
			return fmt.Errorf("finalize candidate %s: %w", c.ID, err)
		}
		if accepted {
			if prep, ok := store.(CandidateCommitPreparer); ok {
				result, err = prep.PrepareCandidateCommit(ctx, b, full, result)
				if err != nil {
					return err
				}
			}
			result, err = compactCommitResult(b, result, runtime.Window.limits.ItemBytes, func(size int64) error { return runtime.Admission.ReserveFinalResultCredit(c.ID, size) })
			if err != nil {
				return err
			}
			if err = runtime.Admission.ApplyFinalResultCredit(c.ID, int64(len(result.PreparedPayload))); err != nil {
				return err
			}
		}
		// Finalization is the last consumer of the decoded private candidate.
		full.PreparedPayload = nil
		full.PreparedHeader = nil
		full.BaselineDigest = c.BaselineDigest
		*c = *HeaderForCandidate(full).Candidate()
		return err
	})
	frame = CandidateFrame{}
	runtime.Admission.ObserveOwned("candidate", c.ID+"/frame", 0)
	if err != nil {
		e.err = err
		return e
	}
	if !accepted {
		retired := retireCandidate(ctx, round, c, store, runtime, false)
		retired.inputTokens, retired.outputTokens = e.inputTokens, e.outputTokens
		return retired
	}
	var outcome CommitOutcome
	err = runtime.SaveNamed(ctx, candidateSaveMetadata("commit", store, c), func(saveCtx context.Context) error {
		var commitErr error
		outcome, commitErr = store.Commit(saveCtx, c, result)
		return commitErr
	})
	e.outcome = outcome
	e.err = err
	if err != nil {
		e.outcome = ""
		return e
	}
	if outcome == CommitAccepted || outcome == CommitNoop || outcome == CommitExisting {
		result.TargetText = c.Segment.Target
		e.completed = &result
	}
	return e
}

// processCandidates retains one request completion reservation until all of its
// independent candidate saves and confirmations have finished. A storage failure
// leaves previously saved members recoverable without replaying their request.
func processCandidates(ctx context.Context, round Round, candidates []*Candidate, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporter progress.Reporter) (e stagedEvent) {
	defer progress.NotifyWorkState(reporter)
	for _, c := range candidates {
		e.indices = append(e.indices, c.Index)
	}
	var live []*Candidate
	for _, c := range candidates {
		if validator, ok := store.(interface {
			ValidateCandidate(context.Context, *Candidate) (bool, error)
		}); ok {
			valid, err := validator.ValidateCandidate(ctx, c)
			if err != nil {
				e.err = err
				return e
			}
			if !valid {
				member := retireCandidate(ctx, round, c, store, runtime, true)
				e.members = append(e.members, member)
				if member.err != nil {
					e.err = member.err
					return e
				}
				continue
			}
		}
		live = append(live, c)
	}
	if len(live) == 0 {
		return e
	}
	worker := &AlignmentWorker{store: store, runtime: runtime, reporter: reporter}
	defer func() {
		if err := worker.finish(e.err); err != nil && (e.err == nil || storageFailure(err)) {
			e.err = err
		}
	}()
	update := worker.RunBatch(ctx, round, live)
	if update.waitCapacity != nil {
		e.waitCapacity = update.waitCapacity
		return e
	}
	if worker.session != nil && !worker.session.handoffDeadline.IsZero() {
		ctx = persistence.WithDeadline(ctx, worker.session.handoffDeadline)
	}
	e.inputTokens, e.outputTokens = update.inputTokens, update.outputTokens
	if update.err != nil || update.paused {
		e.err = update.err
		return e
	}
	for _, c := range live {
		member := stagedEvent{candidate: c}
		if update.stale[c.Index] {
			member = retireCandidate(ctx, round, c, store, runtime, true)
		} else if c.Ready {
			member = processCandidate(ctx, round, c, store, runtime, logger, reporter)
		}
		e.members = append(e.members, member)
		if member.err != nil {
			e.err = member.err
			return e
		}
	}
	return e
}

func retireCandidate(ctx context.Context, round Round, c *Candidate, store RoundStore, runtime *ExecutionRuntime, stale bool) stagedEvent {
	cursor := WorkCursor{Pool: c.PoolIndex + 1, State: "pending"}
	if stale || cursor.Pool >= max(1, round.Retry.MaxAttempts+1) {
		cursor.Pool = max(1, round.Retry.MaxAttempts+1)
		cursor.State = "unresolved"
	}
	e := stagedEvent{indices: []int{c.Index}, candidate: c, cursors: map[int]WorkCursor{c.Index: cursor}, outcome: CommitRejected}
	if stale {
		e.outcome = CommitStale
	}
	e.err = runtime.SaveNamed(ctx, candidateSaveMetadata("retire", store, c), func(saveCtx context.Context) error {
		if stale {
			if retirer, ok := store.(interface {
				RetireStale(context.Context, *Candidate, WorkCursor) error
			}); ok {
				return retirer.RetireStale(saveCtx, c, cursor)
			}
		}
		return store.Retire(saveCtx, c, cursor)
	})
	if e.err != nil {
		e.outcome = ""
		e.cursors = nil
	}
	return e
}

func storeResourceID(store RoundStore) int {
	if s, ok := store.(interface{ ResourceIdentity() int }); ok {
		return s.ResourceIdentity()
	}
	return 0
}

func storeRoundID(store RoundStore) int {
	if s, ok := store.(interface{ RoundIdentity() int }); ok {
		return s.RoundIdentity()
	}
	return 0
}

func candidateSaveMetadata(operation string, store RoundStore, c *Candidate) persistence.Metadata {
	requestID := c.LastAlignmentRequestID
	if requestID == "" {
		requestID = c.ParentRequestID
	}
	return persistence.Metadata{Operation: operation, Class: persistence.Reliable, ResourceID: storeResourceID(store), RequestID: requestID, CandidateID: c.ID}
}
