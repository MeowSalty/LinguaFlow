package service

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialjobreference"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sseevent"
)

type RetentionPreview struct {
	PolicyRevision    int64                            `json:"policy_revision"`
	RetentionDays     int                              `json:"retention_days"`
	AsOf              time.Time                        `json:"as_of"`
	Cutoff            time.Time                        `json:"cutoff"`
	Partial           bool                             `json:"partial"`
	IncompleteReasons []string                         `json:"incomplete_reasons"`
	ByType            map[string]*RetentionTypePreview `json:"by_type"`
}

type RetentionTypePreview struct {
	Active                *int64                    `json:"active"`
	Terminal              *int64                    `json:"terminal"`
	MissingAnchor         *int64                    `json:"missing_anchor"`
	NotExpired            *int64                    `json:"not_expired"`
	Expired               RetentionExpiredCounts    `json:"expired"`
	TimingSources         RetentionTimingCounts     `json:"timing_sources"`
	TerminalAge           RetentionAgeCounts        `json:"terminal_age"`
	DeletableDependencies RetentionDependencyCounts `json:"deletable_dependencies"`
}
type RetentionExpiredCounts struct {
	Blocked   *int64 `json:"blocked"`
	Busy      *int64 `json:"busy"`
	Deletable *int64 `json:"deletable"`
}
type RetentionTimingCounts struct {
	FinishedAt    *int64 `json:"finished_at"`
	LegacyAnchor  *int64 `json:"legacy_anchor"`
	MissingAnchor *int64 `json:"missing_anchor"`
}
type RetentionAgeCounts struct {
	LT1D       *int64 `json:"lt_1d"`
	Days1To6   *int64 `json:"days_1_6"`
	Days7To29  *int64 `json:"days_7_29"`
	Days30To89 *int64 `json:"days_30_89"`
	Days90Plus *int64 `json:"days_90_plus"`
	Unknown    *int64 `json:"unknown"`
}
type RetentionDependencyCounts struct {
	SSEEvents               *int64 `json:"sse_events"`
	JobResources            *int64 `json:"job_resources"`
	JobRounds               *int64 `json:"job_rounds"`
	JobRoundSegments        *int64 `json:"job_round_segments"`
	CredentialJobReferences *int64 `json:"credential_job_references"`
}

func retentionCount(value int64) *int64 { return &value }

func (s *TaskHistoryService) Preview(parent context.Context, days int) (RetentionPreview, error) {
	if days < 1 || days > 3650 {
		return RetentionPreview{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(parent, s.previewBudget)
	defer cancel()
	policy, err := readTaskRetention(ctx, s.client)
	if err != nil {
		return RetentionPreview{}, err
	}
	now := s.now().UTC()
	out := RetentionPreview{PolicyRevision: policy.Revision, RetentionDays: days, AsOf: now, Cutoff: now.Add(-time.Duration(days) * 24 * time.Hour),
		IncompleteReasons: []string{}, ByType: map[string]*RetentionTypePreview{OperationTranslation: {}, OperationGlossarySync: {}}}
	tx, err := s.client.BeginTx(ctx, &stdsql.TxOptions{ReadOnly: true, Isolation: stdsql.LevelSerializable})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	used := 0
	for _, kind := range []string{OperationTranslation, OperationGlossarySync} {
		stats := out.ByType[kind]
		if err = previewBaseCounts(ctx, tx.Client(), kind, now, out.Cutoff, stats); err != nil {
			break
		}
	}
	if err == nil {
		for _, kind := range []string{OperationTranslation, OperationGlossarySync} {
			if err = s.previewEligibility(ctx, tx.Client(), kind, out.Cutoff, out.ByType[kind], &used); err != nil {
				break
			}
		}
	}
	if err != nil {
		if parent.Err() != nil {
			return RetentionPreview{}, parent.Err()
		}
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errPreviewLimit) {
			out.Partial = true
			out.IncompleteReasons = append(out.IncompleteReasons, "evaluation_budget_exhausted")
			return out, nil
		}
		return RetentionPreview{}, err
	}
	// A read-only rollback releases the snapshot without ever producing a write.
	if err = tx.Rollback(); err != nil && !errors.Is(err, stdsql.ErrTxDone) {
		return RetentionPreview{}, err
	}
	return out, nil
}

func previewBaseCounts(ctx context.Context, client *ent.Client, kind string, now, cutoff time.Time, out *RetentionTypePreview) error {
	table := "jobs"
	if kind == OperationGlossarySync {
		table = "sync_tasks"
	}
	terminal := "status IN ('completed','failed','cancelled')"
	conditions := []string{
		"NOT (" + terminal + ")", terminal,
		terminal + " AND retention_anchor_at IS NULL",
		terminal + " AND retention_anchor_at > $1",
		terminal + " AND finished_at IS NOT NULL",
		terminal + " AND finished_at IS NULL AND retention_anchor_at IS NOT NULL",
		terminal + " AND retention_anchor_at > $2",
		terminal + " AND retention_anchor_at <= $2 AND retention_anchor_at > $3",
		terminal + " AND retention_anchor_at <= $3 AND retention_anchor_at > $4",
		terminal + " AND retention_anchor_at <= $4 AND retention_anchor_at > $5",
		terminal + " AND retention_anchor_at <= $5",
	}
	query := "SELECT "
	for i, condition := range conditions {
		if i > 0 {
			query += ","
		}
		query += "COALESCE(SUM(CASE WHEN " + condition + " THEN 1 ELSE 0 END),0)"
	}
	query += " FROM " + table
	rows, err := client.QueryContext(ctx, query, cutoff, now.Add(-24*time.Hour), now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour), now.Add(-90*24*time.Hour))
	if err != nil {
		return err
	}
	defer rows.Close()
	var counts [11]int64
	dest := make([]any, len(counts))
	for i := range counts {
		dest[i] = &counts[i]
	}
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return fmt.Errorf("task retention count did not return a row")
	}
	if err = rows.Scan(dest...); err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	out.Active = retentionCount(counts[0])
	out.Terminal = retentionCount(counts[1])
	out.MissingAnchor = retentionCount(counts[2])
	out.NotExpired = retentionCount(counts[3])
	out.TimingSources = RetentionTimingCounts{retentionCount(counts[4]), retentionCount(counts[5]), retentionCount(counts[2])}
	out.TerminalAge = RetentionAgeCounts{retentionCount(counts[6]), retentionCount(counts[7]), retentionCount(counts[8]), retentionCount(counts[9]), retentionCount(counts[10]), retentionCount(counts[2])}
	return nil
}

