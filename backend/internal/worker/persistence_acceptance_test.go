package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	pa "github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func persistenceRoundStore(t *testing.T, f *pa.Fixture, batchSizes ...int) *roundStore {
	batchSize := 300
	if len(batchSizes) > 0 {
		batchSize = batchSizes[0]
	}
	indices := make(map[int]int, len(f.IDs))
	for i, id := range f.IDs {
		indices[i] = id
	}
	snapshot := &service.JobExecutionSnapshot{SourceLang: "en", TargetLang: "zh", Rounds: []execution.JobRoundSnapshot{{Mode: "extract", Backend: execution.BackendSnapshot{ID: 1, Name: "isolated-fake"}, Extract: &execution.JobExtractRoundSnapshot{TemplateContent: "Extract terms.", Concurrency: 10, BatchSize: batchSize, Retry: execution.RetryConfig{MaxAttempts: 2}}}}}
	snapshot = completeWorkerSnapshot(t, snapshot)
	f.SetExecutionSnapshot(t, snapshot)
	return newRoundStore(f.Client, f.Scope, indices, snapshot, 0, nil, false, pa.Driver(), nil, nil)
}
func persistenceIntent(f *pa.Fixture, pool int) pipeline.RequestIntent {
	in := pipeline.RequestIntent{ID: "acceptance-request", ResourceID: f.Scope.ResourceID, Stage: backend.RequestStageMain, BackendID: 1, Pool: pool, InputDigest: "synthetic"}
	for i := range f.IDs {
		in.Indices = append(in.Indices, i)
	}
	return in
}
func persistenceReport(t testing.TB, p *pa.Probe, name string, extra map[string]any) pa.Sample {
	t.Helper()
	s := p.Snapshot(name, extra)
	if err := pa.WriteSample(s); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s driver=%s SQL=%d BEGIN=%d COMMIT=%d elapsed=%.2fms", name, s.Driver, s.SQL, s.Begins, s.Commits, s.ElapsedMS)
	return s
}

// The counted path includes the adapter's durable pool advance and Reserve,
// rather than moving per-member work outside the reported operation.
func TestPersistenceHotPathCosts(t *testing.T) {
	for _, ms := range []int{0, 1, 2, 5} {
		t.Run(fmt.Sprintf("reserve300-delay%d", ms), func(t *testing.T) {
			f := pa.Seed(t, 300, true)
			store := persistenceRoundStore(t, f)
			f.Probe.Reset(time.Duration(ms) * time.Millisecond)
			if err := store.Reserve(t.Context(), persistenceIntent(f, 0)); err != nil {
				t.Fatal(err)
			}
			s := persistenceReport(t, f.Probe, "reserve300", map[string]any{"delay_ms": ms, "pool": 0})
			if s.SQL > 30 {
				t.Fatalf("full homogeneous Reserve SQL=%d exceeds 30", s.SQL)
			}
		})
	}
	t.Run("pool-advance300", func(t *testing.T) {
		f := pa.Seed(t, 300, true)
		store := persistenceRoundStore(t, f)
		f.Probe.Reset(0)
		if err := store.Reserve(t.Context(), persistenceIntent(f, 1)); err != nil {
			t.Fatal(err)
		}
		s := persistenceReport(t, f.Probe, "pool-advance-reserve300", nil)
		if s.SQL > 30 {
			t.Fatalf("pool advance plus Reserve SQL=%d exceeds 30", s.SQL)
		}
	})
}

