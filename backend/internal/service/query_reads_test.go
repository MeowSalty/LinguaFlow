package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/exportartifact"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

func TestResourceProgressPageQueriesAndStatusSemantics(t *testing.T) {
	c, _, recorder := jobQueryRecordingClient(t)
	ctx := context.Background()
	u := createTestUser(t, c, "resource-page")
	p := createTestProject(t, c, "resources", u.ID)
	s := NewResourceService(c, NewProjectService(c, NewUserService(c, nil)), nil)
	ids := make([]int, 0, 100)
	for i := range 100 {
		r := createTestResource(t, c, p.ID, fmt.Sprintf("%03d.txt", i))
		ids = append(ids, r.ID)
		if i == 0 {
			continue // An empty resource must still return zero counts.
		}
		for n, status := range []segment.Status{segment.StatusPending, segment.StatusTranslated, segment.StatusEdited, segment.StatusApproved} {
			// Pending with target text is excluded; translated with no target is
			// included. Large, invalid metadata must not be decoded by this read.
			q := c.Segment.Create().SetResourceID(r.ID).SetSegmentIndex(n).SetSourceText(strings.Repeat("source", 500)).SetStatus(status).SetMeta("not JSON")
			if status == segment.StatusPending {
				q.SetTargetText("pending draft")
			}
			q.SaveX(ctx)
		}
	}
	for _, limit := range []int{1, 100} {
		recorder.queries = nil
		rows, err := s.ListResources(ctx, u.ID, p.ID, ResourceListOptions{Limit: limit})
		if err != nil || len(rows) != limit {
			t.Fatalf("limit %d: rows=%d err=%v", limit, len(rows), err)
		}
		if len(recorder.queries) != 3 {
			t.Fatalf("limit %d: want project, resources, aggregate; got %v", limit, recorder.queries)
		}
		if rows[0].TranslatedSegments != 0 || rows[0].ApprovedSegments != 0 {
			t.Fatalf("empty resource: %+v", rows[0])
		}
		for _, row := range rows[1:] {
			if row.TranslatedSegments != 3 || row.ApprovedSegments != 1 {
				t.Fatalf("status-based count changed: %+v", row)
			}
		}
		q := recorder.queries[2]
		for _, field := range []string{"source_text", "target_text", "meta", "issues"} {
			if strings.Contains(q, field) {
				t.Errorf("progress reads content %q: %s", field, q)
			}
		}
		if !strings.Contains(q, "GROUP BY") || len(recorder.args[len(recorder.args)-1]) != limit {
			t.Fatalf("progress is not restricted to the page: %s args=%v", q, recorder.args[len(recorder.args)-1])
		}
	}
	recorder.queries = nil
	if _, err := s.GetResourceIdentity(ctx, u.ID, p.ID, ids[0]); err != nil || len(recorder.queries) != 2 {
		t.Fatalf("identity query must not read progress: queries=%v err=%v", recorder.queries, err)
	}
	recorder.queries = nil
	if _, err := s.ListResourcesProgress(ctx, nil); err != nil || len(recorder.queries) != 0 {
		t.Fatalf("empty IDs queried database: %v %v", recorder.queries, err)
	}
	for i := 0; len(ids) < 501; i++ {
		ids = append(ids, 10000+i)
	}
	ids = append(ids, ids[0])
	recorder.queries = nil
	progress, err := s.ListResourcesProgress(ctx, ids)
	if err != nil || len(progress) != 501 || len(recorder.queries) != 2 || progress[ids[500]].Translated != 0 {
		t.Fatalf("bounded aggregate/deduplicated missing IDs: len=%d queries=%d err=%v", len(progress), len(recorder.queries), err)
	}
}

