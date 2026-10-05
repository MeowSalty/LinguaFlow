package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/previewtoken"
)

func TestTranslationGenerationAtomicity(t *testing.T) {
	ctx := context.Background()
	_, client, actor, projectID := newApplyFixture(t)
	res := createTestResource(t, client, projectID, "generation.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "source", "old", segment.StatusTranslated, nil)
	injected := errors.New("injected failure after result write")
	err := WithResourceTranslation(ctx, client, res.ID, 0, func(tx *ent.Client) error {
		if err := tx.Segment.UpdateOneID(seg.ID).SetTargetText("partial").Exec(ctx); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if got := client.Resource.GetX(ctx, res.ID).TranslationGeneration; got != 0 {
		t.Fatalf("rolled back generation=%d", got)
	}
	if got := *client.Segment.GetX(ctx, seg.ID).TargetText; got != "old" {
		t.Fatalf("rolled back target=%q", got)
	}
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, time.Hour, nil)
	target := "edited"
	if _, err := svc.UpdateResourceSegment(ctx, actor, projectID, res.ID, seg.ID, ResourceSegmentUpdateInput{TargetText: &target}); err != nil {
		t.Fatal(err)
	}
	if got := client.Resource.GetX(ctx, res.ID).TranslationGeneration; got != 1 {
		t.Fatalf("committed generation=%d", got)
	}
	client.Resource.UpdateOneID(res.ID).SetSourceGeneration(1).ExecX(ctx)
	called := false
	err = WithResourceTranslation(ctx, client, res.ID, 0, func(*ent.Client) error { called = true; return nil })
	if !errors.Is(err, ErrSourceRevisionConflict) || called {
		t.Fatalf("stale write invoked=%v err=%v", called, err)
	}
}

func TestPreviewRejectsSourceMutationAndChangedGeneration(t *testing.T) {
	ctx := context.Background()
	svc, client, actor, projectID := newApplyFixture(t)
	res := createTestResource(t, client, projectID, "preview.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "source", "old", segment.StatusTranslated, nil)
	claims := revisionApplyClaims(seg, res.ID, projectID, actor, nil, previewtoken.QAConfigClaims{})
	claims.PreviewSource = "legacy changed source"
	claims.SourceHash = sha256Hex(claims.PreviewSource)
	if _, err := svc.ApplyPreview(ctx, actor, projectID, res.ID, seg.ID, encodeApplyClaims(t, claims), "new"); !errors.Is(err, ErrSourceReadOnly) {
		t.Fatalf("legacy token: %v", err)
	}
	claims = revisionApplyClaims(seg, res.ID, projectID, actor, nil, previewtoken.QAConfigClaims{})
	client.Resource.UpdateOneID(res.ID).SetSourceGeneration(1).ExecX(ctx)
	if _, err := svc.ApplyPreview(ctx, actor, projectID, res.ID, seg.ID, encodeApplyClaims(t, claims), "new"); !errors.Is(err, ErrSourceRevisionConflict) {
		t.Fatalf("stale token: %v", err)
	}
	if got := client.Segment.GetX(ctx, seg.ID); got.SourceText != "source" || *got.TargetText != "old" {
		t.Fatal("rejected preview changed source or target")
	}
	for _, input := range []PreviewInput{{SourceTextSet: true}, {SourceText: "source"}} {
		if _, err := svc.RunPreview(ctx, input); !errors.Is(err, ErrSourceReadOnly) {
			t.Fatalf("source override: %v", err)
		}
	}
}

func TestUndoRejectsChangedSourceGeneration(t *testing.T) {
	ctx := context.Background()
	_, client, actor, projectID := newApplyFixture(t)
	res := createTestResource(t, client, projectID, "undo.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "source", "old", segment.StatusTranslated, nil)
	svc := NewSegmentService(client, NewProjectService(client, nil), dialect.SQLite, time.Hour, nil)
	result, err := svc.ApplySearchReplace(ctx, actor, projectID, res.ID, SearchReplaceOptions{Find: "old", ReplaceWith: "new"})
	if err != nil {
		t.Fatal(err)
	}
	client.Resource.UpdateOneID(res.ID).SetSourceGeneration(1).ExecX(ctx)
	if _, err := svc.UndoSearchReplace(ctx, actor, projectID, res.ID, result.OperationID); !errors.Is(err, ErrSourceRevisionConflict) {
		t.Fatalf("undo: %v", err)
	}
	if got := *client.Segment.GetX(ctx, seg.ID).TargetText; got != "new" {
		t.Fatalf("stale undo wrote %q", got)
	}
}

func TestJobResumeRetryAndRecoveryRejectChangedSource(t *testing.T) {
	for _, status := range []string{JobStatusFailed, JobStatusCancelled, JobStatusPaused, JobStatusRunning} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := testClient(t)
			svc := newJobRoundTestService(t, client, nil)
			actor := createTestUser(t, client, "generation-job")
			project := createTestProject(t, client, "generation-job", actor.ID)
			resourceStatus := status
			if status == JobStatusPaused {
				resourceStatus = JobResourceStatusRunning
			}
			job, items := seedJobCancelRetry(t, client, project.ID, status, []string{resourceStatus})
			res := items[0].QueryResource().OnlyX(ctx)
			client.Resource.UpdateOneID(res.ID).SetSourceGeneration(1).ExecX(ctx)
			var err error
			switch status {
			case JobStatusPaused:
				_, err = svc.ResumeJob(ctx, actor.ID, job.ID)
			case JobStatusRunning:
				ids, recoverErr := svc.RecoverPendingJobs(ctx)
				if recoverErr != nil || len(ids) != 0 {
					t.Fatalf("recovery ids=%v err=%v", ids, recoverErr)
				}
				if client.Job.GetX(ctx, job.ID).Status != JobStatusFailed {
					t.Fatal("stale job was recovered")
				}
				return
			default:
				_, err = svc.RetryJob(ctx, actor.ID, job.ID)
			}
			if !errors.Is(err, ErrSourceRevisionConflict) {
				t.Fatalf("control error=%v", err)
			}
			if client.Job.GetX(ctx, job.ID).Status != status || client.JobResource.GetX(ctx, items[0].ID).Status != resourceStatus {
				t.Fatal("rejected control partially reset task")
			}
		})
	}
}
