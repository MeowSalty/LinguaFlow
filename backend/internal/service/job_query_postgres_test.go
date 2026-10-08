package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

func TestPostgresJobTimeBoundariesAndSessionExpiry(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 3, MaxIdleConns: 2}
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	unlock, err := database.AcquireMigrationLock(ctx, db, config.DatabaseDriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
	err = client.Schema.Create(ctx)
	unlockErr := unlock()
	if err != nil {
		t.Fatal(err)
	}
	if unlockErr != nil {
		t.Fatal(unlockErr)
	}
	suffix := fmt.Sprintf("pg-time-%d", time.Now().UnixNano())
	actor := createTestUser(t, client, suffix)
	defer func() {
		_, _ = client.RefreshToken.Delete().Where(refreshtoken.HasUserWith(user.IDEQ(actor.ID))).Exec(context.Background())
		_ = client.User.DeleteOneID(actor.ID).Exec(context.Background())
	}()
	project := createTestProject(t, client, suffix, actor.ID)
	defer func() {
		_, _ = client.Job.Delete().Where(job.ProjectIDEQ(project.ID)).Exec(context.Background())
		_ = client.Project.DeleteOneID(project.ID).Exec(context.Background())
	}()
	svc := newJobRoundTestService(t, client, nil)
	base := time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC)
	older := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base)
	tie := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base.In(time.FixedZone("east", 8*60*60)))
	newer := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base.Add(time.Microsecond))
	var cursor string
	for _, want := range []int{newer.ID, tie.ID, older.ID} {
		page, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{State: "all", Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		assertQueryJobIDs(t, page, want)
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatal("last page has continuation")
	}
	between := base.Add(time.Nanosecond).In(time.FixedZone("west", -7*60*60))
	before, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{State: "all", UpdatedBefore: &between})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, before, tie.ID, older.ID)
	after, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{State: "all", UpdatedFrom: &between})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, after, newer.ID)
	summary, err := svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{}, between)
	if err != nil || summary.RecentFailed != 2 || !summary.AsOf.Equal(between) {
		t.Fatalf("fractional upper summary=%+v, err=%v", summary, err)
	}
	summary, err = svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{}, between.Add(7*24*time.Hour))
	if err != nil || summary.RecentFailed != 1 {
		t.Fatalf("fractional lower summary=%+v, err=%v", summary, err)
	}
	invalidCursor, err := encodeAccessibleJobCursor(&ent.Job{ID: newer.ID, UpdatedAt: between})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Cursor: invalidCursor}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid precision error=%v", err)
	}
	assertSessionExpiryMatchesCredentials(t, client, actor)
}