func TestResourcePrecheckBatchPreservesInputOrder(t *testing.T) {
	c, _, recorder := jobQueryRecordingClient(t)
	ctx := context.Background()
	u := createTestUser(t, c, "precheck-page")
	p := createTestProject(t, c, "precheck", u.ID)
	r := createTestResource(t, c, p.ID, "dir/existing.txt")
	s := NewResourceService(c, NewProjectService(c, NewUserService(c, nil)), nil)
	recorder.queries = nil
	rows, err := s.PrecheckResources(ctx, u.ID, p.ID, []string{` dir\existing.txt `, "../invalid", "new.txt", "dir/existing.txt", "new.txt"})
	if err != nil || len(rows) != 5 || len(recorder.queries) != 2 {
		t.Fatalf("precheck rows=%+v queries=%v err=%v", rows, recorder.queries, err)
	}
	want := []string{"conflict", "duplicate", "create", "duplicate", "duplicate"}
	for i, action := range want {
		if rows[i].Action != action {
			t.Fatalf("item %d: %+v want %s", i, rows[i], action)
		}
	}
	if rows[0].ExistingResource == nil || rows[0].ExistingResource.ID != r.ID || rows[1].Path != "../invalid" {
		t.Fatalf("conflict/invalid path information changed: %+v", rows)
	}
}

func queryReadStorageBlob(t *testing.T, c *ent.Client, projectID int, identity string, active bool) *ent.Blob {
	t.Helper()
	ctx := context.Background()
	b := c.Blob.Create().SetProjectID(projectID).SetIdentity(identity).SetPurpose(blob.PurposeSource).SetStatus(blob.StatusReady).SetLocationGeneration(7).SaveX(ctx)
	if active {
		connection := c.StorageConnection.Create().SetName(identity).SetDriver("local").SaveX(ctx)
		space := c.StorageSpace.Create().SetConnectionID(connection.ID).SetName(identity).SetIdentity(identity).SetMarkerNonce("nonce").SaveX(ctx)
		loc := c.BlobLocation.Create().SetBlobID(b.ID).SetSpaceID(space.ID).SetObjectKey(identity).SetIntegrity(bloblocation.IntegrityAvailable).SaveX(ctx)
		b = c.Blob.UpdateOneID(b.ID).SetActiveLocationID(loc.ID).SaveX(ctx)
	}
	return b
}

func TestSourceVersionsAndExportsBatchDependencies(t *testing.T) {
	c, _, recorder := jobQueryRecordingClient(t)
	ctx := context.Background()
	u := createTestUser(t, c, "versions-page")
	p := createTestProject(t, c, "versions", u.ID)
	r := createTestResource(t, c, p.ID, "versions.txt")
	s := NewResourceService(c, NewProjectService(c, NewUserService(c, nil)), nil)
	ready := queryReadStorageBlob(t, c, p.ID, "ready", true)
	unknown := queryReadStorageBlob(t, c, p.ID, "unknown", false)
	var rev *ent.SourceRevision
	for i := range 30 {
		id := ready.ID
		if i == 0 {
			id = unknown.ID
		}
		rev = c.SourceRevision.Create().SetProjectID(p.ID).SetResourceID(r.ID).SetSourceBlobID(id).SetFormat("txt").SetParserVersion("test").SaveX(ctx)
	}
	recorder.queries = nil
	versions, err := s.Versions(ctx, u.ID, p.ID, r.ID)
	if err != nil || len(versions) != 30 || len(recorder.queries) != 5 {
		t.Fatalf("versions len=%d queries=%v err=%v", len(versions), recorder.queries, err)
	}
	if versions[0].ID != rev.ID || versions[0].Health != "available" || versions[0].LocationGeneration != 7 || versions[29].Health != "unknown" {
		t.Fatalf("version ordering/health changed: first=%+v last=%+v", versions[0], versions[29])
	}
	var expected = make(map[int]bool)
	for i := range 100 {
		q := c.ExportArtifact.Create().SetProjectID(p.ID).SetResourceID(r.ID).SetSourceRevisionID(rev.ID).SetFilename("result.txt").SetRendererVersion(storageRendererVersion)
		if i%5 != 4 {
			q.SetSnapshotBlobID(ready.ID)
		}
		switch i % 5 {
		case 1:
			q.SetSnapshotBlobID(unknown.ID)
		case 2:
			q.SetRendererVersion("old-renderer")
		case 3:
			q.SetStatus(exportartifact.StatusDeleted)
		}
		a := q.SaveX(ctx)
		expected[a.ID] = i%5 == 0 // The identical source/snapshot blob counts once.
	}
	recorder.queries = nil
	artifacts, err := s.ListExportsIncludingDeleted(ctx, u.ID, p.ID, r.ID, true)
	if err != nil || len(artifacts) != 100 || len(recorder.queries) != 5 {
		t.Fatalf("exports len=%d queries=%v err=%v", len(artifacts), recorder.queries, err)
	}
	for _, a := range artifacts {
		if a.Rebuildable != expected[a.ID] {
			t.Fatalf("artifact %d rebuildable=%v want=%v", a.ID, a.Rebuildable, expected[a.ID])
		}
	}
	c.SourceRevision.UpdateOneID(rev.ID).SetDeleted(true).ExecX(ctx)
	recorder.queries = nil
	artifacts, err = s.ListExportsIncludingDeleted(ctx, u.ID, p.ID, r.ID, true)
	if err != nil || len(recorder.queries) != 4 {
		t.Fatalf("unavailable revision must not read blobs: %v %v", recorder.queries, err)
	}
	for _, a := range artifacts {
		if a.Rebuildable {
			t.Fatalf("deleted revision still rebuildable: %d", a.ID)
		}
	}
}

