package workstate

import (
	"context"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

// LockResourceJobs locks every affected Job in deterministic order before a
// source-update/deletion transaction locks the Resource or cascades its rows.
func LockResourceJobs(ctx context.Context, tx *ent.Client, resourceID int) ([]int, error) {
	ids, err := tx.Job.Query().Where(job.HasJobResourcesWith(jobresource.HasResourceWith(resource.IDEQ(resourceID)))).Order(ent.Asc(job.FieldID)).IDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := LockJob(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func CalibrateJobs(ctx context.Context, tx *ent.Client, ids []int) error {
	for _, id := range ids {
		if err := Calibrate(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// BeforeDeleteSegments verifies original membership before adjusting unsealed
// legacy totals. A checkpoint-only subtraction would miss deleted unresolved
// members. New manifests are recalibrated from their FK membership after DELETE.
// The caller has already locked all affected jobs and the resource.
func BeforeDeleteSegments(ctx context.Context, tx *ent.Client, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	resourceIDs, err := tx.Segment.Query().Where(segment.IDIn(ids...)).QueryResource().IDs(ctx)
	if err != nil {
		return err
	}
	if len(resourceIDs) == 0 {
		return nil
	}
	rows, err := tx.JobRound.Query().Where(jobround.ManifestSealedEQ(false), jobround.HasJobResourceWith(jobresource.HasResourceWith(resource.IDIn(resourceIDs...)))).Order(ent.Asc(jobround.FieldID)).WithJobResource().All(ctx)
	if err != nil {
		return err
	}
	deleted := make(map[int]bool, len(ids))
	for _, id := range ids {
		deleted[id] = true
	}
	for _, row := range rows {
		completed, err := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(row.ID)).Select(jobroundsegment.FieldSegmentID).All(ctx)
		if err != nil {
			return err
		}
		if row.SegmentTotal == 0 && len(completed) == 0 {
			continue
		}
		members := make(map[int]bool, row.SegmentTotal)
		if len(completed) == row.SegmentTotal {
			// When all work has durable completion facts, those facts themselves
			// recover the complete original set without guessing from current status.
			for _, link := range completed {
				members[link.SegmentID] = true
			}
		} else {
			jr := row.Edges.JobResource
			if jr == nil {
				return ErrManifest
			}
			res, err := jr.QueryResource().Only(ctx)
			if err != nil {
				return err
			}
			query := tx.Segment.Query().Where(segment.ResourceIDEQ(res.ID))
			if len(jr.SegmentIds) > 0 {
				query.Where(segment.IDIn(jr.SegmentIds...))
			}
			memberIDs, err := query.IDs(ctx)
			if err != nil {
				return err
			}
			for _, id := range memberIDs {
				members[id] = true
			}
		}
		if len(members) != row.SegmentTotal {
			return fmt.Errorf("%w: cannot recover original members for legacy round %d before source deletion", ErrManifest, row.ID)
		}
		for _, link := range completed {
			if !members[link.SegmentID] {
				return fmt.Errorf("%w: legacy checkpoint outside original selection for round %d", ErrManifest, row.ID)
			}
		}
		removed := 0
		for id := range members {
			if deleted[id] {
				removed++
			}
		}
		if removed == 0 {
			continue
		}
		if err := tx.JobRound.UpdateOneID(row.ID).SetSegmentTotal(row.SegmentTotal - removed).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
