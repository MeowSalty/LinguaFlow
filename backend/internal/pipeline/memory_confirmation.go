package pipeline

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// The store retains the encoded buffer which finalization already paid for.
// Decoded fields belong only to their current document, including after restart.
type memoryConfirmation struct {
	payload          []byte
	admission        *backend.RequestAdmission
	owner            string
	localAllocation  *backend.OwnedAllocation
	status           string
	version          int64
	targetIsNull     bool
	stored           bool
	transferredBytes int64
	retired          *memoryConfirmation
	candidateID      string
	index            int
}

func (s *MemoryRoundStore) confirmationOwner(id string) string {
	return "memory-confirmation:" + s.identity + ":" + id
}

// Direct Commit callers use the same bounded preparation before acceptance.
// Staged execution supplies its original-build record and needs no new growth.
func (s *MemoryRoundStore) prepareMemoryConfirmation(ctx context.Context, c *Candidate, result TranslatedSegment) (*memoryConfirmation, error) {
	if result.memoryConfirmation != nil {
		return result.memoryConfirmation, nil
	}
	owner := s.confirmationOwner(c.ID)
	limit := DefaultCandidateLimits().ItemBytes
	if runtime := ExecutionRuntimeFromContext(ctx); runtime != nil {
		ctx = runtime.BuildContext(ctx, owner+":build")
		limit = runtime.Window.limits.ItemBytes
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, limit, 0)
	if err != nil {
		return nil, err
	}
	defer b.CloseRetired()
	if err := backend.ReserveSlice[memoryConfirmation](b, 1); err != nil {
		return nil, err
	}
	result.Index = c.Index
	payload, err := b.EncodeJSON(result, limit)
	if err != nil {
		return nil, err
	}
	record := &memoryConfirmation{payload: payload, owner: owner, candidateID: c.ID, index: c.Index}
	record.transferredBytes = int64(cap(payload)) + int64(unsafe.Sizeof(memoryConfirmation{}))
	credit, err := b.RetainedAllocation(record.transferredBytes)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			_ = credit.ReturnToBuild()
		}
	}()
	admission, _ := backend.BuildOwnership(ctx)
	if admission != nil {
		if err := backend.TransferBuildAllocationToBuild(ctx, credit, owner); err != nil {
			return nil, err
		}
		record.admission = admission
	} else {
		if err := credit.DetachBuild(); err != nil {
			return nil, err
		}
		record.localAllocation = credit
	}
	retained = true
	return record, nil
}

func (record *memoryConfirmation) close() error {
	record.payload = nil
	if record.retired != nil {
		if err := record.retired.close(); err != nil {
			return err
		}
		record.retired = nil
	}
	if record.admission != nil {
		return record.admission.ReleaseBuild(record.owner)
	}
	if record.localAllocation != nil {
		record.localAllocation.Release()
		record.localAllocation = nil
	}
	return nil
}

// Unlike the staged preprepared path, a direct Existing caller receives private
// decoded target/issues. Its Candidate owns the exact detached read receipt;
// Store.Close does not retire the caller's fields or refund a live read.
func (s *MemoryRoundStore) prepareDirectMemoryExisting(ctx context.Context, c *Candidate, record *memoryConfirmation) error {
	runtime := ExecutionRuntimeFromContext(ctx)
	limit := DefaultCandidateLimits().ItemBytes
	if runtime != nil {
		ctx = runtime.BuildContext(ctx, "memory-confirmation-read:"+NewWorkID())
		limit = runtime.Window.limits.ItemBytes
	}
	decode := func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, limit, int64(len(record.payload)))
		if err != nil {
			return err
		}
		defer b.CloseRetired()
		var authoritative TranslatedSegment
		defer func() { authoritative = TranslatedSegment{} }()
		if err := b.DecodeJSON(record.payload, &authoritative); err != nil {
			return err
		}
		meta := Segment{Meta: authoritative.Meta}
		if err := restoreMetadataTypes(&meta, metadataTypes(c.Segment.Meta), b); err != nil {
			return err
		}
		authoritative.Meta = meta.Meta
		plan, _, err := prepareConfirmedFields(ctx, b, authoritative, true, Segment{})
		if err != nil {
			return err
		}
		var amount int64
		for _, n := range []int64{plan.TargetBytes, plan.IssuesBytes, plan.MetaBytes, plan.ProtectedBytes} {
			amount, err = backend.CheckedAdd(amount, n)
			if err != nil {
				return err
			}
		}
		credit, err := b.RetainedAllocation(amount)
		if err != nil {
			return err
		}
		if err := credit.DetachBuild(); err != nil {
			credit.Release()
			return err
		}
		c.CloseConfirmedRead()
		c.confirmedRead = credit
		c.Segment.Target, c.Segment.Issues = authoritative.TargetText, authoritative.Issues
		c.Segment.Meta, c.Segment.Protected = authoritative.Meta, authoritative.Protected
		return nil
	}
	if runtime != nil {
		return runtime.Run(ctx, decode)
	}
	return decode()
}

