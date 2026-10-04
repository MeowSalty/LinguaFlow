package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type sourcePlanChange struct {
	Kind   SegmentChangeType `json:"kind"`
	OldID  int               `json:"old_id,omitempty"`
	Index  int               `json:"index"`
	Source string            `json:"source,omitempty"`
	Meta   map[string]any    `json:"meta,omitempty"`
}
type sourcePlan struct {
	Version               int                    `json:"version"`
	ParserVersion         string                 `json:"parser_version"`
	DiffVersion           string                 `json:"diff_version"`
	RevisionID            *int                   `json:"source_revision_id"`
	SourceGeneration      int64                  `json:"source_generation"`
	TranslationGeneration int64                  `json:"translation_generation"`
	WriteID               int                    `json:"write_id"`
	SHA256                string                 `json:"sha256"`
	Size                  int64                  `json:"size"`
	Stats                 IncrementalUpdateStats `json:"stats"`
	BaselineTrust         string                 `json:"baseline_trust"`
	Changes               []sourcePlanChange     `json:"changes"`
}
type legacySegmentSnapshot struct {
	ID            int               `json:"id"`
	Index         int               `json:"index"`
	Source        string            `json:"source"`
	Target        *string           `json:"target"`
	Status        string            `json:"status"`
	Meta          *string           `json:"metadata"`
	ReviewComment *string           `json:"review_comment"`
	ReviewerID    *int              `json:"reviewer_id"`
	QualityIssues []qa.QualityIssue `json:"quality_issues"`
}
type legacySourceSnapshot struct {
	Version               int                     `json:"version"`
	ProjectID             int                     `json:"project_id"`
	ResourceID            int                     `json:"resource_id"`
	SourceRevisionID      *int                    `json:"source_revision_id"`
	SourceGeneration      int64                   `json:"source_generation"`
	TranslationGeneration int64                   `json:"translation_generation"`
	CreatedAt             time.Time               `json:"created_at"`
	Segments              []legacySegmentSnapshot `json:"segments"`
}

func decodeSourcePlan(t *ent.StorageTask) (*sourcePlan, error) {
	var p sourcePlan
	if len(t.SourcePlan) == 0 || json.Unmarshal(t.SourcePlan, &p) != nil || p.Version != 1 || p.DiffVersion != "unique-text-v2" {
		return nil, ErrSourceRevisionConflict
	}
	return &p, nil
}

func (s *ResourceService) resumeSourcePlan(ctx context.Context, t *ent.StorageTask) error {
	if t.ResourceID == nil {
		return ErrInvalidInput
	}
	res, e := s.client.Resource.Get(ctx, *t.ResourceID)
	if e != nil {
		return e
	}
	items, e := s.readPreparedSource(ctx, t, res.Format)
	if e != nil {
		return e
	}
	return s.prepareSourcePlan(ctx, t, items)
}

