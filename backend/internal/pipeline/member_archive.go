package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func inputMemberTotal(members []InputMemberFrame) int64 {
	var n int64
	for _, m := range members {
		n += m.Bytes
	}
	return n
}
func archiveMemberKey(h MainResponseHeader, ordinal int) MemberKey {
	return MemberKey{RequestID: h.ID, Ordinal: ordinal, CaptureEpoch: h.CaptureEpoch}
}
func archiveDigest(previous, digest string) string {
	sum := sha256.Sum256([]byte(previous + digest))
	return hex.EncodeToString(sum[:])
}

func archiveCapturedResponse(ctx context.Context, h stagedHandler, header MainResponseHeader, store RoundStore, runtime *ExecutionRuntime) (confirmed MainResponseHeader, resultErr error) {
	operation := "load_capture"
	defer func() {
		if resultErr != nil {
			detail := backend.RejectionEvidenceFromContext(ctx)
			detail.Phase, detail.Operation = "archive/"+operation, operation
			detail.MemberKnown, detail.Ordinal, detail.Cursor = true, header.ArchiveCursor, header.Cursor
			resultErr = backend.AnnotateRejection(resultErr, detail)
		}
	}()
	capture, err := store.LoadCapture(ctx, header.ID, runtime.responseLimit())
	if err != nil {
		return header, backend.AnnotateRejection(err, backend.RejectionEvidence{Branch: "response_capture_read", Operation: "load_capture", Phase: string(header.Phase), RequestID: header.ID})
	}
	defer capture.Release()
	b, err := backend.NewBuildBoundsFromContext(ctx, runtime.responseLimit(), int64(len(capture.Payload)))
	if err != nil {
		return header, err
	}
	defer b.CloseRetired()
	if err = backend.ReserveSlice[string](b, int64(header.MemberCount)); err != nil {
		return header, err
	}
	ids := make([]string, header.MemberCount)
	for i := range ids {
		m, e := store.LoadInputMember(ctx, archiveMemberKey(header, i), runtime.handoffLimit())
		if e != nil {
			return header, e
		}
		ids[i] = m.PromptID
		m.Release()
	}
	var opt repair.Options
	switch handler := h.(type) {
	case *TranslateHandler:
		opt = handler.Repair
	case *ReviseHandler:
		opt = handler.Repair
	}
	rawCredit, err := b.OwnAllocation(int64(len(capture.Payload)), 1)
	if err != nil {
		return header, err
	}
	defer rawCredit.Release()
	raw := string(capture.Payload)
	defer func() { raw = "" }()
	session, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	var diagnostic *stagedBatchDiagnostic
	if session != nil && session.intent.ID == header.ID {
		diagnostic = session.mainDiagnostic
	}
	operation = "prepare_archive"
	view, err := repair.PrepareArchive(b, raw, header.TextMode, h.ModeName() == "revise", ids, opt)
	contentFailure := ""
	parseDiagnostic := ""
	var parseError *repair.ArchiveContentError
	if err != nil {
		if !errors.As(err, &parseError) {
			return header, err
		}
		contentFailure = invalidModelResponse
		if promptUpgradeEnabled(h, header) {
			parseDiagnostic, err = parseFailureDiagnostic(b, parseError.Err, raw)
			if err != nil {
				return header, err
			}
		}
	}
	defer view.Release()
	defer func() {
		if view == nil {
			return
		}
		work := view.Work()
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "prepare", Operation: "source_scan", Bytes: work.JSONScanBytes})
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "prepare", Operation: "id_decode", Bytes: work.IDDecodeBytes})
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "prepare", Operation: "selection", Bytes: work.SelectionBytes})
	}()
	var public []byte
	var publicAllocation *backend.OwnedAllocation
	defer func() { publicAllocation.Release() }()
	var entries []prompt.BootstrapEntry
	if handler, ok := h.(*TranslateHandler); ok && view != nil && handler.InlineBootstrap {
		var e error
		entries, e = view.Glossary(b)
		if e != nil {
			return header, e
		}
		var selected []glossary.Entry
		for _, entry := range entries {
			if utf8.RuneCountInString(entry.Source) >= max(2, handler.MinBootstrapSourceLen) && entry.Source != "" && entry.Target != "" {
				selected = append(selected, glossary.Entry{Source: entry.Source, Target: entry.Target, Notes: entry.Notes})
			}
		}
		public, err = b.EncodeJSON(selected, runtime.responseLimit())
		if err != nil {
			return header, err
		}
		publicAllocation, err = b.RetainedAllocation(int64(cap(public)))
		if err != nil {
			return header, err
		}
		if err = publicAllocation.DetachBuild(); err != nil {
			return header, err
		}
	}
	digest := header.ArchiveDigest
	for start := header.ArchiveCursor; start < header.MemberCount; start += 16 {
		count := min(16, header.MemberCount-start)
		members := make([]OutputMemberFrame, 0, count)
		partial := false
		var bounds []*backend.BuildBounds
		finish := func() {
			for i := range members {
				members[i].Allocation.Release()
				members[i].Payload = nil
			}
			for _, bound := range bounds {
				bound.CloseRetired()
			}
		}
		for i := 0; i < count; i++ {
			mb, e := backend.NewBuildBoundsFromContext(ctx, runtime.Window.limits.ItemBytes, 0)
			if e != nil {
				finish()
				return header, e
			}
			bounds = append(bounds, mb)
			selected := archivedOutput{Failure: contentFailure, BatchParseFailure: parseError != nil}
			if view != nil {
				operation = "select_archive_member"
				selected, e = view.Member(mb, start+i)
				if e != nil {
					var content *responsecontract.MemberContentError
					if !errors.As(e, &content) {
						finish()
						return header, e
					}
					selected = archivedOutput{Failure: content.Error(), Outcome: MemberOutcome{Reason: content.Kind, Limit: content.Limit, Observed: content.Observed}}
				}
			}
			selected.Outcome.Key = archiveMemberKey(header, start+i)
			selected.Outcome.Accepted = selected.Failure == "" && !selected.Missing
			if !selected.Outcome.Accepted && selected.Outcome.Reason == "" {
				selected.Outcome.Reason = responsecontract.MemberInvalid
				if selected.Missing {
					selected.Outcome.Reason = responsecontract.MemberMissing
				}
			}
			operation = "encode_archive_member"
			payload, e := encodeArchivedOutput(mb, &selected, archiveMemberKey(header, start+i), runtime.Window.limits.ItemBytes)
			if e != nil {
				finish()
				return header, e
			}
			partial = partial || !selected.Outcome.Accepted
			allocation, e := mb.RetainedAllocation(int64(cap(payload)))
			if e != nil {
				finish()
				return header, e
			}
			if e = allocation.DetachBuild(); e != nil {
				finish()
				return header, e
			}
			d := memberResponseDigest(ctx, payload)
			digest = archiveDigest(digest, d)
			members = append(members, OutputMemberFrame{Key: archiveMemberKey(header, start+i), Payload: payload, Bytes: int64(len(payload)), Digest: d, Allocation: allocation})
		}
		next := start + count
		complete := next == header.MemberCount
		publication := OutputPublication{Key: archiveMemberKey(header, start), Members: members, Next: next, Digest: digest, Glossary: public, PublicAllocation: publicAllocation, Complete: complete}
		if complete {
			publication.ParseDiagnostic = parseDiagnostic
		}
		e := runtime.SaveNamed(ctx, persistence.Metadata{Operation: string(persistence.OutputArchive), Class: persistence.Reliable, RequestID: header.ID, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error {
			confirmed, confirmationErr := store.PublishOutputs(saveCtx, publication)
			if confirmationErr == nil {
				header = confirmed
			}
			return confirmationErr
		})
		finish()
		if e != nil {
			return header, e
		}
		if diagnostic != nil {
			if partial && diagnostic.event.Stage == "translate" {
				diagnostic.event.Status = "partial"
			}
			if complete {
				emitMainBatchEvent(ctx, session, func(event *progress.BatchEvent) {
					event.ReceivedLength = len(raw)
					event.ReceivedContent, _ = cloneBatchContent(b, raw)
					event.ResponseContent = event.ReceivedContent
					if parseError != nil {
						event.Status = "failed"
						event.ErrorType = "parse_error"
						event.ErrorMessage = parseError.Error()
						event.Repaired = parseError.Repaired
						event.ShrinkAttempted = event.Stage == "translate" && event.SegmentCount > 1
					} else {
						event.Repaired = view.Repaired()
						if handler, ok := h.(*TranslateHandler); ok && !handler.InlineBootstrap {
							entries, _ = view.Glossary(b)
						}
						if len(entries) > 0 {
							if _, err := b.MeasureJSONLimited(entries, progress.MaxSSEBatchContentBytes); err == nil {
								event.AddedGlossary = entries
							}
						}
					}
				})
			}
		}
		if e = runtime.Admission.RestoreResponseOwnership(header.ID, prepareIntent(h, store, backend.RequestStageMain).RoundIndex, storeResourceID(store), header.Facts(), header.Phase, true); e != nil {
			return header, e
		}
	}
	if header.ArchiveCursor != header.MemberCount {
		return header, fmt.Errorf("incomplete output archive")
	}
	return header, nil
}

// selected is private to this member. A replacement changes its representation
// and needs its own measurement before the fixed-size encoder can admit it.
func encodeArchivedOutput(b *backend.BuildBounds, selected *archivedOutput, key MemberKey, limit int64) ([]byte, error) {
	// Invalid is a parser-only flag. Keep the identity for duplicate checks
	// and discard the rest so archive recovery cannot verify the malformed row.
	for i, entry := range selected.RubyOutput {
		if entry.Invalid {
			selected.RubyOutput[i] = ruby.OutputEntry{ID: entry.ID, Invalid: true}
		}
	}
	measured, err := b.MeasureJSON(selected)
	if err != nil {
		return nil, err
	}
	if measured > limit {
		outcome := MemberOutcome{Key: key, Reason: responsecontract.MemberTooLarge, Limit: limit, Observed: measured}
		*selected = archivedOutput{Failure: outcome.String(), Outcome: outcome, BatchParseFailure: selected.BatchParseFailure}
		measured, err = b.MeasureJSON(selected)
		if err != nil {
			return nil, err
		}
	}
	return b.EncodeJSONMeasured(selected, measured, limit)
}
