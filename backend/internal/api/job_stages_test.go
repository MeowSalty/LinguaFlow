package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestRouterJobPausingAndStageCounts(t *testing.T) {
	s, client, user := jobQueryTestServer(t)
	project := jobQueryProject(t, client, user.ID)
	row := jobQuerySeed(t, client, project.ID, "pausing", "manual", time.Now())
	ctx := context.Background()
	client.Job.UpdateOneID(row.ID).SetPauseRequested(true).ExecX(ctx)
	resource := client.Resource.Create().SetProjectID(project.ID).SetPath("stages.txt").SetFormat("txt").SetStoragePath("stages.txt").SaveX(ctx)
	jobResource := client.JobResource.Create().SetJobID(row.ID).SetResourceID(resource.ID).SetStatus("running").SaveX(ctx)
	round := client.JobRound.Create().SetJobID(row.ID).SetJobResourceID(jobResource.ID).SetRoundIndex(0).SetMode("translate").SetStatus("running").SaveX(ctx)
	seg := client.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("source").SaveX(ctx)
	client.JobRoundSegment.Create().SetJobRoundID(round.ID).SetSegmentID(seg.ID).ExecX(ctx)
	client.WorkRequest.Create().SetIdentity("saving-response").SetJobID(row.ID).SetResourceID(resource.ID).SetJobRoundID(round.ID).
		SetRetryEpoch(row.RetryEpoch).SetSegmentIds([]int{}).SetStage("main").SetBackendID(1).SetBudgetModel("stage_separated").SetInputDigest("frozen").SetState("received").SaveX(ctx)
	router := s.newRouter()
	token := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, query := range []string{"", "?state=active", "?status=pausing"} {
		list := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs"+query, token))
		if len(list.Items) != 1 || list.Items[0].Id != row.ID || list.Items[0].Status != JobSummaryStatusPausing {
			t.Fatalf("pausing missing from %s: %+v", query, list)
		}
	}
	summary := jobQueryDecode[JobsSummaryResponse](t, jobQueryRequest(router, "/api/v1/jobs/summary", token))
	if summary.Pausing != 1 || summary.Running != 0 || summary.Paused != 0 {
		t.Fatalf("pausing summary bucket is wrong: %+v", summary)
	}
	detail := jobQueryDecode[Job](t, jobQueryRequest(router, "/api/v1/jobs/"+strconv.Itoa(row.ID), token))
	if detail.Status != JobStatusPausing || detail.Progress.Stages == nil || detail.Progress.Stages.ConfirmedWork != 1 || detail.Progress.Stages.SavingRequests != 1 || detail.Progress.Stages.DrainingRequests != 1 || detail.Progress.Stages.ReadyToCommit != 0 || detail.Progress.Stages.AsOf.IsZero() {
		t.Fatalf("missing persisted stage diagnostics: %+v", detail.Progress)
	}
	other := client.User.Create().SetUsername("stage-other").SetEmail("stage-other@example.test").SetPasswordHash("test").SaveX(context.Background())
	otherToken := authTestToken(t, other, time.Now().Add(time.Hour), []byte(authTestSecret))
	w := jobQueryRequest(router, "/api/v1/jobs/"+strconv.Itoa(row.ID), otherToken)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("stage diagnostics escaped project permissions: %d %s", w.Code, w.Body.String())
	}
}