// prepareSourcePlan is shared by the synchronous preview and content endpoint.
// No prepared confirmation exists until this transaction saves its entire plan.
func (s *ResourceService) prepareSourcePlan(ctx context.Context, t *ent.StorageTask, items []parsedResourceSegment) error {
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, e := tx.StorageTask.Get(ctx, t.ID)
		if e != nil {
			return e
		}
		if len(current.SourcePlan) > 0 {
			return nil
		}
		if e = storageTaskGate(ctx, tx, current); e != nil {
			return e
		}
		if current.ResourceID == nil {
			return ErrInvalidInput
		}
		if e = storageProjectGate(ctx, tx, current.ProjectID, current.ExpectedStorageGeneration); e != nil {
			return e
		}
		n, e := tx.Resource.Update().Where(resource.IDEQ(*current.ResourceID), resource.SourceGenerationEQ(current.ExpectedSourceGeneration), resource.TranslationGenerationEQ(current.ExpectedTranslationGeneration)).SetSourceGeneration(current.ExpectedSourceGeneration).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrSourceRevisionConflict
		}
		res, e := tx.Resource.Get(ctx, *current.ResourceID)
		if e != nil {
			return e
		}
		w, e := tx.StorageWrite.Query().Where(storagewrite.TaskIDEQ(t.ID), storagewrite.PhaseEQ("prepared")).Only(ctx)
		if e != nil {
			return e
		}
		old, e := tx.Segment.Query().Where(segment.ResourceIDEQ(res.ID)).Order(ent.Asc(segment.FieldSegmentIndex)).WithReviewedBy().All(ctx)
		if e != nil {
			return e
		}
		if len(old) > s.storage.cfg.Limits.MaxSegments {
			return ErrStorageTooLarge
		}
		trusted := false
		if res.CurrentSourceRevisionID != nil {
			rev, e := tx.SourceRevision.Get(ctx, *res.CurrentSourceRevisionID)
			if e != nil {
				return e
			}
			trusted = rev.VerificationState == sourcerevision.VerificationStateVerified
		}
		changes := diffSegments(old, items)
		if !trusted {
			changes = make([]SegmentChange, 0, len(old)+len(items))
			for _, row := range old {
				changes = append(changes, SegmentChange{ChangeType: SegmentChangeDeleted, OldSegment: row})
			}
			for _, row := range items {
				changes = append(changes, SegmentChange{ChangeType: SegmentChangeAdded, NewIndex: row.Index, NewSource: row.SourceText, NewMeta: row.Meta})
			}
		}
		plan := sourcePlan{Version: 1, ParserVersion: storageParserVersion, DiffVersion: "unique-text-v2", RevisionID: res.CurrentSourceRevisionID, SourceGeneration: res.SourceGeneration, TranslationGeneration: res.TranslationGeneration, WriteID: w.ID, SHA256: w.Sha256, Size: w.ActualBytes, Stats: *changeStats(changes)}
		plan.BaselineTrust = "legacy_unverified"
		if trusted {
			plan.BaselineTrust = "verified"
		}
		for _, c := range changes {
			p := sourcePlanChange{Kind: c.ChangeType, Index: c.NewIndex, Source: c.NewSource, Meta: c.NewMeta}
			if c.OldSegment != nil {
				p.OldID = c.OldSegment.ID
			}
			plan.Changes = append(plan.Changes, p)
		}
		encoded, e := json.Marshal(plan)
		if e != nil {
			return e
		}
		if int64(len(encoded)) > s.storage.cfg.Limits.MaxMetadataBytes {
			return ErrStorageTooLarge
		}
		update := tx.StorageTask.UpdateOneID(t.ID).SetSourcePlan(encoded).SetPhase("prepared").SetStatus(storagetask.StatusRunning)
		if res.CurrentSourceRevisionID != nil {
			update.SetSourceRevisionID(*res.CurrentSourceRevisionID)
		}
		if !trusted {
			snapshot := legacySourceSnapshot{Version: 1, ProjectID: current.ProjectID, ResourceID: res.ID, SourceRevisionID: res.CurrentSourceRevisionID, SourceGeneration: res.SourceGeneration, TranslationGeneration: res.TranslationGeneration, CreatedAt: time.Now().UTC()}
			for _, row := range old {
				part := legacySegmentSnapshot{ID: row.ID, Index: row.SegmentIndex, Source: row.SourceText, Target: row.TargetText, Status: string(row.Status), Meta: row.Meta, ReviewComment: row.ReviewComment, QualityIssues: row.QualityIssues}
				if row.Edges.ReviewedBy != nil {
					id := row.Edges.ReviewedBy.ID
					part.ReviewerID = &id
				}
				snapshot.Segments = append(snapshot.Segments, part)
			}
			data, e := json.Marshal(snapshot)
			if e != nil {
				return e
			}
			if int64(len(data))+int64(len(encoded)) > s.storage.cfg.Limits.MaxMetadataBytes {
				return ErrStorageTooLarge
			}
			update.SetLegacySnapshot(data).SetNillableLegacySnapshotExpiresAt(current.Deadline)
		}
		return update.Exec(ctx)
	})
}

func (s *ResourceService) SourcePreviewForTask(ctx context.Context, actor, projectID, taskID int) (*SourceUpdatePreview, error) {
	t, e := s.storage.task(ctx, actor, projectID, taskID)
	if e != nil {
		return nil, e
	}
	if t.Kind != "source_update" {
		return nil, ErrInvalidInput
	}
	p, e := decodeSourcePlan(t)
	if e != nil {
		return nil, e
	}
	available, until, e := legacySnapshotLifetime(ctx, s.client, t)
	if e != nil {
		return nil, e
	}
	return &SourceUpdatePreview{TaskID: t.ID, SourceGeneration: p.SourceGeneration, TranslationGeneration: p.TranslationGeneration, Stats: p.Stats, ExpiresAt: t.Deadline, BaselineTrust: p.BaselineTrust, LegacySnapshotAvailable: available, LegacySnapshotExpiresAt: until}, nil
}

func (s *ResourceService) LegacySnapshot(ctx context.Context, actor, projectID, taskID int) (json.RawMessage, error) {
	t, e := s.storage.task(ctx, actor, projectID, taskID)
	if e != nil {
		return nil, e
	}
	available, _, e := legacySnapshotLifetime(ctx, s.client, t)
	if e != nil {
		return nil, e
	}
	if t.Kind != "source_update" || !available {
		return nil, ErrResourceNotFound
	}
	return append(json.RawMessage(nil), t.LegacySnapshot...), nil
}

// A committed legacy backup lives at least as long as its retained old source
// revision, including pins and other consumers. Nil means no current deadline
// can be promised, not that the content has already expired.
func legacySnapshotLifetime(ctx context.Context, client *ent.Client, t *ent.StorageTask) (bool, *time.Time, error) {
	if len(t.LegacySnapshot) == 0 {
		return false, nil, nil
	}
	until := t.LegacySnapshotExpiresAt
	if t.Phase != "committed" && t.Deadline != nil {
		until = t.Deadline
	}
	if until == nil || until.After(time.Now().UTC()) {
		return true, until, nil
	}
	if t.Phase == "committed" && t.SourceRevisionID != nil {
		rev, err := client.SourceRevision.Get(ctx, *t.SourceRevisionID)
		if ent.IsNotFound(err) {
			return false, until, nil
		}
		if err != nil {
			return false, nil, err
		}
		if !rev.Deleted {
			if rev.RetainUntil != nil && rev.RetainUntil.After(time.Now().UTC()) {
				return true, rev.RetainUntil, nil
			}
			return true, nil, nil
		}
	}
	return false, until, nil
}
