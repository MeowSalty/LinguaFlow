package worker

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
	"sort"
)

func loadCanonicalResolved(ctx context.Context, client *ent.Client, resource int, doc *pipeline.Document) error {
	for _, mode := range []string{pipeline.RoundModeExtract, pipeline.RoundModeSemanticQA, pipeline.RoundModeAdjudicate, pipeline.RoundModeRevise, "correct"} {
		after := 0
		for {
			page, err := client.JobRoundSegment.Query().Where(jobroundsegment.IDGT(after), jobroundsegment.HasJobRoundWith(jobround.JobResourceIDEQ(resource), jobround.ModeEQ(mode))).Order(ent.Asc(jobroundsegment.FieldID)).Select(jobroundsegment.FieldID, jobroundsegment.FieldSegmentID).WithSegment(func(q *ent.SegmentQuery) { q.Select(segment.FieldID, segment.FieldSegmentIndex) }).Limit(16).All(ctx)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			for _, link := range page {
				row, err := link.Edges.SegmentOrErr()
				if err != nil {
					return err
				}
				i := sort.Search(len(doc.Segments), func(i int) bool { return doc.Segments[i].DBIndex >= row.SegmentIndex })
				if i >= len(doc.Segments) || doc.Segments[i].DBID != row.ID {
					return workstate.ErrManifest
				}
				doc.Segments[i].CompletionModes |= pipeline.CompletedModeMask(mode)
				after = link.ID
			}
		}
	}
	return nil
}
