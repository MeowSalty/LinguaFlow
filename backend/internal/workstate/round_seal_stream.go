package workstate

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

// SealRoundStream freezes membership directly from canonical input. Only 16
// builders are retained; verification replays fixed IDs without copying them.
func (s *Store) SealRoundStream(ctx context.Context, scope Scope, total int, walk func(func(int) error) error) error {
	verify := func(ctx context.Context, tx *ent.Client) (bool, error) {
		r, err := tx.JobRound.Query().Where(jobround.IDEQ(scope.RoundID), jobround.JobIDEQ(scope.JobID)).Select(jobround.FieldID, jobround.FieldManifestSealed, jobround.FieldSegmentTotal).Only(ctx)
		if err != nil {
			return false, err
		}
		if !r.ManifestSealed {
			return false, nil
		}
		if r.SegmentTotal != total {
			return false, ErrManifest
		}
		n, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).Count(ctx)
		if err != nil || n != total {
			if err == nil {
				err = ErrManifest
			}
			return false, err
		}
		err = walkManifestPages(total, walk, func(ids []int) error {
			count, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID), workitem.SegmentIDIn(ids...)).Count(ctx)
			if err == nil && count != len(ids) {
				err = ErrManifest
			}
			return err
		})
		return err == nil, err
	}
	ctx = operationContext(ctx)
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, scope, true); err != nil {
			return err
		}
		r, err := tx.JobRound.Get(ctx, scope.RoundID)
		if err != nil {
			return err
		}
		if r.ManifestSealed {
			_, err := verify(ctx, tx)
			return err
		}
		if total < 0 || r.SegmentTotal > 0 && r.SegmentTotal != total {
			return ErrManifest
		}
		err = walkManifestPages(total, walk, func(ids []int) error {
			valid, err := tx.Segment.Query().Where(segment.IDIn(ids...), segment.ResourceIDEQ(scope.ResourceID)).Count(ctx)
			if err != nil {
				return err
			}
			if valid != len(ids) {
				return ErrManifest
			}
			completed, err := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(scope.RoundID), jobroundsegment.SegmentIDIn(ids...)).Select(jobroundsegment.FieldSegmentID).All(ctx)
			if err != nil {
				return err
			}
			done := make(map[int]bool, len(completed))
			for _, row := range completed {
				done[row.SegmentID] = true
			}
			builders := make([]*ent.WorkItemCreate, 0, len(ids))
			for _, id := range ids {
				state := "pending"
				if done[id] {
					state = "resolved"
				}
				builders = append(builders, tx.WorkItem.Create().SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetSegmentID(id).SetRetryEpoch(scope.RetryEpoch).SetState(state))
			}
			return tx.WorkItem.CreateBulk(builders...).Exec(ctx)
		})
		if err != nil {
			return err
		}
		n, err := tx.JobRound.Update().Where(jobround.IDEQ(scope.RoundID), jobround.ManifestSealedEQ(false)).SetManifestVersion(1).SetManifestSealed(true).SetSegmentTotal(total).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrManifest
		}
		return Calibrate(ctx, tx, scope.JobID)
	})
	return s.finishUnknown(ctx, scope.JobID, err, verify)
}

// A manifest page contains only fixed-size IDs. Retain the whole-round
// transaction so neither dispatch nor recovery can observe a partial seal.
func walkManifestPages(total int, walk func(func(int) error) error, visit func([]int) error) error {
	var page [16]int
	used, seen := 0, 0
	err := walk(func(id int) error {
		if id < 1 || seen >= total {
			return ErrManifest
		}
		page[used] = id
		used++
		seen++
		if used == len(page) {
			if err := visit(page[:used]); err != nil {
				return err
			}
			used = 0
		}
		return nil
	})
	if err != nil {
		return err
	}
	if seen != total {
		return ErrManifest
	}
	if used > 0 {
		return visit(page[:used])
	}
	return nil
}
