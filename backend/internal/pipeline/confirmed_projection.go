package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type confirmationDocumentKey struct{}

// Only these identity/status scalars survive a temporary Memory store's close.
// The header and its private ID are paid during precommit finalization.
type confirmationHeader struct {
	ID           string
	Index        int
	Status       string
	Version      int64
	TargetIsNull bool
}

// ConfirmationProjectionError means acceptance succeeded, but the caller's
// after-confirm callback or document receiver has not completed its handoff.
// RetryConfirmedDocument owns that retry until CloseDocument or Runtime.Close.
type ConfirmationProjectionError struct {
	Index int
	Cause error
}

func (e *ConfirmationProjectionError) Error() string {
	return fmt.Sprintf("confirmed segment %d projection pending: %v", e.Index, e.Cause)
}
func (e *ConfirmationProjectionError) Unwrap() error { return e.Cause }

type preparedConfirmation struct {
	result                                                          TranslatedSegment
	plan                                                            DocumentAllocationPlan
	borrowed                                                        uint8
	candidate                                                       *Candidate
	doc                                                             *Document
	apply                                                           func(context.Context, *Candidate, TranslatedSegment) (bool, error)
	owner, callbackOwner                                            string
	deadline                                                        time.Time
	lifetime                                                        context.Context
	observe                                                         func(backend.MemberWorkEvent)
	runtime                                                         *ExecutionRuntime
	memoryStore                                                     *MemoryRoundStore
	memoryReplacement                                               *memoryConfirmation
	memoryHeader                                                    *confirmationHeader
	callbackDone                                                    bool
	baseInstalled, corrected, correctionsReady, correctionInstalled bool
}

func (p *preparedConfirmation) header() confirmationHeader {
	if p.memoryHeader != nil {
		return *p.memoryHeader
	}
	c := p.candidate
	return confirmationHeader{ID: c.ID, Index: c.Index, Status: c.Segment.Status, Version: c.Segment.ContentVersion, TargetIsNull: c.Segment.TargetIsNull}
}

func (p *preparedConfirmation) project(ctx context.Context) (err error) {
	c, r, doc := p.candidate, p.runtime, p.doc
	header := p.header()
	if p.lifetime != nil {
		ctx = completionContext{Context: p.lifetime, values: ctx}
	}
	if !p.deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, p.deadline)
		defer cancel()
	}
	buildCtx := r.BuildContext(WithExecutionRuntime(ctx, r), p.owner)
	if !p.baseInstalled {
		started := time.Now()
		err := installPreparedConfirmedHeader(buildCtx, r, doc, header, p.result, p.owner, p.plan, p.borrowed, func() { p.baseInstalled = true })
		if p.observe != nil {
			p.observe(backend.MemberWorkEvent{Phase: "confirmation", Operation: "install", DurationNS: time.Since(started).Nanoseconds()})
		}
		if err != nil {
			return err
		}
	}
	previous := doc.Segments[header.Index]
	defer func() {
		if err != nil && !p.correctionInstalled {
			doc.Segments[header.Index] = previous
		}
	}()
	buildCtx = r.BuildContext(WithExecutionRuntime(ctx, r), p.callbackOwner)
	if !p.callbackDone {
		if p.apply != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			applied, err := p.apply(buildCtx, c, p.result)
			if err != nil {
				return err
			}
			p.callbackDone = true
			if applied {
				p.corrected = true
				p.result.TargetText = doc.Segments[header.Index].Target
				p.result.Issues = doc.Segments[header.Index].Issues
				header.Status, header.Version, header.TargetIsNull = doc.Segments[header.Index].Status, doc.Segments[header.Index].ContentVersion, doc.Segments[header.Index].TargetIsNull
				if p.memoryHeader != nil {
					*p.memoryHeader = header
				} else {
					c.Segment.Status, c.Segment.ContentVersion, c.Segment.TargetIsNull = header.Status, header.Version, header.TargetIsNull
				}
				// The public callback may produce new backing. Its corrections are
				// paid by this existing owner; a failed clone keeps the handoff.
			}
		}
		p.callbackDone = true
	}
	if p.corrected && !p.correctionsReady {
		err = r.Run(buildCtx, func() error {
			b, err := backend.NewBuildBoundsFromContext(buildCtx, 0, 0)
			if err != nil {
				return err
			}
			if unsafe.SliceData(p.result.Issues) != unsafe.SliceData(previous.Issues) {
				var private []qa.QualityIssue
				if err := b.CloneInto(p.result.Issues, &private); err != nil {
					return err
				}
				p.result.Issues = private
			}
			p.plan, p.borrowed, err = prepareConfirmedFields(buildCtx, b, p.result, false, previous)
			if err != nil {
				return err
			}
			if p.memoryStore != nil {
				p.memoryStore.mu.Lock()
				closed := p.memoryStore.closed
				p.memoryStore.mu.Unlock()
				if !closed {
					if err := backend.ReserveSlice[memoryConfirmation](b, 1); err != nil {
						return err
					}
					payload, err := b.EncodeJSON(p.result, r.Window.limits.ItemBytes)
					if err != nil {
						return err
					}
					next := &memoryConfirmation{payload: payload, admission: r.Admission, owner: p.memoryStore.confirmationOwner(header.ID) + ":" + NewWorkID(), transferredBytes: int64(cap(payload)) + int64(unsafe.Sizeof(memoryConfirmation{}))}
					credit, err := b.RetainedAllocation(next.transferredBytes)
					if err != nil {
						return err
					}
					if err = backend.TransferBuildAllocationToBuild(buildCtx, credit, next.owner); err != nil {
						credit.Release()
						return err
					}
					p.memoryReplacement = next
				}
			}
			return err
		})
		if err != nil {
			return err
		}
		p.correctionsReady = true
	}
	doc.Segments[header.Index] = previous
	// No Run, JSON decoding, backing measurement or capacity growth is needed
	// on the ordinary accepted path. Close still permits this paid transfer.
	if p.corrected && !p.correctionInstalled {
		err = installPreparedConfirmedHeader(buildCtx, r, doc, header, p.result, p.callbackOwner, p.plan, p.borrowed, func() { p.correctionInstalled = true })
		if err != nil {
			return err
		}
	}
	if p.memoryReplacement != nil {
		transferred, err := p.memoryStore.updateMemoryConfirmation(header, p.memoryReplacement)
		if transferred || err == nil {
			p.memoryReplacement = nil
		}
		if err != nil {
			return err
		}
	}
	doc.Segments[header.Index].RoundWork.Completed = true
	return err
}

