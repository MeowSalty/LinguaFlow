package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

func TestRouterJobPageKeepsStageAndDetailResponse(t *testing.T) {
	s, c, actor := jobQueryTestServer(t)
	ctx := context.Background()
	s.taskHistory = service.NewTaskHistoryService(c, s.projectSvc, &tasklife.Coordinator{}, nil)
	s.taskHistory.SetReady(true)
	p := jobQueryProject(t, c, actor.ID)
	j := jobQuerySeed(t, c, p.ID, "completed", "manual", time.Now())
	c.Job.UpdateOneID(j.ID).SetCreatedByID(actor.ID).ExecX(ctx)
	r := c.Resource.Create().SetProjectID(p.ID).SetPath("page.txt").SetFormat("txt").SetStoragePath("page.txt").SaveX(ctx)
	jr := c.JobResource.Create().SetJobID(j.ID).SetResourceID(r.ID).SetSegmentCount(9).SetCompletedSegments(4).SetWorkWeight(9).SetSegmentIds([]int{1, 2, 3}).SetStatus("completed").SaveX(ctx)
	c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("translate").SetStatus("completed").SetSegmentCompleted(3).SetManifestSealed(true).SaveX(ctx)
	round := c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(1).SetMode("review").SetStatus("completed").SaveX(ctx)
	seg := c.Segment.Create().SetResourceID(r.ID).SetSegmentIndex(0).SetSourceText("source").SaveX(ctx)
	c.JobRoundSegment.Create().SetJobRoundID(round.ID).SetSegmentID(seg.ID).ExecX(ctx)
	token := authTestToken(t, actor, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	page := jobQueryDecode[struct {
		Items []jobResponse `json:"items"`
	}](t, jobQueryRequest(router, fmt.Sprintf("/api/v1/projects/%d/jobs", p.ID), token))
	if len(page.Items) != 1 {
		t.Fatalf("job page: %+v", page)
	}
	item := page.Items[0]
	if item.Progress.Stages == nil || item.Progress.Stages.ConfirmedWork != 4 || !item.CanDelete || item.CreatedBy == nil || item.CreatedBy.Username != actor.Username || item.ExecutionConfig != nil {
		t.Fatalf("page lost stage/creator/advisory contract: %+v", item)
	}
	detail := jobQueryDecode[jobResponse](t, jobQueryRequest(router, fmt.Sprintf("/api/v1/jobs/%d", j.ID), token))
	if len(detail.JobResources) != 1 || detail.ExecutionConfig == nil {
		t.Fatalf("detail missing configuration/resource matrix: %+v", detail)
	}
	resource := detail.JobResources[0]
	if resource.ResourceID != r.ID || resource.Resource == nil || resource.Resource.Path != r.Path || resource.SegmentCount != 9 || resource.CompletedSegments != 4 || len(resource.Rounds) != 2 || resource.Rounds[0].RoundIndex != 0 || resource.Rounds[1].RoundIndex != 1 {
		t.Fatalf("narrow hydration changed resource/round response: %+v", resource)
	}
}
