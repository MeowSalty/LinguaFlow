package worker

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"strconv"
)

// Reload within one bounded source snapshot, retaining the lease until every
// document consumer has joined.
func (r *JobRunner) loadCanonicalDocument(ctx context.Context, resource int, doc *pipeline.Document) error {
	rows, lease, err := service.LoadExecutionSegments(ctx, r.client, resource)
	if err != nil {
		return err
	}
	var next []pipeline.Segment
	success := false
	defer func() {
		if !success {
			lease.Close()
		}
	}()
	err = lease.RunBuild(ctx, func() error {
		next = make([]pipeline.Segment, len(rows))
		for i, row := range rows {
			meta, err := service.ExecutionSegmentMeta(ctx, row)
			if err != nil {
				return err
			}
			seg := pipeline.Segment{DBID: row.ID, DBIndex: row.SegmentIndex, OriginalSource: row.SourceText, ID: strconv.Itoa(row.SegmentIndex), ContentVersion: row.ContentVersion, TargetIsNull: row.TargetText == nil, Source: row.SourceText, Status: string(row.Status), Issues: row.QualityIssues, Translate: true, Meta: meta}
			if i < len(doc.Segments) {
				seg.CompletionModes = doc.Segments[i].CompletionModes
			}
			if row.TargetText != nil {
				seg.Target = *row.TargetText
			}
			next[i] = seg
		}
		return nil
	})
	if err != nil {
		return err
	}
	doc.Segments = next
	service.ReplaceExecutionDocumentLease(ctx, lease)
	success = true
	return nil
}
func canonicalDBIdentity(doc *pipeline.Document, index int) (int, bool) {
	if index < 0 || index >= len(doc.Segments) || doc.Segments[index].DBID < 1 {
		return 0, false
	}
	return doc.Segments[index].DBID, true
}