func TestPersistenceSealAndCheckpointCosts(t *testing.T) {
	// 502 sealed members and 501 confirmations cross both 500-row SQL chunks
	// while retaining an unfinished member. Full acceptance keeps seal5000.
	count, confirmed := 502, 501
	if pa.FullWorkload() {
		count, confirmed = 5000, 300
	}
	f := pa.Seed(t, count, false)
	f.Probe.Reset(0)
	if _, err := f.Store.SealRound(t.Context(), f.Scope, f.IDs); err != nil {
		t.Fatal(err)
	}
	persistenceReport(t, f.Probe, fmt.Sprintf("seal%d", count), nil)
	f.Probe.Reset(0)
	err := workstate.Transaction(t.Context(), f.Client, func(tx *ent.Client) error {
		if err := workstate.LockJob(t.Context(), tx, f.Scope.JobID); err != nil {
			return err
		}
		facts := make([]workstate.Confirmation, confirmed)
		for i, id := range f.IDs[:confirmed] {
			facts[i] = workstate.Confirmation{SegmentID: id}
		}
		_, err := workstate.Confirm(t.Context(), tx, f.Scope.JobID, f.Scope.RoundID, facts)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	persistenceReport(t, f.Probe, fmt.Sprintf("checkpoint%d", confirmed), nil)
	f.Probe.Reset(0)
	if err := workstate.Transaction(t.Context(), f.Client, func(tx *ent.Client) error {
		if err := workstate.LockJob(t.Context(), tx, f.Scope.JobID); err != nil {
			return err
		}
		return workstate.Calibrate(t.Context(), tx, f.Scope.JobID)
	}); err != nil {
		t.Fatal(err)
	}
	persistenceReport(t, f.Probe, "calibrate", nil)
	job := f.Client.Job.GetX(t.Context(), f.Scope.JobID)
	if job.ProgressCompleted != int64(confirmed) || job.ProgressTotal != int64(count) {
		t.Fatalf("progress=%d/%d", job.ProgressCompleted, job.ProgressTotal)
	}
	// Published SegmentID-only replay remains legal and does not recount.
	if err := workstate.Transaction(t.Context(), f.Client, func(tx *ent.Client) error {
		if err := workstate.LockJob(t.Context(), tx, f.Scope.JobID); err != nil {
			return err
		}
		_, err := workstate.Confirm(t.Context(), tx, f.Scope.JobID, f.Scope.RoundID, []workstate.Confirmation{{SegmentID: f.IDs[0]}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if f.Client.Job.GetX(t.Context(), f.Scope.JobID).ProgressCompleted != int64(confirmed) {
		t.Fatal("legacy replay recounted progress")
	}
}

func TestPersistenceReservationProtocol(t *testing.T) {
	f := pa.Seed(t, 300, true)
	store := persistenceRoundStore(t, f)
	in := persistenceIntent(f, 0)
	if err := store.Reserve(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	for _, id := range f.IDs {
		cur, err := f.Store.LoadCursor(t.Context(), f.Scope, id)
		if err != nil || cur.MainAttempts != 1 || cur.NetworkAttempts != 1 {
			t.Fatalf("duplicate debit: %+v %v", cur, err)
		}
	}
	changed := in
	changed.InputDigest = "conflicting"
	if err := store.Reserve(t.Context(), changed); err == nil {
		t.Fatal("conflicting identity accepted")
	}
	if err := store.AbortRequest(t.Context(), in.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range f.IDs {
		cur, err := f.Store.LoadCursor(t.Context(), f.Scope, id)
		if err != nil || cur.MainAttempts != 0 || cur.NetworkAttempts != 0 {
			t.Fatalf("refund: %+v %v", cur, err)
		}
	}
	if err := store.Reserve(t.Context(), in); err == nil {
		t.Fatal("retired identity resurrected")
	}
	in.ID = "sent-request"
	if err := store.Reserve(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(t.Context(), in.ID, pipeline.RequestRecord{State: "sent"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AbortRequest(t.Context(), in.ID); err == nil {
		t.Fatal("sent request refunded")
	}
	f.Client.Job.UpdateOneID(f.Scope.JobID).SetStatus("cancelled").ExecX(t.Context())
	for range 2 {
		if err := store.Record(t.Context(), in.ID, pipeline.RequestRecord{State: "received", UsageKnown: true, Usage: backend.Usage{PromptTokens: 3, CompletionTokens: 5}}); err != nil {
			t.Fatal(err)
		}
	}
	if f.Client.UsageRecord.Query().CountX(t.Context()) != 1 {
		t.Fatal("usage duplicated after cancel")
	}
	in.ID = "after-cancel"
	if err := store.Reserve(t.Context(), in); !errors.Is(err, workstate.ErrStopped) {
		t.Fatalf("cancel allowed reservation: %v", err)
	}
}

func TestPersistenceDelayHonorsContext(t *testing.T) {
	f := pa.Seed(t, 1, true)
	f.Probe.Reset(time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := f.Client.Job.Get(ctx, f.Scope.JobID)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("delay ignored context: %v", err)
	}
}

func TestPersistenceAtomicChunkFailure(t *testing.T) {
	f := pa.Seed(t, 1200, false)
	f.Probe.FailStatement("INSERT INTO \"work_items\"", 2)
	_, err := f.Store.SealRound(t.Context(), f.Scope, f.IDs)
	if !errors.Is(err, pa.ErrInjected) {
		t.Fatalf("later Seal chunk fault did not fire: %v", err)
	}
	if f.Client.WorkItem.Query().CountX(t.Context()) != 0 || f.Client.JobRound.GetX(t.Context(), f.Scope.RoundID).ManifestSealed {
		t.Fatal("partial Seal committed")
	}
	f.Probe.Reset(0)
	if _, err := f.Store.SealRound(t.Context(), f.Scope, f.IDs); err != nil {
		t.Fatal(err)
	}
	f.Probe.FailStatement("INSERT INTO \"job_round_segments\"", 2)
	err = workstate.Transaction(t.Context(), f.Client, func(tx *ent.Client) error {
		if err := workstate.LockJob(t.Context(), tx, f.Scope.JobID); err != nil {
			return err
		}
		facts := make([]workstate.Confirmation, len(f.IDs))
		for i, id := range f.IDs {
			facts[i] = workstate.Confirmation{SegmentID: id}
		}
		_, err := workstate.Confirm(t.Context(), tx, f.Scope.JobID, f.Scope.RoundID, facts)
		return err
	})
	if !errors.Is(err, pa.ErrInjected) {
		t.Fatalf("later Confirm chunk fault did not fire: %v", err)
	}
	if f.Client.JobRoundSegment.Query().CountX(t.Context()) != 0 || f.Client.Job.GetX(t.Context(), f.Scope.JobID).ProgressCompleted != 0 {
		t.Fatal("partial checkpoint committed")
	}
}

func TestPersistenceUnknownCommit(t *testing.T) {
	f := pa.Seed(t, 1, true)
	ctx := t.Context()
	row := f.Client.Segment.GetX(ctx, f.IDs[0])
	candidate := persistenceCandidate(t, f, row.ID)
	if err := f.Store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	f.Probe.LoseNextCommit()
	input := workstate.CommitInput{Scope: f.Scope, SegmentID: row.ID, CommitID: "unknown-commit", CandidateID: candidate.ID, CandidateVersion: 1, BaselineVersion: row.ContentVersion, BaselineStatus: string(row.Status), Target: "translated", Status: "translated"}
	result, err := f.Store.Commit(ctx, input)
	if err != nil || result.Outcome != workstate.AlreadyCommitted {
		t.Fatalf("lost ack verification: %+v %v", result, err)
	}
	f.Client.Segment.UpdateOneID(row.ID).SetTargetText("human edit").ExecX(ctx)
	result, err = f.Store.Commit(ctx, input)
	if err != nil || result.Outcome != workstate.AlreadyCommitted {
		t.Fatalf("replay verification: %+v %v", result, err)
	}
	if *f.Client.Segment.GetX(ctx, row.ID).TargetText != "human edit" || f.Client.Job.GetX(ctx, f.Scope.JobID).ProgressCompleted != 1 {
		t.Fatal("unknown/replay overwrote or double counted")
	}
}

func TestPersistencePostgresLockAndDisconnect(t *testing.T) {
	if pa.Driver() != "postgres" {
		return
	}
	f := pa.Seed(t, 1, true)
	ctx := t.Context()
	locker, err := f.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback()
	if _, err := locker.ExecContext(ctx, "UPDATE jobs SET retry_epoch=retry_epoch WHERE id=$1", f.Scope.JobID); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		err = persistenceRoundStore(t, f).Reserve(blocked, persistenceIntent(f, 0))
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("real PG row lock wait not cancelled: %v", err)
		}
	}
	if err := locker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if inUse := f.DB.Stats().InUse; inUse != 0 {
		t.Fatalf("cancelled lock leaked %d connections", inUse)
	}
	// Terminate only this owned cluster's exact transaction connection before
	// COMMIT; then verify its write rolled back and the pool becomes usable.
	tx, err := f.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE jobs SET progress_completed=99 WHERE id=$1", f.Scope.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("terminated transaction reported success")
	}
	_ = tx.Rollback()
	if f.Client.Job.GetX(ctx, f.Scope.JobID).ProgressCompleted != 0 {
		t.Fatal("terminated uncommitted transaction leaked its write")
	}
	if inUse := f.DB.Stats().InUse; inUse != 0 {
		t.Fatalf("disconnect leaked %d connections", inUse)
	}
}

func persistenceCandidate(t *testing.T, f *pa.Fixture, id int) workstate.Candidate {
	t.Helper()
	store := persistenceRoundStore(t, f)
	row := f.Client.Segment.GetX(t.Context(), id)
	index := 0
	for i, member := range f.IDs {
		if member == id {
			index = i
			break
		}
	}
	c := &pipeline.Candidate{DTOVersion: pipeline.CandidateDTOVersion, ID: fmt.Sprintf("candidate-%d", id), WorkID: store.WorkIdentity(index), Version: 1, Index: index, Mode: pipeline.RoundModeTranslate, Format: "text", Ready: true, BaselineStatus: string(row.Status), Segment: pipeline.Segment{DBID: id, Source: row.SourceText, ContentVersion: row.ContentVersion, Status: string(row.Status), TargetIsNull: row.TargetText == nil, Translate: true}}
	payload, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return workstate.Candidate{ID: c.ID, WorkID: c.WorkID, Version: 1, Scope: f.Scope, SegmentID: id, DTOVersion: 2, Mode: "translate", SnapshotDigest: store.digest, BaselineVersion: row.ContentVersion, BaselineTarget: row.TargetText, BaselineStatus: string(row.Status), State: "ready_to_commit", Payload: payload}
}
func TestPersistenceFencesAndPauseHandoff(t *testing.T) {
	for _, boundary := range []string{"epoch", "source", "member-budget", "pause", "cancel", "aba"} {
		t.Run(boundary, func(t *testing.T) {
			f := pa.Seed(t, 2, true)
			ctx := t.Context()
			candidate := persistenceCandidate(t, f, f.IDs[0])
			in := persistenceIntent(f, 0)
			switch boundary {
			case "epoch":
				f.Client.Job.UpdateOneID(f.Scope.JobID).SetRetryEpoch(1).ExecX(ctx)
			case "source":
				f.Client.Resource.UpdateOneID(f.Scope.ResourceID).SetSourceGeneration(1).ExecX(ctx)
			case "member-budget":
				f.Client.WorkItem.Update().Where(workitem.SegmentIDEQ(f.IDs[1])).SetMainAttempts(3).ExecX(ctx)
			case "pause":
				if err := persistenceRoundStore(t, f).Reserve(ctx, in); err != nil {
					t.Fatal(err)
				}
				f.Client.Job.UpdateOneID(f.Scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
				if err := f.Store.SaveCandidate(ctx, candidate); !errors.Is(err, workstate.ErrStopped) {
					t.Fatalf("unadmitted pause candidate: %v", err)
				}
				candidate.ParentRequestID = in.ID
				if err := f.Store.SaveCandidate(ctx, candidate); !errors.Is(err, workstate.ErrStopped) {
					t.Fatalf("reserved-only pause candidate: %v", err)
				}
				if err := f.Store.RecordRequest(ctx, in.ID, workstate.RequestResult{State: "received"}); err != nil {
					t.Fatal(err)
				}
				if err := f.Store.SaveCandidate(ctx, candidate); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				f.Client.Job.UpdateOneID(f.Scope.JobID).SetStatus("cancelled").ExecX(ctx)
				if err := f.Store.SaveCandidate(ctx, candidate); !errors.Is(err, workstate.ErrStopped) {
					t.Fatalf("cancel accepted candidate: %v", err)
				}
			case "aba":
				if err := f.Store.SaveCandidate(ctx, candidate); err != nil {
					t.Fatal(err)
				}
				f.Client.Segment.UpdateOneID(candidate.SegmentID).SetTargetText("temporary").ExecX(ctx)
				f.Client.Segment.UpdateOneID(candidate.SegmentID).ClearTargetText().ExecX(ctx)
				result, err := f.Store.Commit(ctx, workstate.CommitInput{Scope: f.Scope, SegmentID: candidate.SegmentID, CommitID: "stale-commit", CandidateID: candidate.ID, CandidateVersion: 1, BaselineVersion: candidate.BaselineVersion, BaselineStatus: candidate.BaselineStatus, Target: "overwrite"})
				if err != nil || result.Outcome != workstate.Stale {
					t.Fatalf("ABA bypassed version: %+v %v", result, err)
				}
				return
			}
			in.ID = "must-reject"
			if err := persistenceRoundStore(t, f).Reserve(ctx, in); err == nil {
				t.Fatal("fence accepted Reserve")
			}
			if boundary == "member-budget" {
				cur, err := f.Store.LoadCursor(ctx, f.Scope, f.IDs[0])
				if err != nil || cur.MainAttempts != 0 {
					t.Fatalf("late member failure charged early member: %+v %v", cur, err)
				}
			}
		})
	}
}

func TestPersistenceCandidateCursorAtomicity(t *testing.T) {
	f := pa.Seed(t, 1, true)
	candidate := persistenceCandidate(t, f, f.IDs[0])
	cur, err := f.Store.LoadCursor(t.Context(), f.Scope, f.IDs[0])
	if err != nil {
		t.Fatal(err)
	}
	cur.State = "candidate"
	cur.PromptPhase = "waiting_alignment"
	f.Probe.FailStatement("UPDATE \"work_items\"", 2)
	if err := f.Store.SaveCandidateWithCursor(t.Context(), candidate, cur); !errors.Is(err, pa.ErrInjected) {
		t.Fatalf("post-candidate cursor failure: %v", err)
	}
	if f.Client.WorkCandidate.Query().CountX(t.Context()) != 0 {
		t.Fatal("partial candidate survived cursor failure")
	}
	row := f.Client.WorkItem.Query().OnlyX(t.Context())
	if row.CandidateID != "" || row.State != "pending" {
		t.Fatalf("partial candidate/cursor ownership: %+v", row)
	}
	f.Probe.Reset(0)
	if err := f.Store.SaveCandidateWithCursor(t.Context(), candidate, cur); err != nil {
		t.Fatal(err)
	}
}

// A SegmentID-only reporter fact predates candidate acceptance. Enriching that
// same fact with the commit receipt must remain atomic and must not recount it.
func TestPersistenceLegacyCheckpointUnknownCommit(t *testing.T) {
	f := pa.Seed(t, 1, true)
	ctx := t.Context()
	candidate := persistenceCandidate(t, f, f.IDs[0])
	if err := f.Store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := workstate.Transaction(ctx, f.Client, func(tx *ent.Client) error {
		if err := workstate.LockJob(ctx, tx, f.Scope.JobID); err != nil {
			return err
		}
		_, err := workstate.Confirm(ctx, tx, f.Scope.JobID, f.Scope.RoundID, []workstate.Confirmation{{SegmentID: candidate.SegmentID}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if f.Client.Job.GetX(ctx, f.Scope.JobID).ProgressCompleted != 1 {
		t.Fatal("legacy checkpoint absent")
	}
	f.Probe.LoseNextCommit()
	input := workstate.CommitInput{Scope: f.Scope, SegmentID: candidate.SegmentID, CommitID: "legacy-enriched-commit", CandidateID: candidate.ID, CandidateVersion: 1, BaselineVersion: candidate.BaselineVersion, BaselineStatus: candidate.BaselineStatus, Target: "translated", Status: "translated"}
	result, err := f.Store.Commit(ctx, input)
	if err != nil || result.Outcome != workstate.AlreadyCommitted {
		t.Fatalf("legacy fact lost commit receipt: %+v %v", result, err)
	}
	fresh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	confirmed, err := f.Store.LookupCommit(fresh, input.CommitID)
	if err != nil || !confirmed {
		t.Fatalf("fresh receipt lookup: %v %v", confirmed, err)
	}
	if f.Client.JobRoundSegment.Query().CountX(ctx) != 1 || f.Client.Job.GetX(ctx, f.Scope.JobID).ProgressCompleted != 1 {
		t.Fatal("enrichment recounted completion")
	}
	row := f.Client.JobRoundSegment.Query().OnlyX(ctx)
	if row.CommitID == nil || *row.CommitID != input.CommitID || row.CandidateID == nil || *row.CandidateID != candidate.ID || row.Outcome == "legacy" {
		t.Fatal("enrichment did not persist exact acceptance identity")
	}
}

func TestPersistenceReservationUnknownBoundaries(t *testing.T) {
	for _, mode := range []string{"committed-expired", "not-delivered", "verification-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f := pa.Seed(t, 300, true)
			store := persistenceRoundStore(t, f)
			in := persistenceIntent(f, 0)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "committed-expired":
				f.Probe.LoseCommitAndCancel(cancel)
			case "not-delivered":
				f.Probe.LoseBeforeCommit()
			case "verification-unavailable":
				f.Probe.LoseCommitWithUnavailableVerification()
			}
			err := store.Reserve(ctx, in)
			f.Probe.RestoreVerification()
			switch mode {
			case "committed-expired":
				if err != nil || ctx.Err() == nil {
					t.Fatalf("fresh verification failed: %v ctx=%v", err, ctx.Err())
				}
			case "not-delivered":
				var uncommitted interface{ Uncertain() bool }
				if !errors.As(err, &uncommitted) || uncommitted.Uncertain() {
					t.Fatalf("unknown absent identity was not verified: %v", err)
				}
				if f.Client.WorkRequest.Query().CountX(t.Context()) != 0 {
					t.Fatal("not-delivered reservation persisted")
				}
				if err := store.Reserve(t.Context(), in); err != nil {
					t.Fatal(err)
				}
			case "verification-unavailable":
				var uncertain interface{ Uncertain() bool }
				if !errors.As(err, &uncertain) || !uncertain.Uncertain() {
					t.Fatalf("unavailable lookup treated as non-commit: %v", err)
				}
			}
			if f.Client.WorkRequest.Query().CountX(t.Context()) != 1 {
				t.Fatal("unknown identity duplicated/lost")
			}
			for _, id := range f.IDs {
				cur, err := f.Store.LoadCursor(t.Context(), f.Scope, id)
				if err != nil || cur.MainAttempts != 1 || cur.MainNetworkAttempts != 1 {
					t.Fatalf("unknown refunded/recharged member: %+v %v", cur, err)
				}
			}
		})
	}
}
