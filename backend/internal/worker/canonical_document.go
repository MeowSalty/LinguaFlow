package worker

import (
	"context"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"strconv"
)

// Reload the original document in place. ORM rows are released page by page;
// only the canonical source/target/metadata become long-lived input M.
func (r *JobRunner) loadCanonicalDocument(ctx context.Context, resource int, doc *pipeline.Document) error {
	count, err := r.client.Segment.Query().Where(segment.ResourceIDEQ(resource)).Count(ctx)
	if err != nil {
		return err
	}
	if len(doc.Segments) != count {
		doc.Segments = make([]pipeline.Segment, count)
	}
	afterIndex, afterID, filled := 0, 0, 0
	for {
		q := r.client.Segment.Query().Where(segment.ResourceIDEQ(resource))
		if afterID > 0 {
			q.Where(segment.Or(segment.SegmentIndexGT(afterIndex), segment.And(segment.SegmentIndexEQ(afterIndex), segment.IDGT(afterID))))
		}
		page, err := q.Order(ent.Asc(segment.FieldSegmentIndex), ent.Asc(segment.FieldID)).Limit(16).All(ctx)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			if filled >= len(doc.Segments) {
				return fmt.Errorf("source document membership changed during load")
			}
			seg := pipeline.Segment{DBID: row.ID, DBIndex: row.SegmentIndex, CompletionModes: doc.Segments[filled].CompletionModes, OriginalSource: row.SourceText, ID: strconv.Itoa(row.SegmentIndex), ContentVersion: row.ContentVersion, TargetIsNull: row.TargetText == nil, Source: row.SourceText, Status: string(row.Status), Issues: row.QualityIssues, Translate: true}
			if row.TargetText != nil {
				seg.Target = *row.TargetText
			}
			if row.Meta != nil && *row.Meta != "" {
				runtime := pipeline.ExecutionRuntimeFromContext(ctx)
				if runtime == nil {
					return fmt.Errorf("canonical load has no execution runtime")
				}
				err = runtime.Run(ctx, func() error {
					b, e := backend.NewBuildBounds(runtime.BudgetLimits().Item, int64(len(*row.Meta)))
					if e != nil {
						return e
					}
					if e = b.ReserveText(int64(len(*row.Meta)), int64(len(*row.Meta))); e != nil {
						return e
					}
					if e = b.DecodeJSON([]byte(*row.Meta), &seg.Meta); e != nil {
						return e
					}
					_, e = canonicalMetadataNumbers(b, seg.Meta)
					return e
				})
				if err != nil {
					return err
				}
			}
			doc.Segments[filled] = seg
			filled++
			afterIndex, afterID = row.SegmentIndex, row.ID
		}
		page = nil
	}
	if filled != count {
		return fmt.Errorf("source document membership changed during load")
	}
	return nil
}
func canonicalDBIdentity(doc *pipeline.Document, index int) (int, bool) {
	if index < 0 || index >= len(doc.Segments) || doc.Segments[index].DBID < 1 {
		return 0, false
	}
	return doc.Segments[index].DBID, true
}
