package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
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

func archiveCapturedResponse(ctx context.Context, h stagedHandler, header MainResponseHeader, store RoundStore, runtime *ExecutionRuntime) (MainResponseHeader, error) {
	capture, err := store.LoadCapture(ctx, header.ID, runtime.responseLimit())
	if err != nil {
		return header, err
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
	view, err := repair.PrepareArchive(b, raw, header.TextMode, h.ModeName() == "revise", ids, opt)
	contentFailure := ""
	if err != nil {
		var content *repair.ArchiveContentError
		if !errors.As(err, &content) {
			return header, err
		}
		contentFailure = "invalid model response"
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
	if handler, ok := h.(*TranslateHandler); ok && handler.InlineBootstrap && view != nil {
		entries, e := view.Glossary(b)
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
			selected := archivedOutput{Failure: contentFailure}
			if view != nil {
				selected, e = view.Member(mb, start+i)
				if e != nil {
					finish()
					return header, e
				}
			}
			payload, e := mb.EncodeJSON(selected, runtime.Window.limits.ItemBytes)
			if e != nil {
				finish()
				return header, e
			}
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
		e := runtime.SaveNamed(ctx, persistence.Metadata{Operation: "output_archive", Class: persistence.Reliable, RequestID: header.ID, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error {
			return store.PublishOutputs(saveCtx, OutputPublication{Key: archiveMemberKey(header, start), Members: members, Next: next, Digest: digest, Glossary: public, PublicAllocation: publicAllocation, Complete: complete})
		})
		finish()
		if e != nil {
			return header, e
		}
		for _, member := range members {
			header.OutputMemberBytes += member.Bytes
		}
		header.CorrectedBytes = header.OutputMemberBytes
		header.ArchiveCursor, header.ArchiveDigest = next, digest
	}
	if header.ArchiveCursor != header.MemberCount {
		return header, fmt.Errorf("incomplete output archive")
	}
	header.Phase = "outputs_ready"
	header.ResponseBytes = 0
	header.PublicBytes = int64(len(public))
	return header, nil
}
