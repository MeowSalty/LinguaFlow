package pipeline

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestRuntimeReporterSavingObservesCheckpointSQLAndLifetime(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "reporter.db"))+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close(); _ = db.Close() })
	ctx := context.Background()
	if err = client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	project := client.Project.Create().SetName("runtime-reporter").SaveX(ctx)
	resource := client.Resource.Create().SetProjectID(project.ID).SetPath("source.txt").SetStoragePath("source.txt").SetFormat("txt").SaveX(ctx)
	job := client.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetStatus("running").SaveX(ctx)
	jr := client.JobResource.Create().SetJobID(job.ID).SetResourceID(resource.ID).SetStatus("running").SaveX(ctx)
	round := client.JobRound.Create().SetJobID(job.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("extract").SetStatus("running").SaveX(ctx)
	segment := client.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("source").SaveX(ctx)
	scope := workstate.Scope{JobID: job.ID, ResourceID: resource.ID, JobResourceID: jr.ID, RoundID: round.ID}
	if _, err = workstate.NewStore(client).SealRound(ctx, scope, []int{segment.ID}); err != nil {
		t.Fatal(err)
	}
	runtime := testExecutionRuntime(t, 1)
	t.Cleanup(runtime.Close)
	worker, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.Lifetime = worker
	defaults := runtime.Observer.(*persistence.Collector)
	observed := &runtimeMetadataObserver{}
	runtime.Observer = persistence.Fanout(runtime.Observer, observed)
	saver := runtime.PersistenceSaver()
	if runtime.Saver == nil {
		t.Fatal("producer-ready scheduler not initialized")
	}
	// The Reporter lifetime has no observer. This is the JobRunner shape that
	// previously bypassed Runtime observers when given the raw scheduler.
	reporter := progress.NewDBReporter(progress.DBReporterOptions{Client: client, JobID: job.ID, JobResourceID: jr.ID, ResourceID: resource.ID, Lifetime: worker, Saver: saver, Ticker: time.Hour})
	defer reporter.Close()
	reporter.SwitchRound(round.ID, func(index int) (int, bool) { return segment.ID, index == 0 })
	reporter.StageStart("extract", 1)
	if err = reporter.StageError(); err != nil {
		t.Fatal(err)
	}
	reporter.SegmentDone()
	reporter.SegmentResolved(0)
	upstream := &runtimeMetadataObserver{}
	if err = reporter.FlushCheckpoint(persistence.WithObserver(ctx, upstream)); err != nil {
		t.Fatal(err)
	}
	if err = reporter.Close(); err != nil {
		t.Fatal(err)
	}
	for name, observer := range map[string]*runtimeMetadataObserver{"runtime": observed, "upstream": upstream} {
		queue, sqlCalls, transactions := 0, 0, 0
		for _, event := range observer.snapshot() {
			if event.Metadata.Operation != "checkpoint" {
				continue
			}
			switch event.Phase {
			case "save_queue":
				queue++
			case "query", "exec":
				sqlCalls++
			case "transaction":
				transactions++
			}
		}
		if queue == 0 || sqlCalls == 0 || transactions == 0 {
			t.Fatalf("%s observer missed checkpoint/calibration queue/SQL/Tx: %d/%d/%d", name, queue, sqlCalls, transactions)
		}
	}
	if stats := defaults.Snapshot(); stats.Calls == 0 || stats.Transactions < 2 {
		t.Fatalf("default collector missed Reporter SQL: %+v", stats)
	}
	observedSaves := uint64(0)
	for _, event := range observed.snapshot() {
		if event.Phase == "save_execution" {
			observedSaves++
		}
	}
	if got := runtime.Saver.Snapshot().Saves; got != observedSaves {
		t.Fatalf("Reporter Save double-enqueued: scheduler=%d observed=%d", got, observedSaves)
	}
	if err = saver.Save(ctx, persistence.Metadata{Operation: "checkpoint", Class: persistence.Reliable}, func(ctx context.Context) error {
		if persistence.Lifetime(ctx).Done() != worker.Done() {
			t.Fatal("writer verification escaped worker lifetime")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
