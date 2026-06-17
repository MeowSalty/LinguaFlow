package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationjob"
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

func (s *StatsService) Summary(ctx context.Context, actorUserID int) (*UsageStats, error) {
	rows, err := s.client.UsageRecord.Query().
		Where(usagerecord.Or(
			usagerecord.HasUserWith(user.IDEQ(actorUserID)),
			usagerecord.HasOrganizationWith(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actorUserID)))),
			usagerecord.HasProjectWith(project.Or(
				project.OwnerUserIDEQ(actorUserID),
				project.HasOwnerOrgWith(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actorUserID)))),
			)),
		)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	stats := &UsageStats{UsageRecords: len(rows)}
	for _, row := range rows {
		stats.APICalls += row.APICalls
		stats.InputTokens += row.InputTokens
		stats.OutputTokens += row.OutputTokens
		stats.SegmentCount += row.SegmentCount
	}
	stats.CompletedJobs, err = s.client.TranslationJob.Query().
		Where(
			translationjob.StatusEQ(TranslationJobStatusCompleted),
			translationjob.HasProjectWith(project.Or(
				project.OwnerUserIDEQ(actorUserID),
				project.HasOwnerOrgWith(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actorUserID)))),
			)),
		).
		Count(ctx)
	if err != nil {
		return nil, err
	}
	stats.FailedJobs, err = s.client.TranslationJob.Query().
		Where(
			translationjob.StatusEQ(TranslationJobStatusFailed),
			translationjob.HasProjectWith(project.Or(
				project.OwnerUserIDEQ(actorUserID),
				project.HasOwnerOrgWith(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actorUserID)))),
			)),
		).
		Count(ctx)
	if err != nil {
		return nil, err
	}
	return stats, nil
}