func (p *preparedConfirmation) release() error {
	r, c := p.runtime, p.candidate
	owner, callbackOwner := p.owner, p.callbackOwner
	observe := p.observe
	var replacementErr error
	if p.memoryReplacement != nil {
		replacementErr = p.memoryReplacement.close()
	}
	p.result = TranslatedSegment{}
	c.PreparedPayload, c.PreparedHeader = nil, nil
	c.Segment.Target, c.Segment.Issues = "", nil
	*p = preparedConfirmation{}
	started := time.Now()
	err := errors.Join(replacementErr, r.Admission.ReleaseBuild(owner), r.Admission.ReleaseBuild(callbackOwner))
	if observe != nil {
		observe(backend.MemberWorkEvent{Phase: "confirmation", Operation: "owner_release", DurationNS: time.Since(started).Nanoseconds()})
	}
	return err
}

func (r *ExecutionRuntime) rememberConfirmation(p *preparedConfirmation) error {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	if r.pendingConfirmations == nil {
		r.pendingConfirmations = make(map[documentSlot]*preparedConfirmation)
	}
	header := p.header()
	r.pendingConfirmations[documentSlot{p.doc, header.Index}] = p
	// A temporary Memory store may close as the entrypoint returns. Its own
	// candidate cleanup cannot release this independent paid handoff owner.
	move := func(source, target string) error {
		if n := r.Admission.BuildBytes(source); n > 0 {
			if err := r.Admission.MoveBuildAllocations(source, []backend.BuildAllocationTransfer{{Owner: target, Bytes: n}}); err != nil {
				return err
			} else {
				if source == p.owner {
					p.owner = target
				} else {
					p.callbackOwner = target
				}
			}
		}
		return nil
	}
	transferErr := errors.Join(move(p.owner, "confirmation:"+header.ID), move(p.callbackOwner, "confirmation-callback:"+header.ID))
	r.Window.Release(header.ID)
	return transferErr
}

// RetryConfirmedDocument never calls a model or Commit. A successful public
// callback is not repeated when only its receiver failed. Failed callbacks may
// be retried explicitly by their owner within the original execution lifetime.
func (r *ExecutionRuntime) RetryConfirmedDocument(ctx context.Context, doc *Document) error {
	for {
		r.ownerMu.Lock()
		var p *preparedConfirmation
		var key documentSlot
		for slot, pending := range r.pendingConfirmations {
			if slot.document == doc {
				key, p = slot, pending
				break
			}
		}
		r.ownerMu.Unlock()
		if p == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return &ConfirmationProjectionError{Index: key.index, Cause: err}
		}
		if p.lifetime != nil && p.lifetime.Err() != nil {
			return &ConfirmationProjectionError{Index: key.index, Cause: p.lifetime.Err()}
		}
		if !p.deadline.IsZero() && !time.Now().Before(p.deadline) {
			return &ConfirmationProjectionError{Index: key.index, Cause: context.DeadlineExceeded}
		}
		projectionErr := p.project(ctx)
		if projectionErr != nil && !doc.Segments[key.index].RoundWork.Completed {
			return &ConfirmationProjectionError{Index: key.index, Cause: projectionErr}
		}
		r.ownerMu.Lock()
		delete(r.pendingConfirmations, key)
		r.ownerMu.Unlock()
		r.Window.Release(p.header().ID)
		if err := errors.Join(projectionErr, p.release()); err != nil {
			return err
		}
	}
}

func (r *ExecutionRuntime) closePendingConfirmations(doc *Document) error {
	var result error
	for key, p := range r.pendingConfirmations {
		if doc != nil && key.document != doc {
			continue
		}
		delete(r.pendingConfirmations, key)
		r.Window.Release(p.header().ID)
		result = errors.Join(result, p.release())
	}
	return result
}
