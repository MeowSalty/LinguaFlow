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
	var facts []struct {
		RetryEpoch int64 `json:"retry_epoch"`
		Draining   bool  `json:"draining"`
	}
	err := client.Job.Query().Where(job.IDEQ(jobID)).Modify(func(s *sql.Selector) {
		s.Select(s.C(job.FieldRetryEpoch), sql.As("CASE WHEN "+s.C(job.FieldPauseRequested)+" OR "+s.C(job.FieldStatus)+"='pausing' THEN TRUE ELSE FALSE END", "draining"))
	}).Scan(ctx, &facts)
	if err != nil {
		return nil, err
	}
	if len(facts) != 1 {
		return nil, ErrManifest
	}
	row := facts[0]
	confirmed, err := client.JobRoundSegment.Query().Where(jobroundsegment.HasJobRoundWith(jobround.JobIDEQ(jobID))).Count(ctx)
	if err != nil {
		return nil, err
	}
	counts := &StageCounts{ConfirmedWork: int64(confirmed), AsOf: timeutil.NowUTC()}
	requests := client.WorkRequest.Query().Where(workrequest.JobIDEQ(jobID), workrequest.RetryEpochEQ(row.RetryEpoch))
	counts.UnknownRequests, err = requests.Clone().Where(workrequest.StateEQ("unknown")).Count(ctx)
	if err != nil {
		return nil, err
	}
	counts.SavingRequests, err = requests.Clone().Where(workrequest.StateEQ("received")).Count(ctx)
	if err != nil {
		return nil, err
	}
	counts.AlignmentRequests, err = requests.Clone().Where(workrequest.StateEQ("sent"), workrequest.StageIn("alignment", "ruby_alignment")).Count(ctx)
	if err != nil {
		return nil, err
	}
	counts.MainRequests, err = requests.Clone().Where(workrequest.StateEQ("sent"), workrequest.StageNotIn("alignment", "ruby_alignment")).Count(ctx)
	if err != nil {
		return nil, err
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
	if row.Draining {
		counts.DrainingRequests = counts.MainRequests + counts.AlignmentRequests + counts.SavingRequests
	}
	return counts, nil
}
