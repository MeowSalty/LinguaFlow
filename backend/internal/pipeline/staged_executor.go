package pipeline

import (
	"context"
	"errors"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type stagedHandler interface {
	RoundHandler
	stagedRound()
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
	mainPlan              *deferredMainPlan
	confirmation          *preparedConfirmation
}

// These counters belong to the coordinator. Workers remain in flight until
// their terminal event has been applied, including paid saves and recovery pages.
type stagedInFlight struct {
	main, candidates, promotions int
	candidatePage, responsePage  bool
}

func (s stagedInFlight) pending() bool {
	return s.main+s.candidates+s.promotions != 0 || s.candidatePage || s.responsePage
}

// Once production stops, wait only for workers that can still post an event.
// Cancellation can arrive after the coordinator's earlier idle check.
func (s stagedInFlight) drain(events <-chan stagedEvent) (stagedEvent, bool) {
	if !s.pending() {
		return stagedEvent{}, false
	}
	return <-events, true
}

// ErrResourceYield suspends an unfinished resource after all its callbacks have
// joined. Its persisted cursors remain authoritative on re-admission.
var ErrResourceYield = errors.New("resource yields to recovered candidates")

// RunStagedRound keeps the pool barrier until every accepted candidate has a
// durable destination. Main workers finish at reliable handoff, not alignment.
// Only this goroutine applies confirmed results to the resource Document.
func RunStagedRound(ctx context.Context, round Round, doc *Document, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporter progress.Reporter) (RunRoundResult, error) {
	h, ok := round.Handler.(stagedHandler)
	if !ok {
		return RunRoundResult{}, fmt.Errorf("unsupported staged round handler %T", round.Handler)
	}
	if runtime == nil {
		return RunRoundResult{}, errors.New("staged round requires an execution runtime")
	}
	ctx = WithExecutionRuntime(ctx, runtime)
	ctx = context.WithValue(ctx, confirmationDocumentKey{}, doc)
	if err := runtime.RetryConfirmedDocument(ctx, doc); err != nil {
		return RunRoundResult{}, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if reporter == nil {
		reporter = progress.Nop{}
	}
	if store == nil {
		memory := NewMemoryRoundStore(nil)
		defer memory.Close()
		store = memory
	}
	if handler, ok := h.(*TranslateHandler); ok && handler.InlineBootstrap && handler.Glossary != nil {
		processor, ok := handler.Glossary.(glossary.ResponseProcessor)
		if !ok {
			return RunRoundResult{}, responsecontract.ErrPublicationDomain
		}
		domain, err := processor.ResponsePublicationDomain(store.PublicationDomain())
		if err != nil {
			return RunRoundResult{}, err
		}
		if err = responsecontract.ValidatePublication(store.PublicationDomain(), domain); err != nil {
			return RunRoundResult{}, err
		}
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
	ctx, closeCanonicalIndex := withCanonicalIndexLifetime(ctx)
	defer closeCanonicalIndex()
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
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_prepare", Operation: "doc_member_visit", Rows: 1})
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
	if checked, ok := reporter.(interface{ StageError() error }); ok {
		if err := checked.StageError(); err != nil {
			return RunRoundResult{}, err
		}
	}
	defer reporter.StageDone()
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
	var scheduler *mainScheduler
	err = runtime.Run(ctx, func() error {
		var buildErr error
		scheduler, buildErr = newMainScheduler(ctx, doc, h, runtime, maxPools, "scheduler:"+NewWorkID())
		return buildErr
	})
	if err != nil {
		if workDeferred(err) != nil {
			runtime.Admission.YieldMainPriority(storeResourceID(store), prepareIntent(round.Handler, store, backend.RequestStageMain).RoundIndex)
		}
		return RunRoundResult{}, err
	}
	defer scheduler.close()
	poolProgress := stagedPoolProgress{pool: -1, maxPools: maxPools, shrink: round.Shrink}
	if poolProgress.shrink == 0 {
		poolProgress.shrink = 1
	}
	var batchProgress stagedBatchProgress
	// Final result collection is one cold document pass per result set. Include
	// its real visits so diagnostics never hide the retained scalar inventory.
	remaining = func(yield func(int) bool) {
		for i := range doc.Segments {
			scheduler.observe("doc_member_visit", 0, 1)
			w := doc.Segments[i].RoundWork
			if w.Member && !w.Completed && !yield(i) {
				return
			}
		}
	}
	completed = func(yield func(int) bool) {
		for i := range doc.Segments {
			scheduler.observe("doc_member_visit", 0, 1)
			w := doc.Segments[i].RoundWork
			if w.Member && w.Completed && !yield(i) {
				return
			}
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan stagedEvent, 32)
	var wg sync.WaitGroup
	active := map[int]bool{}
	mainActive, candidateActive, promotionActive := 0, 0, 0
	var firstErr error
	yielded := false
	var waitingBatch *deferredMainPlan
	defer func() { waitingBatch.close() }()
	paused := func() bool { return runtime.Gate != nil && runtime.Gate.Paused() }
	applyMember := func(e stagedEvent) {
		for _, header := range e.responseHeaders {
			responses[header.ID] = header
			if e.main && e.err == nil {
				batchProgress.start(header)
			}
		}
		for idx, cursor := range e.cursors {
			doc.Segments[idx].RoundWork.Cursor = cursor
		}
		if e.responseAdvance != nil {
			header := *e.responseAdvance
			previous, tracked := responses[header.ID]
			responseActive[header.ID] = false
			delete(responseBlocked, header.ID)
			if header.Cursor >= header.MemberCount {
				delete(responses, header.ID)
			} else {
				responses[header.ID] = header
			}
			if e.err == nil && tracked && header.Cursor > previous.Cursor {
				if rejected := header.Cursor - previous.Cursor - len(e.candidates); rejected > 0 {
					retry := false
					for _, cursor := range e.cursors {
						retry = retry || (cursor.State == "pending" && cursor.Pool == header.Pool)
					}
					batchProgress.finish(reporter, header.ID, rejected, retry)
				}
				if header.Cursor == header.MemberCount {
					progress.NotifyWorkState(reporter)
				}
			}
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
			if observe, _ := ctx.Value(alignmentReadyObserverKey{}).(func(time.Time)); observe != nil {
				observe(alignmentReadyAt[c.Index])
			}
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "alignment", Operation: "ready", Rows: 1})
		}
		if e.candidate != nil {
			c := e.candidate
			candidates[c.Index] = HeaderForCandidate(c).Candidate()
			alignmentReadyAt[c.Index] = maxTime(time.Now(), c.NextAttemptAt)
			if e.outcome == CommitAccepted || e.outcome == CommitExisting || e.outcome == CommitNoop {
				if e.completed != nil {
					p := e.confirmation
					if p == nil {
						firstErr = errors.New("confirmed result has no prepared handoff")
						cancel()
						return
					}
					p.candidate, p.doc, p.runtime = c, doc, runtime
					if memory, ok := store.(interface{ MemoryStore() *MemoryRoundStore }); ok {
						m := memory.MemoryStore()
						p.memoryStore = m
						m.mu.Lock()
						callback := m.apply
						// Commit ended the store's candidate payload lifetime. The
						// confirmation now owns this runtime's final backing, even
						// if its later move to a pending owner is rejected.
						if owners := m.candidateOwners[c.ID]; owners != nil {
							owners[memoryCandidateOwner{runtime.Admission, runtime.Window}] = false
						}
						m.mu.Unlock()
						if callback != nil {
							p.apply = func(callCtx context.Context, _ *Candidate, result TranslatedSegment) (bool, error) {
								return true, callback(callCtx, BatchResult{Segments: []TranslatedSegment{result}})
							}
						}
					} else if local, ok := store.(interface {
						ApplyConfirmed(context.Context, *Candidate, TranslatedSegment) (bool, error)
					}); ok {
						p.apply = local.ApplyConfirmed
					}
					p.result.TargetText = c.Segment.Target
					if err := p.project(ctx); err != nil {
						if doc.Segments[c.Index].RoundWork.Completed {
							runtime.Window.Release(c.ID)
							_ = p.release()
						} else {
							err = errors.Join(err, runtime.rememberConfirmation(p))
						}
						if firstErr == nil {
							firstErr = &ConfirmationProjectionError{Index: c.Index, Cause: err}
							cancel()
						}
						return
					}
					*e.completed = TranslatedSegment{}
					if err := p.release(); err != nil && firstErr == nil {
						firstErr = err
						cancel()
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
				batchProgress.finish(reporter, c.ParentRequestID, 1, false)
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
				batchProgress.finish(reporter, c.ParentRequestID, 1, false)
			}
		}
		if e.main && e.err == nil && len(e.responseHeaders) == 0 && e.mainPlan != nil {
			finished := false
			for _, cursor := range e.cursors {
				finished = finished || cursor.Pool > e.mainPlan.pool || cursor.State == "unresolved"
			}
			if finished {
				reporter.BatchComplete()
				progress.NotifyWorkState(reporter)
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
		if e.err != nil && (firstErr == nil || storageFailure(e.err) && ctx.Err() == nil && (errors.Is(firstErr, backend.ErrAdmissionClosed) || errors.Is(firstErr, context.Canceled))) {
			firstErr = e.err
			cancel()
		}
	}
	apply := func(e stagedEvent) {
		// Workers return only confirmed facts. Refresh touched nodes after the
		// entire event has been applied, including its active-to-idle transition.
		defer func() {
			now := time.Now()
			// A returning main wait may restore only its own active members.
			// Other ready changes while it was dispatched must invalidate the
			// selected public batch instead of laundering its old version.
			resumePlan := e.mainPlan != nil && e.main && e.waitCapacity != nil && len(e.waitIndices) > 0 && len(e.cursors) == 0 && len(e.candidates) == 0 && e.candidate == nil && len(e.members) == 0 && e.mainPlan.canRestoreWait(scheduler)
			refresh := func(member stagedEvent) {
				for _, idx := range member.indices {
					scheduler.refresh(idx, active[idx], candidates[idx], now)
				}
				for idx := range member.cursors {
					scheduler.refresh(idx, active[idx], candidates[idx], now)
				}
				for _, c := range member.candidates {
					scheduler.refresh(c.Index, active[c.Index], candidates[c.Index], now)
				}
				if member.candidate != nil {
					idx := member.candidate.Index
					scheduler.refresh(idx, active[idx], candidates[idx], now)
				}
			}
			refresh(e)
			for _, member := range e.members {
				refresh(member)
			}
			if e.mainPlan != nil {
				if e.waitCapacity != nil && e.main && len(e.waitIndices) > 0 {
					waitingBatch.close()
					waitingBatch = e.mainPlan
					waitingBatch.deferFor(e.waitCapacity.Wait, runtime.Admission.PolicyChanged())
					if g := scheduler.selectedGroup(); resumePlan && g >= 0 && waitingBatch.matches(scheduler.groups[g]) {
						waitingBatch.version = scheduler.groups[g].version
					}
				} else {
					e.mainPlan.close()
				}
			}
		}()
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
		scheduler.expire(time.Now(), active, candidates)
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
							err = runtime.Admission.RestoreResponseOwnership(header.ID, i.RoundIndex, storeResourceID(store), header.Facts(), header.Phase, false)
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
			if recoveryDone && len(responses) > 0 {
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
					if !responseActive[id] && (businessOwner == "" || businessOwner == id || header.Phase == "business_ready" || runtime.Window.TransferAllowed(businessOwner, storeResourceID(store), prepareIntent(round.Handler, store, backend.RequestStageMain).RoundIndex)) {
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
						advance, err := AdvanceResponse(runCtx, round, h, header, store, runtime, logger)
						e := advance.event
						e.err = err
						e.responseAdvance = &advance.Header
						events <- e
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
				mayProduce = scheduler.tree[1].mainPending > 0
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
				if waitingBatch != nil && !waitingBatch.valid(scheduler) {
					scheduler.observe("plan_invalidate", 0, 1)
					waitingBatch.close()
					waitingBatch = nil
				}
				if waitingBatch != nil && (waitingBatch.wait.Reason == "prepare_capacity" || waitingBatch.wait.Reason == "main_concurrency") {
					// Unrelated freeze/read byte claims also signal Admission.Changed.
					// They do not free a queued preparation group or a full main C.
					// Check the actual blocking predicate before entering Preparation.
					hint, hintErr := runtime.Admission.ReadyHint(runCtx, prepareIntent(round.Handler, store, backend.RequestStageMain), runtime.Gate)
					if hintErr != nil && !errors.Is(hintErr, backend.ErrDispatchPaused) {
						firstErr = hintErr
						cancel()
						break
					}
					wait := waitingBatch.wait
					blocked := wait.Reason == "main_concurrency" && hint.Reason == "main_concurrency"
					if wait.Reason == "prepare_capacity" {
						blocked = !mainWaitSignalReady(wait.CapacityChanged) && !mainWaitSignalReady(wait.PolicyChanged) && (wait.AvailableAt.IsZero() || time.Now().Before(wait.AvailableAt))
					}
					if blocked {
						if wait.Reason == "main_concurrency" {
							waitingBatch.deferFor(hint, globalPolicyChanged)
							wait = waitingBatch.wait
						}
						scheduler.observe("wait_predicate/"+wait.Reason, 0, 1)
						capacityWaits = append(capacityWaits, &WorkDeferred{Wait: wait})
						break
					}
				}
				if waitingBatch != nil && waitingBatch.wait.Reason == "candidate_window" {
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
					case <-waitingBatch.wait.CapacityChanged:
						ready = true
					default:
					}
					if !ready {
						windowWake = waitingBatch.wait.CapacityChanged
						capacityWaits = append(capacityWaits, &WorkDeferred{Wait: waitingBatch.wait})
						break
					}
				}
				g := scheduler.selectedGroup()
				if g < 0 {
					break
				}
				pool, attempt := scheduler.groups[g].pool, scheduler.groups[g].attempt
				// A whole-batch parse retry may become pending one member at a
				// time. Re-admit it after the parent releases all its members.
				if _, promoting := responses[scheduler.groups[g].retry.parent()]; promoting {
					break
				}
				var e error
				if waitingBatch == nil {
					e = runtime.Run(runCtx, func() error { var err error; waitingBatch, err = scheduler.buildPlan(runCtx, g, store); return err })
				} else {
					scheduler.observe("plan_reuse", 0, 1)
				}
				if d := workDeferred(e); d != nil {
					capacityWaits = append(capacityWaits, d)
					break
				}
				if e != nil {
					firstErr = e
					cancel()
					break
				}
				poolProgress.start(scheduler, g, reporter, logger)
				idxs := waitingBatch.indices
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
					waitingBatch.deferFor(backend.AdmissionWait{Reason: "candidate_window", CapacityChanged: wake}, globalPolicyChanged)
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
					waitingBatch.deferFor(d.Wait, globalPolicyChanged)
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
				result, wake := runtime.Window.TryPrepareOwned(reservation, len(idxs), storeResourceID(store), consumer, waitingBatch.keys[:len(idxs)])
				for result == WindowPinnedLimit {
					limit := runtime.Window.fixedBatchLimit(consumer)
					if limit < 1 {
						result = WindowTooLarge
						break
					}
					// Only authoritative fixed recovery stock can shorten this new
					// prefix. Ordinary in-flight ownership retains its Busy wait.
					idxs = idxs[:min(len(idxs), limit)]
					result, wake = runtime.Window.TryPrepareOwned(reservation, len(idxs), storeResourceID(store), consumer, waitingBatch.keys[:len(idxs)])
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
					waitingBatch.indices = idxs
					waitingBatch.deferFor(backend.AdmissionWait{Reason: "candidate_window", CapacityChanged: wake}, globalPolicyChanged)
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
				waitingBatch.indices = idxs
				var input *preparedMainDocument
				snapshotCtx := runtime.BuildContext(runCtx, "build:"+reservation)
				snapshotErr := runtime.Run(snapshotCtx, func() error {
					var err error
					input, err = snapshotMainInput(snapshotCtx, doc, idxs, h, runtime)
					return err
				})
				if snapshotErr != nil {
					input = nil
					_ = runtime.Admission.ReleaseBuild("build:" + reservation)
					runtime.Window.Release(reservation)
					ticket.Release()
					if d := workDeferred(snapshotErr); d != nil {
						waitingBatch.deferFor(d.Wait, globalPolicyChanged)
						capacityWaits = append(capacityWaits, d)
						break
					}
					firstErr = snapshotErr
					cancel()
					break
				}
				for _, idx := range idxs {
					active[idx] = true
					scheduler.refresh(idx, true, candidates[idx], time.Now())
				}
				waitingBatch.captureDispatch(scheduler, g)
				dispatchedPlan := waitingBatch
				waitingBatch = nil
				mainActive++
				wg.Add(1)
				go func(idxs []int, pool, attempt int, input *preparedMainDocument, key string, ticket *PreparationTicket, plan *deferredMainPlan) {
					defer wg.Done()
					e := prepareCapturedMain(withPreparationTicket(withCoordinatorPreparation(runCtx), ticket), round, h, doc, input, idxs, pool, attempt, plan.retry, key, store, runtime, logger, reporter)
					e.mainPlan = plan
					if ticket.Active() && e.err == nil {
						ticket.Release()
					}
					events <- e
				}(idxs, pool, attempt, input, reservation, ticket, dispatchedPlan)
			}
		}
		inFlight := stagedInFlight{mainActive, candidateActive, promotionActive, pageActive, responsePageActive}
		if !inFlight.pending() {
			if firstErr != nil || runCtx.Err() != nil || paused() || yielded {
				break
			}
			if recoveryDone && responseRecoveryDone && waitingBatch != nil && waitingBatch.wait.Reason == "candidate_window" && len(candidates) == 0 && !runtime.Window.CanDrainFor(consumer) {
				select {
				case <-waitingBatch.wait.CapacityChanged:
				default:
					yielded = true
				}
				if yielded {
					break
				}
			}
			pending := scheduler.tree[1].pending > 0
			if !pending && recoveryDone {
				break
			}
			if pending && recoveryDone && len(candidates) == 0 {
				future := len(scheduler.heap) > 0
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
			scheduler.observe("wait/"+w.Reason, 0, 1)
			if policyChanged == nil {
				policyChanged = w.PolicyChanged
			}
			if !w.AvailableAt.IsZero() && (deadline.IsZero() || w.AvailableAt.Before(deadline)) {
				deadline = w.AvailableAt
			}
		}
		if at := scheduler.deadline(); !at.IsZero() && (deadline.IsZero() || at.Before(deadline)) {
			deadline = at
		}
		var timer *time.Timer
		var timerC <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(max(time.Until(deadline), time.Nanosecond))
			timerC = timer.C
		}
		changed := windowChanged
		if runCtx.Err() != nil || paused() {
			e, received := inFlight.drain(events)
			if !received {
				if timer != nil {
					timer.Stop()
				}
				break
			}
			apply(e)
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
		phase := doc.Segments[idx].RoundWork.Cursor.Phase
		if phase == "terminal_failure_batch" {
			result.FailedBatches++
		}
		if phase == "terminal_failure_batch" || phase == "terminal_failure" {
			result.FailedSegments = append(result.FailedSegments, idx)
		} else {
			result.Unresolved = append(result.Unresolved, idx)
		}
	}
	sort.Ints(result.Resolved)
	sort.Ints(result.Unresolved)
	if firstErr != nil {
		// A caller cancellation can follow the internally observed closure
		// while its storage worker is still joining. Do not expose that
		// dispatch control error as the caller's terminal result.
		if errors.Is(firstErr, backend.ErrAdmissionClosed) && ctx.Err() != nil {
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

func processCandidate(ctx context.Context, round Round, c *Candidate, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger, reporter progress.Reporter) (e stagedEvent) {
	parentSession, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	ctx = runtime.BuildContext(ctx, "candidate:"+c.ID)
	e = stagedEvent{indices: []int{c.Index}, candidate: c}
	defer progress.NotifyWorkState(reporter)
	var contextErr error
	ctx, contextErr = parentSession.responseBuildContext(ctx)
	if contextErr != nil {
		e.err = contextErr
		return e
	}
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
		ctx, contextErr = worker.session.responseBuildContext(ctx)
		if contextErr != nil {
			e.err = contextErr
			return e
		}
		e.inputTokens, e.outputTokens = update.inputTokens, update.outputTokens
		if update.err != nil || update.paused {
			e.err = update.err
			return e
		}
		if err := worker.complete(); err != nil {
			e.err = err
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
	finalOwner := "candidate-finalize:" + NewWorkID()
	defer runtime.Admission.ReleaseBuild(finalOwner)
	if parentSession, _ = ctx.Value(requestSessionKey{}).(*requestSession); parentSession == nil || len(parentSession.responseBackings) == 0 {
		ctx = runtime.BuildContext(ctx, finalOwner)
	}
	var result TranslatedSegment
	var accepted bool
	var finalBounds *backend.BuildBounds
	var retainedResult int64
	var confirmation *preparedConfirmation
	defer func() {
		if e.completed == nil {
			if saved := result.memoryConfirmation; saved != nil && saved.stored {
				retainedResult -= saved.transferredBytes
			}
			// An uncertain ACK may have written only these transient decoded
			// fields to the candidate header. Its encoded store record is a
			// separate owner; end every temporary body borrower before refund.
			c.Segment.Target, c.Segment.Issues = "", nil
			if confirmation != nil {
				confirmation.result = TranslatedSegment{}
				confirmation.memoryHeader = nil
			}
			result = TranslatedSegment{}
			if retainedResult > 0 {
				_ = runtime.Admission.ReleaseBuildBytes("candidate:"+c.ID, retainedResult)
			}
		}
		finalBounds.CloseRetired()
	}()
	frame, loadErr := loadCandidateFrame(ctx, store, c.ID, runtime.Window.limits.ItemBytes)
	if loadErr == nil {
		runtime.Admission.ObserveOwned("candidate", c.ID+"/frame", int64(cap(frame.Payload)))
		defer runtime.Admission.ObserveOwned("candidate", c.ID+"/frame", 0)
	}
	if loadErr != nil {
		e.err = loadErr
		return e
	}
	defer frame.Release()
	finalizeStarted := time.Now()
	err := runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, runtime.Window.limits.ItemBytes, int64(len(frame.Payload)))
		if err != nil {
			return err
		}
		finalBounds = b
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
			compact := result
			result, err = restoreCommitResult(b, compact)
			if err != nil {
				return err
			}
			result.PreparedPayload, result.IssuesPayload = compact.PreparedPayload, compact.IssuesPayload
			result.PreparedStatus, result.PreparedNoop = compact.PreparedStatus, compact.PreparedNoop
			result.PreparedClearReview, result.CommitPrepared = compact.PreparedClearReview, compact.CommitPrepared
			result.PreparedAuthorityVersion = compact.PreparedAuthorityVersion
			result.PreparedTargetIsNull = compact.PreparedTargetIsNull
			if memory, ok := store.(interface{ MemoryStore() *MemoryRoundStore }); ok {
				if err := backend.ReserveSlice[memoryConfirmation](b, 1); err != nil {
					return err
				}
				result.memoryConfirmation = &memoryConfirmation{payload: result.PreparedPayload, admission: runtime.Admission, owner: memory.MemoryStore().confirmationOwner(c.ID), candidateID: c.ID, index: c.Index, transferredBytes: int64(cap(result.PreparedPayload)) + int64(unsafe.Sizeof(memoryConfirmation{}))}
			}
			previous := Segment{}
			if document, _ := ctx.Value(confirmationDocumentKey{}).(*Document); document != nil {
				previous = document.Segments[c.Index]
				meta := Segment{Meta: result.Meta}
				if err = restoreMetadataTypes(&meta, metadataTypes(previous.Meta), b); err != nil {
					return err
				}
				result.Meta = meta.Meta
			}
			plan, borrowed, err := prepareConfirmedFields(ctx, b, result, true, previous)
			if err != nil {
				return err
			}
			if err = backend.ReserveSlice[preparedConfirmation](b, 1); err != nil {
				return err
			}
			deadline := persistence.Deadline(ctx)
			if callerDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || callerDeadline.Before(deadline)) {
				deadline = callerDeadline
			}
			if deadline.IsZero() {
				deadline = time.Now().Add(runtime.Policy.HandoffBudget)
			}
			confirmation = &preparedConfirmation{result: result, plan: plan, borrowed: borrowed, owner: "candidate:" + c.ID, callbackOwner: "candidate-callback:" + c.ID, deadline: deadline, lifetime: runtime.Lifetime, observe: backend.MemberWorkRecorder(ctx)}
			if result.memoryConfirmation != nil {
				if err := backend.ReserveSlice[confirmationHeader](b, 1); err != nil {
					return err
				}
				confirmation.memoryHeader = &confirmationHeader{Index: c.Index}
				if err := b.CloneInto(c.ID, &confirmation.memoryHeader.ID); err != nil {
					return err
				}
			}
		}
		// The existing header remains authoritative. Preparation only reads it;
		// Commit applies final status and content version after its durable save.
		full = nil
		if accepted {
			if result.memoryConfirmation != nil {
				retainedResult += int64(unsafe.Sizeof(memoryConfirmation{}))
				retainedResult += int64(unsafe.Sizeof(confirmationHeader{})) + int64(len(confirmation.memoryHeader.ID))
			}
			// IssuesPayload borrows PreparedPayload; PreparedStatus is a literal.
			// Only these original-build strings and buffer escape finalization.
			for _, n := range []int64{int64(len(result.ID)), int64(len(result.SourceText)), confirmation.plan.TargetBytes, confirmation.plan.IssuesBytes, confirmation.plan.MetaBytes, confirmation.plan.ProtectedBytes, int64(cap(result.PreparedPayload)), int64(unsafe.Sizeof(preparedConfirmation{}))} {
				retainedResult, err = backend.CheckedAdd(retainedResult, n)
				if err != nil {
					retainedResult = 0
					return err
				}
			}
			credit, creditErr := b.RetainedAllocation(retainedResult)
			if creditErr != nil {
				creditErr = fmt.Errorf("final_retained requested=%d live=%d: %w", retainedResult, b.Snapshot().LiveBytes, creditErr)
				retainedResult = 0
				return creditErr
			}
			if err = backend.TransferBuildAllocationToBuild(ctx, credit, "candidate:"+c.ID); err != nil {
				credit.Release()
				retainedResult = 0
				return err
			}
		}
		return err
	})
	frame.Release()
	runtime.Admission.ObserveOwned("candidate", c.ID+"/frame", 0)
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "confirmation", Operation: "finalize", DurationNS: time.Since(finalizeStarted).Nanoseconds()})
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
	commitStarted := time.Now()
	err = runtime.SaveNamed(ctx, candidateSaveMetadata("commit", store, c), func(saveCtx context.Context) error {
		var commitErr error
		outcome, commitErr = store.Commit(saveCtx, c, result)
		return commitErr
	})
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "confirmation", Operation: "commit_ack", DurationNS: time.Since(commitStarted).Nanoseconds()})
	e.outcome = outcome
	e.err = err
	if err != nil {
		e.outcome = ""
		return e
	}
	if outcome == CommitAccepted || outcome == CommitNoop || outcome == CommitExisting {
		if confirmation.memoryHeader != nil {
			confirmation.memoryHeader.Status, confirmation.memoryHeader.Version, confirmation.memoryHeader.TargetIsNull = c.Segment.Status, c.Segment.ContentVersion, c.Segment.TargetIsNull
			if document, _ := ctx.Value(confirmationDocumentKey{}).(*Document); document != nil && document.Segments[c.Index].Status == c.Segment.Status {
				// Preserve an existing canonical status string's source ownership.
				confirmation.memoryHeader.Status = document.Segments[c.Index].Status
			}
		}
		// Store ownership ends every borrowed reference to its encoding here.
		result.PreparedPayload, result.IssuesPayload, result.memoryConfirmation = nil, nil, nil
		confirmation.result.PreparedPayload, confirmation.result.IssuesPayload, confirmation.result.memoryConfirmation = nil, nil, nil
		result.TargetText = c.Segment.Target
		e.completed = &result
		e.confirmation = confirmation
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
	var contextErr error
	ctx, contextErr = worker.session.responseBuildContext(ctx)
	if contextErr != nil {
		e.err = contextErr
		return e
	}
	e.inputTokens, e.outputTokens = update.inputTokens, update.outputTokens
	if update.err != nil || update.paused {
		e.err = update.err
		return e
	}
	if err := worker.complete(); err != nil {
		e.err = err
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