func TestJobListProjectionAndEarlyAuthorization(t *testing.T) {
	c, _, recorder := jobQueryRecordingClient(t)
	ctx := context.Background()
	u := createTestUser(t, c, "job-card")
	other := createTestUser(t, c, "job-outsider")
	p := createTestProject(t, c, "job-card", u.ID)
	s := newJobRoundTestService(t, c, nil)
	j := seedQueryJob(t, c, p.ID, JobStatusRunning, "manual", time.Now().UTC())
	c.Job.UpdateOneID(j.ID).SetCreatedByID(u.ID).SetExecutionConfig(map[string]any{"large": strings.Repeat("large", 100)}).SetPipelineRuntimeState([]byte("private runtime")).ExecX(ctx)
	recorder.queries = nil
	rows, err := s.ListJobs(ctx, u.ID, p.ID, JobListOptions{})
	if err != nil || len(rows) != 1 || rows[0].Edges.CreatedBy == nil || rows[0].Edges.CreatedBy.Username != u.Username {
		t.Fatalf("list lost creator: %+v err=%v", rows, err)
	}
	for _, q := range recorder.queries {
		if strings.Contains(q, "execution_config") || strings.Contains(q, "pipeline_runtime_state") || strings.Contains(q, "password_hash") {
			t.Fatalf("list reads payload/private user fields: %s", q)
		}
	}
	recorder.queries = nil
	if _, err := s.GetJob(ctx, other.ID, j.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider detail: %v", err)
	}
	for _, q := range recorder.queries {
		if strings.Contains(q, "execution_config") || strings.Contains(q, "job_resources") || strings.Contains(q, "job_rounds") {
			t.Fatalf("denied detail read expensive/private payload: %s", q)
		}
	}
}

