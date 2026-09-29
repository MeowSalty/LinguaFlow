package service

import (
	"context"
	stdsql "database/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

type StatsService struct {
	client   *ent.Client
	projects *ProjectService
}

type UsageStats struct {
	APICalls      int
	InputTokens   int
	OutputTokens  int
	SegmentCount  int
	UsageRecords  int
	CompletedJobs int
	FailedJobs    int
}

func NewStatsService(client *ent.Client, projects *ProjectService) *StatsService {
	return &StatsService{client: client, projects: projects}
}

func readableUsagePredicate(actorUserID int) predicate.UsageRecord {
	return usagerecord.Or(
		usagerecord.And(usagerecord.VisibilityScopeEQ(usagerecord.VisibilityScopeProject),
			usagerecord.HasProjectWith(readableProjectPredicate(actorUserID))),
		usagerecord.And(usagerecord.VisibilityScopeEQ(usagerecord.VisibilityScopeOrganization),
			usagerecord.Not(usagerecord.HasProject()),
			usagerecord.HasOrganizationWith(readableOrganizationPredicate(actorUserID))),
		usagerecord.And(usagerecord.VisibilityScopeEQ(usagerecord.VisibilityScopePersonal),
			usagerecord.Not(usagerecord.HasProject()), usagerecord.Not(usagerecord.HasOrganization()),
			usagerecord.HasUserWith(user.IDEQ(actorUserID))),
	)
}

func (s *StatsService) Summary(ctx context.Context, actorUserID int) (*UsageStats, error) {
	var sums []struct {
		Count        int
		APICalls     stdsql.NullInt64 `json:"api_calls"`
		InputTokens  stdsql.NullInt64 `json:"input_tokens"`
		OutputTokens stdsql.NullInt64 `json:"output_tokens"`
		SegmentCount stdsql.NullInt64 `json:"segment_count"`
	}
	err := s.client.UsageRecord.Query().Where(readableUsagePredicate(actorUserID)).
		Aggregate(ent.Count(), ent.As(ent.Sum(usagerecord.FieldAPICalls), "api_calls"), ent.As(ent.Sum(usagerecord.FieldInputTokens), "input_tokens"),
			ent.As(ent.Sum(usagerecord.FieldOutputTokens), "output_tokens"), ent.As(ent.Sum(usagerecord.FieldSegmentCount), "segment_count")).Scan(ctx, &sums)
	if err != nil {
		return nil, err
	}
	stats := &UsageStats{}
	if len(sums) > 0 {
		stats.UsageRecords = sums[0].Count
		stats.APICalls = int(sums[0].APICalls.Int64)
		stats.InputTokens = int(sums[0].InputTokens.Int64)
		stats.OutputTokens = int(sums[0].OutputTokens.Int64)
		stats.SegmentCount = int(sums[0].SegmentCount.Int64)
	}
	var counts []struct {
		Status string
		Count  int
	}
	err = s.client.Job.Query().Where(job.HasProjectWith(readableProjectPredicate(actorUserID)),
		job.StatusIn(JobStatusCompleted, JobStatusFailed)).
		GroupBy(job.FieldStatus).Aggregate(ent.Count()).Scan(ctx, &counts)
	if err != nil {
		return nil, err
	}
	for _, row := range counts {
		switch row.Status {
		case JobStatusCompleted:
			stats.CompletedJobs = row.Count
		case JobStatusFailed:
			stats.FailedJobs = row.Count
		}
	}
	return stats, nil
}
