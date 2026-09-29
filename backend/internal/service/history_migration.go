package service

import (
	"context"
	"encoding/json"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
)

// MigrateHistoryVisibility runs after schema creation, before serving requests.
// Only legacy rows are considered: unknown rows are permanently quarantined and
// cannot be accidentally claimed when an unrelated target is created later.
func MigrateHistoryVisibility(ctx context.Context, client *ent.Client) error {
	for {
		done := false
		err := withOrganizationTransaction(ctx, client, func(tx *ent.Client) error {
			// The no-match UPDATE obtains SQLite's write lock before any reads, without
			// altering a timestamp or requiring a dedicated migration marker table.
			if _, err := tx.ActivityLog.Update().Where(activitylog.IDEQ(-1)).
				SetVisibilityScope(activitylog.VisibilityScopeLegacy).Save(ctx); err != nil {
				return err
			}
			activities, err := tx.ActivityLog.Query().Where(activitylog.VisibilityScopeEQ(activitylog.VisibilityScopeLegacy)).
				Order(ent.Asc(activitylog.FieldID)).Limit(200).WithActor().WithProject().WithOrganization().All(ctx)
			if err != nil {
				return err
			}
			usages, err := tx.UsageRecord.Query().Where(usagerecord.VisibilityScopeEQ(usagerecord.VisibilityScopeLegacy)).
				Order(ent.Asc(usagerecord.FieldID)).Limit(200).WithProject().All(ctx)
			if err != nil {
				return err
			}
			done = len(activities) == 0 && len(usages) == 0
			for _, row := range activities {
				if err := migrateActivityVisibility(ctx, tx, row); err != nil {
					return err
				}
			}
			for _, row := range usages {
				update := tx.UsageRecord.UpdateOneID(row.ID).
					SetVisibilityScope(usagerecord.VisibilityScopeUnknown).SetUpdatedAt(row.UpdatedAt)
				if row.Edges.Project != nil && validHistoricalProject(row.Edges.Project) &&
					(row.Source == "job" || row.Source == "preview" || row.Source == "quick_translate") {
					update.SetVisibilityScope(usagerecord.VisibilityScopeProject).ClearOrganization()
					if orgID := EffectiveProjectOrgID(row.Edges.Project); orgID != nil {
						update.SetOrganizationID(*orgID)
					}
				}
				if err := update.Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func validHistoricalProject(row *ent.Project) bool {
	return row != nil && ((row.OwnerUserID != nil && *row.OwnerUserID > 0) || EffectiveProjectOrgID(row) != nil)
}

func migrateActivityVisibility(ctx context.Context, client *ent.Client, row *ent.ActivityLog) error {
	update := client.ActivityLog.UpdateOneID(row.ID).
		SetVisibilityScope(activitylog.VisibilityScopeUnknown).SetUpdatedAt(row.UpdatedAt)
	if !knownHistoricalActivity(row.Action) {
		return update.Exec(ctx)
	}
	projectRow := row.Edges.Project
	if projectRow == nil {
		var err error
		projectRow, err = recoverActivityProject(ctx, client, row)
		if err != nil {
			return err
		}
	}
	if validHistoricalProject(projectRow) {
		update.SetProjectID(projectRow.ID).ClearOrganization().SetVisibilityScope(activitylog.VisibilityScopeProject)
		if orgID := EffectiveProjectOrgID(projectRow); orgID != nil {
			update.SetOrganizationID(*orgID)
		}
	} else if row.Action == "quick_translate" && row.ResourceType == "quick_translate" &&
		row.Edges.Project == nil && row.Edges.Organization == nil && row.Edges.Actor != nil {
		// This historical writer always wrote metadata.project_id when project-bound.
		if _, hasProject := row.Metadata["project_id"]; !hasProject {
			update.SetVisibilityScope(activitylog.VisibilityScopePersonal)
		}
	}
	return update.Exec(ctx)
}

func knownHistoricalActivity(action string) bool {
	switch action {
	case "quick_translate", "job.create", "job.cancel", "job.retry", "job.pause", "job.resume",
		"resource.segment.update", "resource.segment.translation_preview.apply", "resource.segment.revision_preview.apply",
		"segment.approve", "segment.reject", "segment.issue_disposition", "segment.batch_review",
		"segment.approve_all", "segment.retranslate_rejected", "segment.search_replace", "segment.search_replace_undo",
		"qa.recheck", "glossary.sync_execute":
		return true
	}
	return false
}

func recoverActivityProject(ctx context.Context, client *ent.Client, row *ent.ActivityLog) (*ent.Project, error) {
	var query *ent.ProjectQuery
	id := 0
	if row.ResourceID != nil {
		id = *row.ResourceID
	}
	switch row.Action {
	case "quick_translate":
		if row.ResourceType != "quick_translate" {
			return nil, nil
		}
		id = historicalPositiveID(row.Metadata["project_id"])
		if id > 0 {
			query = client.Project.Query().Where(project.IDEQ(id))
		}
	case "job.create", "job.cancel", "job.retry", "job.pause", "job.resume":
		if row.ResourceType == "job" && id > 0 {
			query = client.Job.Query().Where(job.IDEQ(id)).QueryProject()
		}
	case "resource.segment.update", "segment.approve", "segment.reject", "segment.issue_disposition":
		if row.ResourceType == "segment" && id > 0 {
			query = client.Segment.Query().Where(segment.IDEQ(id)).QueryResource().QueryProject()
		}
	case "segment.batch_review", "segment.approve_all", "segment.retranslate_rejected", "segment.search_replace", "segment.search_replace_undo":
		if row.ResourceType == "resource" && id > 0 {
			query = client.Resource.Query().Where(resource.IDEQ(id)).QueryProject()
		}
	case "qa.recheck":
		if row.ResourceType == "project" && id > 0 {
			query = client.Project.Query().Where(project.IDEQ(id))
		}
	}
	if query == nil {
		return nil, nil
	}
	result, err := query.Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return result, err
}

func historicalPositiveID(value any) int {
	switch v := value.(type) {
	case int:
		if v > 0 {
			return v
		}
	case float64:
		if v > 0 && v < float64(1<<53) && v == float64(int(v)) {
			return int(v)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 && int64(int(n)) == n {
			return int(n)
		}
	}
	return 0
}