// Called with mutation held, so Close cannot end the payload while it is read.
func (s *MemoryRoundStore) restoreMemoryConfirmation(ctx context.Context, doc *Document, index int, record *memoryConfirmation) error {
	runtime := ExecutionRuntimeFromContext(ctx)
	if runtime == nil {
		return backend.ErrPipelineCapacity
	}
	owner := "memory-confirmation-load:" + NewWorkID()
	preparedOwner := owner + ":prepared"
	ctx = runtime.BuildContext(ctx, owner)
	defer runtime.Admission.ReleaseBuild(owner)
	defer runtime.Admission.ReleaseBuild(preparedOwner)
	return runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, runtime.Window.limits.ItemBytes, int64(len(record.payload)))
		if err != nil {
			return err
		}
		defer b.CloseRetired()
		var result TranslatedSegment
		defer func() { result = TranslatedSegment{} }()
		if err = b.DecodeJSON(record.payload, &result); err != nil {
			return err
		}
		previous := doc.Segments[index]
		if result.Index != index || result.ID != "" && result.ID != previous.ID || result.SourceText != "" && result.SourceText != rawSource(&previous) {
			return fmt.Errorf("memory confirmation does not match canonical member %d", index)
		}
		meta := Segment{Meta: result.Meta}
		if err = restoreMetadataTypes(&meta, metadataTypes(previous.Meta), b); err != nil {
			return err
		}
		result.Meta = meta.Meta
		plan, borrowed, err := prepareConfirmedFields(ctx, b, result, true, previous)
		if err != nil {
			return err
		}
		amount := plan.TargetBytes + plan.IssuesBytes + plan.MetaBytes + plan.ProtectedBytes
		credit, err := b.RetainedAllocation(amount)
		if err != nil {
			return err
		}
		if err = backend.TransferBuildAllocationToBuild(ctx, credit, preparedOwner); err != nil {
			credit.Release()
			return err
		}
		c := &Candidate{Index: index, Segment: previous}
		c.Segment.Status, c.Segment.ContentVersion, c.Segment.TargetIsNull = record.status, record.version, record.targetIsNull
		return installPreparedConfirmedFields(ctx, runtime, doc, c, result, preparedOwner, plan, borrowed, func() { doc.Segments[index].RoundWork.Completed = true })
	})
}

// Callback corrections replace the stored encoding only after their document
// installation. The replacement was already prepared under the callback owner.
func (s *MemoryRoundStore) updateMemoryConfirmation(header confirmationHeader, next *memoryConfirmation) (bool, error) {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, next.close()
	}
	record := s.confirmations[header.Index]
	if record == nil {
		return false, ErrCandidateStale
	}
	next.stored = true
	next.candidateID, next.index = record.candidateID, record.index
	next.status, next.version, next.targetIsNull = header.Status, header.Version, header.TargetIsNull
	s.confirmations[header.Index] = next
	if err := record.close(); err != nil {
		next.retired = record
		return true, err
	}
	return true, nil
}