var errPreviewLimit = errors.New("task retention preview candidate limit")

func (s *TaskHistoryService) previewEligibility(ctx context.Context, client *ent.Client, kind string, cutoff time.Time, out *RetentionTypePreview, used *int) error {
	var blocked, busy, deletable int64
	var deps [5]int64
	for _, status := range retentionStatuses {
		cursor := retentionCursor{}
		for {
			rows, err := queryRetentionCandidates(ctx, client, kind, status, cutoff, 0, cursor, 100)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			ids := make([]int, 0, len(rows))
			for _, row := range rows {
				if *used >= s.previewLimit {
					return errPreviewLimit
				}
				*used++
				cursor = retentionCursor{Anchor: *row.Anchor, ID: row.ID}
				p, err := client.Project.Get(ctx, row.ProjectID)
				if err != nil {
					return err
				}
				switch {
				case s.blocked() || p.StorageState != "active":
					blocked++
				case s.lifecycle.Busy(kind, row.ID):
					busy++
				default:
					deletable++
					ids = append(ids, row.ID)
				}
			}
			if kind == OperationTranslation && len(ids) > 0 {
				counts, err := countHistoryDependencies(ctx, client, ids)
				if err != nil {
					return err
				}
				for i, n := range counts {
					deps[i] += int64(n)
				}
			}
		}
	}
	out.Expired = RetentionExpiredCounts{retentionCount(blocked), retentionCount(busy), retentionCount(deletable)}
	out.DeletableDependencies = RetentionDependencyCounts{retentionCount(deps[0]), retentionCount(deps[1]), retentionCount(deps[2]), retentionCount(deps[3]), retentionCount(deps[4])}
	return nil
}

func countHistoryDependencies(ctx context.Context, client *ent.Client, ids []int) ([5]int, error) {
	var counts [5]int
	var err error
	counts[0], err = client.SSEEvent.Query().Where(sseevent.JobIDIn(ids...)).Count(ctx)
	if err != nil {
		return counts, err
	}
	counts[1], err = client.JobResource.Query().Where(jobresource.HasJobWith(job.IDIn(ids...))).Count(ctx)
	if err != nil {
		return counts, err
	}
	counts[2], err = client.JobRound.Query().Where(jobround.JobIDIn(ids...)).Count(ctx)
	if err != nil {
		return counts, err
	}
	counts[3], err = client.JobRoundSegment.Query().Where(jobroundsegment.HasJobRoundWith(jobround.JobIDIn(ids...))).Count(ctx)
	if err != nil {
		return counts, err
	}
	counts[4], err = client.CredentialJobReference.Query().Where(credentialjobreference.JobIDIn(ids...)).Count(ctx)
	return counts, err
}
