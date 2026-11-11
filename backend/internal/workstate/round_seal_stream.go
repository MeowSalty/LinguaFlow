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
		err = walk(func(id int) error {
			ok, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID), workitem.SegmentIDEQ(id)).Exist(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return ErrManifest
			}
			return nil
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
		builders := make([]*ent.WorkItemCreate, 0, 16)
		seen := 0
		flush := func() error {
			if len(builders) == 0 {
				return nil
			}
			err := tx.WorkItem.CreateBulk(builders...).Exec(ctx)
			builders = builders[:0]
			return err
		}
		err = walk(func(id int) error {
			if id < 1 {
				return ErrManifest
			}
			valid, err := tx.Segment.Query().Where(segment.IDEQ(id), segment.ResourceIDEQ(scope.ResourceID)).Exist(ctx)
			if err != nil {
				return err
			}
			if !valid {
				return ErrManifest
			}
			done, err := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(scope.RoundID), jobroundsegment.SegmentIDEQ(id)).Exist(ctx)
			if err != nil {
				return err
			}
			state := "pending"
			if done {
				state = "resolved"
			}
			builders = append(builders, tx.WorkItem.Create().SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetSegmentID(id).SetRetryEpoch(scope.RetryEpoch).SetState(state))
			seen++
			if len(builders) == 16 {
				return flush()
			}
			return nil
		})
		if err != nil {
			return err
		}
		if seen != total {
			return ErrManifest
		}
		if err = flush(); err != nil {
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
