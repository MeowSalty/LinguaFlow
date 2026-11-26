package workstate

import (
	"context"
	"entgo.io/ent/dialect/sql"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// StageCounts is an observation of persisted state, not an admission counter.
// PendingAlignment includes drafts with an active alignment request. A logical
// confirmed work unit is one persisted segment checkpoint in one round. Closed
// round progress may also include unresolved work, so it cannot serve as evidence.
type StageCounts struct {
	MainRequests      int       `json:"main_requests"`
	PendingAlignment  int       `json:"pending_alignment"`
	AlignmentRequests int       `json:"alignment_requests"`
	SavingRequests    int       `json:"saving_requests"`
	ReadyToCommit     int       `json:"ready_to_commit"`
	ConfirmedWork     int64     `json:"confirmed_work"`
	UnknownRequests   int       `json:"unknown_requests"`
	DrainingRequests  int       `json:"draining_requests"`
	AsOf              time.Time `json:"as_of"`
}

func ReadStageCounts(ctx context.Context, client *ent.Client, jobID int) (*StageCounts, error) {
	all, err := ReadStageCountsBatch(ctx, client, []int{jobID})
	if err != nil {
		return nil, err
	}
	if all[jobID] == nil {
		return nil, ErrManifest
	}
	return all[jobID], nil
}

// ReadStageCountsBatch returns only existing jobs. Authorization belongs to the
// caller. Every aggregate in a page shares one SQL statement snapshot, including
// sealed/unsealed checkpoints and each job's current request retry epoch.
func ReadStageCountsBatch(ctx context.Context, client *ent.Client, jobIDs []int) (map[int]*StageCounts, error) {
	out := make(map[int]*StageCounts, len(jobIDs))
	if len(jobIDs) == 0 {
		return out, nil
	}
	asOf := timeutil.NowUTC()
	for start := 0; start < len(jobIDs); start += bulkIDs {
		ids := jobIDs[start:min(start+bulkIDs, len(jobIDs))]
		var rows []struct {
			ID       int  `json:"id"`
			Draining bool `json:"draining"`
			StageCounts
		}
		err := client.Job.Query().Where(job.IDIn(ids...)).Modify(selectStageCounts(ids)).Scan(ctx, &rows)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			counts := row.StageCounts
			counts.AsOf = asOf
			if row.Draining {
				counts.DrainingRequests = counts.MainRequests + counts.AlignmentRequests + counts.SavingRequests
			}
			out[row.ID] = &counts
		}
	}
	return out, nil
}

