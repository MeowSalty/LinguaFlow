package service

import (
	"context"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// resetWorkEpoch only touches unfinished members in resources actually reset
// by RetryJob. Resume and startup recovery never call it.
func resetWorkEpoch(ctx context.Context, tx *ent.Client, jobID int, epoch int64) error {
	var sizes []struct {
		Bytes int64 `json:"bytes"`
	}
	if err := tx.WorkItem.Query().Where(workitem.JobIDEQ(jobID), workitem.StateNEQ("resolved")).Aggregate(func(s *sql.Selector) string {
		parts := make([]string, 2)
		for i, f := range []string{workitem.FieldCandidateID, workitem.FieldResponseRequestID} {
			c := s.C(f)
			if s.Dialect() == dialect.Postgres {
				parts[i] = "COALESCE(octet_length(" + c + "),0)"
			} else {
				parts[i] = "COALESCE(length(CAST(" + c + " AS BLOB)),0)"
			}
		}
		return "COALESCE(MAX(" + parts[0] + "+" + parts[1] + "),0) AS bytes"
	}).Scan(ctx, &sizes); err != nil {
		return err
	}
	if len(sizes) != 1 || sizes[0].Bytes > 256 {
		return fmt.Errorf("retry member reference exceeds fixed identity bound")
	}
	after := 0
	for {
		rows, err := tx.WorkItem.Query().Where(workitem.JobIDEQ(jobID), workitem.IDGT(after), workitem.StateNEQ("resolved"), workitem.HasRoundWith(jobround.StatusEQ(JobRoundStatusPending), jobround.HasJobResourceWith(jobresource.StatusEQ(JobResourceStatusPending)))).Order(ent.Asc(workitem.FieldID)).Limit(128).Select(workitem.FieldID, workitem.FieldJobID, workitem.FieldJobRoundID, workitem.FieldResourceID, workitem.FieldSegmentID, workitem.FieldCandidateID, workitem.FieldResponseRequestID).All(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			after = row.ID
			state := "pending"
			pendingResponse, err := workstate.HasPendingResponse(ctx, tx, row)
			if err != nil {
				return err
			}
			if pendingResponse {
				state = "response_pending"
			} else if row.CandidateID != "" {
				valid, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(row.CandidateID), workcandidate.StateIn("pending_alignment", "ready_to_commit")).Exist(ctx)
				if err != nil {
					return err
				}
				if valid {
					state = "candidate"
				}
			}
			if err := tx.WorkItem.UpdateOneID(row.ID).SetRetryEpoch(epoch).SetState(state).SetPoolIndex(0).SetMainAttempts(0).SetAlignmentAttempts(0).SetNetworkAttempts(0).SetMainNetworkAttempts(0).SetAlignmentNetworkAttempts(0).SetPromptPhase("initial").ClearCursor().ClearNextAttemptAt().SetLastError("").Exec(ctx); err != nil {
				return err
			}
		}
		if len(rows) < 128 {
			break
		}
	}
	_, err := tx.JobRound.Update().Where(jobround.JobIDEQ(jobID), jobround.StatusEQ(JobRoundStatusPending), jobround.HasJobResourceWith(jobresource.StatusEQ(JobResourceStatusPending))).SetPoolIndex(0).Save(ctx)
	return err
}
