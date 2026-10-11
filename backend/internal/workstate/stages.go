package workstate

import (
	"context"
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
	row, err := client.Job.Query().Where(job.IDEQ(jobID)).Select(job.FieldID, job.FieldStatus, job.FieldPauseRequested, job.FieldRetryEpoch).Only(ctx)
	if err != nil {
		return nil, err
	}
	confirmed, err := client.JobRoundSegment.Query().Where(jobroundsegment.HasJobRoundWith(jobround.JobIDEQ(jobID))).Count(ctx)
	if err != nil {
		return nil, err
	}
	counts := &StageCounts{ConfirmedWork: int64(confirmed), AsOf: timeutil.NowUTC()}
	var requests []struct {
		State string `json:"state"`
		Stage string `json:"stage"`
		Count int    `json:"count"`
	}
	err = client.WorkRequest.Query().Where(workrequest.JobIDEQ(jobID), workrequest.RetryEpochEQ(row.RetryEpoch), workrequest.StateIn("sent", "received", "unknown")).GroupBy(workrequest.FieldState, workrequest.FieldStage).Aggregate(ent.Count()).Scan(ctx, &requests)
	if err != nil {
		return nil, err
	}
	for _, request := range requests {
		if request.State == "unknown" {
			counts.UnknownRequests += request.Count
			continue
		}
		if request.State == "received" {
			counts.SavingRequests += request.Count
			continue
		}
		if request.Stage == "alignment" || request.Stage == "ruby_alignment" {
			counts.AlignmentRequests += request.Count
		} else {
			counts.MainRequests += request.Count
		}
	}
	var candidates []struct {
		State string `json:"state"`
		Count int    `json:"count"`
	}
	err = client.WorkCandidate.Query().Where(workcandidate.StateIn("pending_alignment", "ready_to_commit"), workcandidate.HasWorkItemWith(workitem.JobIDEQ(jobID))).GroupBy(workcandidate.FieldState).Aggregate(ent.Count()).Scan(ctx, &candidates)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.State == "pending_alignment" {
			counts.PendingAlignment = candidate.Count
		} else {
			counts.ReadyToCommit = candidate.Count
		}
	}
	if row.PauseRequested || row.Status == "pausing" {
		counts.DrainingRequests = counts.MainRequests + counts.AlignmentRequests + counts.SavingRequests
	}
	return counts, nil
}