func selectStageCounts(ids []int) func(*sql.Selector) {
	return func(s *sql.Selector) {
		d := sql.Dialect(s.Dialect())
		request := sql.Table(workrequest.Table).As("stage_request")
		currentJob := sql.Table(job.Table).As("stage_request_job")
		state, stage := request.C(workrequest.FieldState), request.C(workrequest.FieldStage)
		requests := d.Select(request.C(workrequest.FieldJobID), request.C(workrequest.FieldRetryEpoch),
			sql.As("SUM(CASE WHEN "+state+"='unknown' THEN 1 ELSE 0 END)", "unknown_requests"),
			sql.As("SUM(CASE WHEN "+state+"='received' THEN 1 ELSE 0 END)", "saving_requests"),
			sql.As("SUM(CASE WHEN "+state+"='sent' AND "+stage+" IN ('alignment','ruby_alignment') THEN 1 ELSE 0 END)", "alignment_requests"),
			sql.As("SUM(CASE WHEN "+state+"='sent' AND "+stage+" NOT IN ('alignment','ruby_alignment') THEN 1 ELSE 0 END)", "main_requests"),
		).From(request).Join(currentJob).On(request.C(workrequest.FieldJobID), currentJob.C(job.FieldID)).OnP(sql.ColumnsEQ(request.C(workrequest.FieldRetryEpoch), currentJob.C(job.FieldRetryEpoch))).Where(sql.And(sql.InInts(currentJob.C(job.FieldID), ids...), sql.In(request.C(workrequest.FieldState), "sent", "received", "unknown"))).GroupBy(request.C(workrequest.FieldJobID), request.C(workrequest.FieldRetryEpoch)).As("stage_requests")
		item := sql.Table(workitem.Table).As("stage_item")
		candidate := sql.Table(workcandidate.Table).As("stage_candidate")
		// Keep the live-state index as the candidate scan root. A freely
		// reordered join can start with every historical WorkItem in a job.
		// Correlated primary-key ownership checks retain the original live-set
		// complexity while adding the job ID needed by the batch aggregate.
		owner, _ := d.Select(item.C(workitem.FieldJobID)).From(item).Where(sql.ColumnsEQ(item.C(workitem.FieldID), candidate.C(workcandidate.FieldWorkItemID))).Query()
		ownership := d.Select(item.C(workitem.FieldID)).From(item).Where(sql.And(sql.ColumnsEQ(item.C(workitem.FieldID), candidate.C(workcandidate.FieldWorkItemID)), sql.InInts(item.C(workitem.FieldJobID), ids...)))
		candidates := d.Select(sql.As("("+owner+")", workitem.FieldJobID),
			sql.As("SUM(CASE WHEN "+candidate.C(workcandidate.FieldState)+"='pending_alignment' THEN 1 ELSE 0 END)", "pending_alignment"),
			sql.As("SUM(CASE WHEN "+candidate.C(workcandidate.FieldState)+"='ready_to_commit' THEN 1 ELSE 0 END)", "ready_to_commit"),
		).From(candidate).Where(sql.And(sql.In(candidate.C(workcandidate.FieldState), "pending_alignment", "ready_to_commit"), sql.Exists(ownership))).GroupBy("(" + owner + ")").As("stage_candidates")
		round := sql.Table(jobround.Table).As("stage_round")
		checkpoint := sql.Table(jobroundsegment.Table).As("stage_checkpoint")
		checkpoints, _ := d.Select(sql.Count("*")).From(checkpoint).Where(sql.ColumnsEQ(checkpoint.C(jobroundsegment.FieldJobRoundID), round.C(jobround.FieldID))).Query()
		confirmed := d.Select(round.C(jobround.FieldJobID), sql.As("SUM(CASE WHEN "+round.C(jobround.FieldManifestSealed)+" THEN "+round.C(jobround.FieldSegmentCompleted)+" ELSE ("+checkpoints+") END)", "confirmed_work")).From(round).Where(sql.InInts(round.C(jobround.FieldJobID), ids...)).GroupBy(round.C(jobround.FieldJobID)).As("stage_confirmed")
		s.LeftJoin(requests).On(s.C(job.FieldID), requests.C(workrequest.FieldJobID)).OnP(sql.ColumnsEQ(s.C(job.FieldRetryEpoch), requests.C(workrequest.FieldRetryEpoch)))
		s.LeftJoin(candidates).On(s.C(job.FieldID), candidates.C(workitem.FieldJobID))
		s.LeftJoin(confirmed).On(s.C(job.FieldID), confirmed.C(jobround.FieldJobID))
		s.Select(s.C(job.FieldID), sql.As("CASE WHEN "+s.C(job.FieldPauseRequested)+" OR "+s.C(job.FieldStatus)+"='pausing' THEN TRUE ELSE FALSE END", "draining"))
		for _, name := range []string{"unknown_requests", "saving_requests", "alignment_requests", "main_requests"} {
			s.AppendSelect(sql.As("COALESCE("+requests.C(name)+",0)", name))
		}
		for _, name := range []string{"pending_alignment", "ready_to_commit"} {
			s.AppendSelect(sql.As("COALESCE("+candidates.C(name)+",0)", name))
		}
		s.AppendSelect(sql.As("COALESCE("+confirmed.C("confirmed_work")+",0)", "confirmed_work"))
	}
}
