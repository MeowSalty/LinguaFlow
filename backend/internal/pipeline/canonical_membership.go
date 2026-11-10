package pipeline

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"unicode"
	"unicode/utf8"
)

func structuralProtectedText(source string, known map[string]string) bool {
	for pos := 0; pos < len(source); {
		if len(source)-pos >= 13 {
			if _, ok := known[source[pos:pos+13]]; ok {
				pos += 13
				continue
			}
		}
		r, n := utf8.DecodeRuneInString(source[pos:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		pos += n
	}
	return true
}
func prepareCanonicalMembership(ctx context.Context, h stagedHandler, doc *Document, runtime *ExecutionRuntime) (int, error) {
	count, skipped := 0, 0
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		seg.RoundWork.Member = false
		seg.RoundWork.Completed = false
		seg.RoundWork.Cursor = WorkCursor{}
		switch handler := h.(type) {
		case *TranslateHandler:
			if seg.Skip {
				seg.Target = seg.Source
				skipped++
				continue
			}
			err := prepareCanonicalTranslateSegment(ctx, handler, seg, runtime)
			if err != nil {
				return count, err
			}
			if !seg.Translate {
				continue
			}
			if seg.StructuralOnly {
				seg.Target = rawSource(seg)
				skipped++
				continue
			}
			seg.RoundWork.Member = true
		case *ReviseHandler:
			if seg.CompletionModes&CompletedModeMask(handler.ModeName()) != 0 {
				continue
			}
			if handler.Renderer == nil || handler.Backend == nil || !seg.Translate || (seg.Status != "translated" && seg.Status != "edited") || seg.Target == "" {
				continue
			}
			if _, done := doc.ResolvedIndices[i]; done {
				continue
			}
			for _, issue := range seg.Issues {
				if !issue.IsPending() {
					continue
				}
				for _, code := range handler.IssueCodes {
					if issue.Code == code {
						seg.RoundWork.Member = true
						break
					}
				}
				if seg.RoundWork.Member {
					break
				}
			}
		default:
			return count, backend.ErrBuildCapacity
		}
		if seg.RoundWork.Member {
			count++
		}
	}
	if handler, ok := h.(*TranslateHandler); ok {
		handler.writeSkippedCount(doc, skipped)
	}
	return count, nil
}

func prepareCanonicalTranslateSegment(ctx context.Context, handler *TranslateHandler, seg *Segment, runtime *ExecutionRuntime) error {
	return runtime.Run(ctx, func() error {
		b, err := backend.NewBuildBounds(runtime.Window.limits.ItemBytes, runtime.handoffLimit())
		if err != nil {
			return err
		}
		raw := rawSource(seg)
		if structuralProtectedText(raw, nil) {
			seg.StructuralOnly = true
			return nil
		}
		if handler.Protector == nil {
			seg.StructuralOnly = false
			return nil
		}
		if err = b.ReserveNodes(1, 512); err != nil {
			return err
		}
		tmp := Segment{Source: raw}
		if err = protect.ProtectBounded(b, handler.Protector, &tmp); err != nil {
			return err
		}
		seg.StructuralOnly = structuralProtectedText(tmp.Source, tmp.Protected)
		if seg.Translate && !seg.StructuralOnly {
			if seg.OriginalSource == "" {
				seg.OriginalSource = raw
			}
			seg.Source = tmp.Source
			seg.Protected = tmp.Protected
			if len(tmp.Meta) > 0 {
				if err = b.ReserveNodes(int64(len(tmp.Meta))+1, 1024); err != nil {
					return err
				}
				if seg.Meta == nil {
					seg.Meta = map[string]any{}
				}
				for k, v := range tmp.Meta {
					seg.Meta[k] = v
				}
			}
		}
		return nil
	})
}
