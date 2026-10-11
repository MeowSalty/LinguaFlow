package workstate

import "testing"

func TestRecoveryHeadersAndResourcePriorityAreBounded(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	first := ready(scope, seg)
	if err := s.SaveCandidate(ctx, first); err != nil {
		t.Fatal(err)
	}
	j := c.Job.GetX(ctx, scope.JobID)
	r := c.Resource.Create().SetProjectID(j.ProjectID).SetPath("next.txt").SetStoragePath("next.txt").SetFormat("txt").SaveX(ctx)
	jr := c.JobResource.Create().SetJobID(j.ID).SetResourceID(r.ID).SetStatus("running").SaveX(ctx)
	round := c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("translate").SetStatus("running").SaveX(ctx)
	a := c.Segment.Create().SetResourceID(r.ID).SetSegmentIndex(0).SetSourceText("a").SaveX(ctx)
	b := c.Segment.Create().SetResourceID(r.ID).SetSegmentIndex(1).SetSourceText("b").SaveX(ctx)
	next := Scope{JobID: j.ID, ResourceID: r.ID, JobResourceID: jr.ID, RoundID: round.ID}
	if _, err := s.SealRound(ctx, next, []int{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	for i, seg := range []int{a.ID, b.ID} {
		candidate := ready(next, c.Segment.GetX(ctx, seg))
		candidate.ID = []string{"second", "third"}[i]
		if err := s.SaveCandidate(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}
	var after, count int
	for {
		headers, cursor, err := s.LoadCandidateHeaders(ctx, j.ID, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(headers) == 0 {
			break
		}
		if len(headers) != 1 || cursor <= after {
			t.Fatal("unbounded or non-advancing header page")
		}
		header := headers[0]
		if header.PayloadBytes != int64(len(first.Payload)) || header.SnapshotDigest != "frozen" || header.Scope.JobID != j.ID || header.DTOVersion != 1 {
			t.Fatalf("header=%+v", header)
		}
		after = cursor
		count++
	}
	if count != 3 {
		t.Fatalf("candidate count=%d", count)
	}
	ids, err := s.ResourcesWithCandidates(ctx, j.ID, 0, 1)
	if err != nil || len(ids) != 1 || ids[0] != scope.ResourceID {
		t.Fatalf("first resource page=%v %v", ids, err)
	}
	ids, err = s.ResourcesWithCandidates(ctx, j.ID, ids[0], 2)
	if err != nil || len(ids) != 1 || ids[0] != r.ID {
		t.Fatalf("deduplicated resource page=%v %v", ids, err)
	}
	if _, err := s.Commit(ctx, input(first)); err != nil {
		t.Fatal(err)
	}
	ids, err = s.ResourcesWithCandidates(ctx, j.ID, 0, 2)
	if err != nil || len(ids) != 1 || ids[0] != r.ID {
		t.Fatal("completed candidate still participates in recovery window")
	}
}