func TestTaskHistoryPageAdvisoryRechecksWritesAndBusy(t *testing.T) {
	c, _, recorder := jobQueryRecordingClient(t)
	f := historyFixtureWithClient(t, c)
	ctx := context.Background()
	rows := make([]HistoryDeleteCandidate, 0, 100)
	for range 100 {
		j := f.job(t, JobStatusCompleted, 0)
		rows = append(rows, HistoryDeleteCandidate{Kind: OperationTranslation, ID: j.ID, ProjectID: j.ProjectID, Status: j.Status})
	}
	recorder.queries = nil
	observed := f.s.PrepareDeleteObservation(ctx, f.owner, rows)
	for _, row := range rows {
		if !f.s.CanDelete(observed, f.owner, row.Kind, row.ID, row.ProjectID, row.Status) {
			t.Fatalf("owner cannot delete terminal row: %+v", row)
		}
	}
	if len(recorder.queries) != 1 {
		t.Fatalf("advisory project queries scale with page: %v", recorder.queries)
	}
	first := rows[0]
	guard, err := f.s.lifecycle.Lock(ctx, first.Kind, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	release := guard.Claim()
	guard.Release()
	if f.s.CanDelete(observed, f.owner, first.Kind, first.ID, first.ProjectID, first.Status) {
		t.Fatal("request observation cached away current busy state")
	}
	release()
	other := createTestUser(t, c, "advisory-new-owner")
	c.Project.UpdateOneID(f.p).SetOwnerUserID(other.ID).ExecX(ctx)
	fresh := f.s.PrepareDeleteObservation(ctx, f.owner, rows)
	if f.s.CanDelete(fresh, f.owner, first.Kind, first.ID, first.ProjectID, first.Status) {
		t.Fatal("next request retained revoked owner permission")
	}
	if err := f.s.Delete(observed, f.owner, HistoryTarget{Kind: first.Kind, ID: fmt.Sprint(first.ID), ProjectID: first.ProjectID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("write trusted advisory permission: %v", err)
	}
}

func TestJobStagePageAuthorizationAndFixedQueryCount(t *testing.T) {
	c, _, recorder := jobQueryRecordingClient(t)
	ctx := context.Background()
	u := createTestUser(t, c, "stage-page")
	p := createTestProject(t, c, "stages", u.ID)
	s := newJobRoundTestService(t, c, nil)
	ids := make([]int, 0, 100)
	for range 100 {
		j := seedQueryJob(t, c, p.ID, JobStatusRunning, "manual", time.Now().UTC())
		ids = append(ids, j.ID)
	}
	for _, size := range []int{1, 100} {
		recorder.queries = nil
		counts, err := s.GetJobStageCountsBatch(ctx, u.ID, ids[:size])
		if err != nil || len(counts) != size || len(recorder.queries) != 2 {
			t.Fatalf("page=%d counts=%d queries=%v err=%v", size, len(counts), recorder.queries, err)
		}
	}
	org := c.Organization.Create().SetName("stages").SetSlug("stages").SaveX(ctx)
	m := c.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(u.ID).SetRole(OrgRoleMember).SaveX(ctx)
	team := c.Project.Create().SetName("team").SetOwnerOrgID(org.ID).SaveX(ctx)
	j := seedQueryJob(t, c, team.ID, JobStatusRunning, "manual", time.Now().UTC())
	if counts, err := s.GetJobStageCountsBatch(ctx, u.ID, []int{j.ID}); err != nil || counts[j.ID] == nil {
		t.Fatalf("member lost read access: %v %v", counts, err)
	}
	c.OrgMembership.DeleteOneID(m.ID).ExecX(ctx)
	counts, err := s.GetJobStageCountsBatch(ctx, u.ID, []int{j.ID})
	if err != nil || len(counts) != 0 {
		t.Fatalf("revoked member kept stage access: %v %v", counts, err)
	}
}

func TestTaskHistoryPageOrganizationRolesAndOwnerPrecedence(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	actor := createTestUser(t, f.c, "advisory-team")
	org := f.c.Organization.Create().SetName("advisory").SetSlug("advisory").SaveX(ctx)
	m := f.c.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(actor.ID).SetRole(OrgRoleMember).SaveX(ctx)
	team := f.c.Project.Create().SetName("team").SetOwnerOrgID(org.ID).SaveX(ctx)
	dual := f.c.Project.Create().SetName("dual").SetOwnerUserID(f.owner).SetOwnerOrgID(org.ID).SaveX(ctx)
	rows := []HistoryDeleteCandidate{{Kind: OperationTranslation, ID: 9001, ProjectID: team.ID, Status: JobStatusCompleted}, {Kind: OperationTranslation, ID: 9002, ProjectID: dual.ID, Status: JobStatusCompleted}}
	for _, role := range []string{OrgRoleMember, OrgRoleAdmin, OrgRoleOwner} {
		f.c.OrgMembership.UpdateOneID(m.ID).SetRole(role).ExecX(ctx)
		observed := f.s.PrepareDeleteObservation(ctx, actor.ID, rows)
		if got := f.s.CanDelete(observed, actor.ID, rows[0].Kind, rows[0].ID, team.ID, rows[0].Status); got != (role != OrgRoleMember) {
			t.Fatalf("role %s: can_delete=%v", role, got)
		}
		if f.s.CanDelete(observed, actor.ID, rows[1].Kind, rows[1].ID, dual.ID, rows[1].Status) {
			t.Fatalf("role %s overrode personal owner", role)
		}
	}
	f.c.OrgMembership.DeleteOneID(m.ID).ExecX(ctx)
	observed := f.s.PrepareDeleteObservation(ctx, actor.ID, rows)
	if f.s.CanDelete(observed, actor.ID, rows[0].Kind, rows[0].ID, team.ID, rows[0].Status) {
		t.Fatal("revoked member kept delete permission on next request")
	}
}
