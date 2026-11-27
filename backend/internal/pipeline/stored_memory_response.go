package pipeline

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
)

var _ StoredResponseStore = (*MemoryRoundStore)(nil)

func (s *MemoryRoundStore) SettleStoredResponse(ctx context.Context, doc *Document, in StoredResponseSettlement) (result error) {
	id := in.Header.ID
	s.mutation.Lock()
	defer func() {
		s.mutation.Unlock()
		if !errors.Is(result, backend.ErrAdmissionClosed) {
			result = errors.Join(result, s.RetireResponse(ctx, id))
		}
	}()
	s.mu.Lock()
	request, err := s.checkStoredSettlement(in)
	s.mu.Unlock()
	if err != nil || request == nil {
		return err
	}

	var prepared []*memoryConfirmation
	if doc != nil && in.Apply != nil {
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
		if err != nil {
			return err
		}
		defer b.CloseRetired()
		if err := backend.ReserveSlice[Segment](b, int64(len(in.Dispositions))); err != nil {
			return err
		}
		originals := make([]Segment, len(in.Dispositions))
		for i, d := range in.Dispositions {
			originals[i] = doc.Segments[d.Index]
		}
		restore := func() {
			for i, d := range in.Dispositions {
				doc.Segments[d.Index] = originals[i]
			}
		}
		if err := in.Apply(); err != nil {
			restore()
			return err
		}
		prepared, err = s.prepareStoredConfirmations(ctx, doc, in, originals)
		restore()
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	if in.Install != nil {
		in.Install()
	}
	if s.confirmations == nil && len(prepared) > 0 {
		s.confirmations = make(map[int]*memoryConfirmation)
	}
	for i, d := range in.Dispositions {
		if d.Accepted {
			s.state.Completed = append(s.state.Completed, d.Index)
			s.state.Cursors[d.Index] = WorkCursor{State: "resolved"}
			if len(prepared) > 0 && prepared[i] != nil {
				s.confirmations[d.Index] = prepared[i]
			}
		} else {
			s.state.Cursors[d.Index] = d.Cursor
		}
	}
	request.dropRequestBacking()
	h := &request.response.MainResponseHeader
	h.CaptureBytes, h.InputMemberBytes, h.BusinessBound = 0, 0, 0
	h.Cursor, h.Phase, h.ArchiveDigest = h.MemberCount, responsecontract.Promoted, h.ResponseDigest
	s.mu.Unlock()
	for i, record := range prepared {
		if record == nil {
			continue
		}
		// The capture is retired. Build pressure now delays installing the
		// confirmed document fields, without rereading or reparsing the capture.
		for {
			err := s.restoreMemoryConfirmation(ctx, doc, in.Dispositions[i].Index, record)
			wait := workDeferred(err)
			if wait == nil {
				if err == nil {
					break
				}
				return err
			}
			if err := waitPreparation(ctx, wait.Wait); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkStoredSettlement is called with the state lock held before any memory
// publication callback can modify a document or dictionary.
func (s *MemoryRoundStore) checkStoredSettlement(in StoredResponseSettlement) (*memoryResponse, error) {
	if s.closed {
		return nil, backend.ErrAdmissionClosed
	}
	r, err := s.memberLocked(MemberKey{RequestID: in.Header.ID, CaptureEpoch: in.Header.CaptureEpoch})
	if err != nil {
		return nil, err
	}
	h := r.response.MainResponseHeader
	if h.ResponseDigest != in.Header.ResponseDigest || h.Mode != in.Header.Mode || h.MemberCount != len(in.Dispositions) {
		return nil, fmt.Errorf("stored response identity mismatch")
	}
	if h.Phase == responsecontract.Promoted {
		return nil, nil
	}
	if h != in.Header || h.Phase != responsecontract.CapturePending {
		return nil, fmt.Errorf("stored response is not pending")
	}
	for i, d := range in.Dispositions {
		member := r.inputs[i]
		if member.Index != d.Index || member.SegmentID != d.SegmentID || s.state.Cursors[d.Index].State != "response_pending" {
			return nil, fmt.Errorf("stored response member changed")
		}
	}
	return r, nil
}

func (s *MemoryRoundStore) prepareStoredConfirmations(ctx context.Context, doc *Document, in StoredResponseSettlement, originals []Segment) (records []*memoryConfirmation, result error) {
	runtime := ExecutionRuntimeFromContext(ctx)
	owner := "stored-install:" + in.Header.ID
	ctx = runtime.BuildContext(ctx, owner)
	defer runtime.Admission.ReleaseBuild(owner)
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	if err = backend.ReserveSlice[*memoryConfirmation](b, int64(len(in.Dispositions))); err != nil {
		return nil, err
	}
	prepared := make([]*memoryConfirmation, len(in.Dispositions))
	defer func() {
		if result != nil {
			for _, r := range prepared {
				if r != nil {
					result = errors.Join(result, r.close())
				}
			}
		}
	}()
	for i, d := range in.Dispositions {
		if !d.Accepted {
			continue
		}
		seg := doc.Segments[d.Index]
		if unsafe.SliceData(seg.Issues) == unsafe.SliceData(originals[i].Issues) && cap(seg.Issues) == cap(originals[i].Issues) {
			continue
		}
		c := &Candidate{Index: d.Index, ID: NewWorkID(), Segment: seg}
		err := runtime.Run(ctx, func() error {
			record, err := s.prepareMemoryConfirmation(ctx, c, TranslatedSegment{ID: seg.ID, Issues: seg.Issues})
			if err != nil {
				return err
			}
			record.stored, record.issuesOnly, record.status, record.version, record.targetIsNull = true, true, seg.Status, seg.ContentVersion, seg.TargetIsNull
			prepared[i] = record
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return prepared, nil
}
