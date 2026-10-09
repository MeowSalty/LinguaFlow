package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestJobLocalWriteRejectsCancellationBeforeContextNotification(t *testing.T) {
	base := context.Background()
	client := newRegistryTestClient(t)
	jobID, jr, _ := registryFixture(t, client)
	item, err := client.JobResource.Query().Where(jobresource.IDEQ(jr)).WithResource().Only(base)
	if err != nil {
		t.Fatal(err)
	}
	res := item.Edges.Resource
	seg, err := client.Segment.Create().SetResourceID(res.ID).SetSourceText("source").SetSegmentIndex(0).Save(base)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(base, jobWriteScopeKey{}, jobWriteScope{jobID: jobID})
	if err := client.Job.UpdateOneID(jobID).SetStatus("cancelled").Exec(base); err != nil {
		t.Fatal(err)
	}
	err = withJobResourceTranslation(ctx, client, res.ID, res.SourceGeneration, func(tx *ent.Client) error { return tx.Segment.UpdateOneID(seg.ID).SetTargetText("late").Exec(ctx) })
	if !errors.Is(err, workstate.ErrStopped) {
		t.Fatalf("late callback=%v", err)
	}
	row, err := client.Segment.Get(base, seg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TargetText != nil || row.ContentVersion != seg.ContentVersion {
		t.Fatal("cancelled callback changed segment")
	}
	resource, err := client.Resource.Get(base, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resource.TranslationGeneration != res.TranslationGeneration {
		t.Fatal("rejected callback changed generation")
	}
}
