package workstate_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workresponsemember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func executionBackendDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// The runner selects SQLite or a private PostgreSQL schema through the existing
// persistenceacceptance environment. No production database is discovered.
func TestExecutionBatchBackendContract(t *testing.T) {
	const n = 17
	f := persistenceacceptance.Seed(t, n, false)
	ctx := t.Context()
	f.Probe.Reset(0)
	f.Probe.LoseNextCommit()
	err := f.Store.SealRoundStream(ctx, f.Scope, n, func(visit func(int) error) error {
		for _, id := range f.IDs {
			if err := visit(id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !f.Client.JobRound.GetX(ctx, f.Scope.RoundID).ManifestSealed {
		t.Fatalf("seal ACK proof: %v", err)
	}
	seal := f.Probe.Snapshot("execution_batch_seal", nil)
	f.Probe.Reset(0)
	err = workstate.Transaction(ctx, f.Client, func(tx *ent.Client) error {
		if err := workstate.LockJob(ctx, tx, f.Scope.JobID); err != nil {
			return err
		}
		added, err := workstate.ConfirmIDStream(ctx, tx, f.Scope.JobID, f.Scope.RoundID, func(visit func([]int) error) error {
			for start := 0; start < n; start += 16 {
				if err := visit(f.IDs[start:min(start+16, n)]); err != nil {
					return err
				}
			}
			return nil
		})
		if err == nil && added != n {
			return fmt.Errorf("added=%d", added)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmed := f.Probe.Snapshot("execution_batch_confirm", nil)
	f.Probe.Reset(0)
	counts, err := workstate.ReadStageCountsBatch(ctx, f.Client, []int{f.Scope.JobID})
	stages := f.Probe.Snapshot("execution_batch_stages", nil)
	if err != nil || counts[f.Scope.JobID] == nil || counts[f.Scope.JobID].ConfirmedWork != n || stages.SQL != 1 {
		t.Fatalf("stages=%+v SQL=%d err=%v", counts, stages.SQL, err)
	}

	// The acceptance observer owns the isolated DB. Its dialect wrapper has no
	// raw QueryContext API, so reuse that pool with the production SQL driver for
	// the physical inventory path instead of substituting a mock byte vector.
	client := ent.NewClient(ent.Driver(database.NewDriver(sql.OpenDB(persistenceacceptance.Driver(), f.DB))))
	t.Cleanup(func() { _ = client.Close() })
	store := workstate.NewStore(client)
	config := map[string]any{"schema_version": 4, "defaults_version": 4, "response_format_version": 2}
	client.Job.UpdateOneID(f.Scope.JobID).SetExecutionConfig(config).ExecX(ctx)
	actor := client.User.Create().SetUsername("execution-batch").SetPasswordHash("fixture").SaveX(ctx)
	client.Job.UpdateOneID(f.Scope.JobID).SetCreatedByID(actor.ID).ExecX(ctx)
	source := workstate.InventorySource{DB: client, Driver: persistenceacceptance.Driver(), ActorID: actor.ID, ItemBytes: 1 << 20}
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 64, MaxMainResponseOwners: 64, MaxAuxResponseOwners: 64, MaxBuildWorkers: 64, MaxQueuedDescriptors: 4096, MaxLogicalStorageBytes: 1 << 30}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := root.RebuildActor(ctx, actor.ID, source); err != nil {
		t.Fatal(err)
	}
	owner := resourcebudget.Scope{ActorID: actor.ID, JobID: f.Scope.JobID}
	if err := root.Claim(owner, workstate.RuntimeIdentity(f.Scope.JobID), resourcebudget.Vector{LogicalStorageBytes: 8 << 20}); err != nil {
		t.Fatal(err)
	}
	workstate.InstallStorageHooks(client)
	budget := &workstate.StorageBudget{Root: root, Source: source, Scope: owner}
	t.Cleanup(func() {
		if err := budget.Close(); err != nil {
			t.Error(err)
		}
	})
	owned := workstate.WithStorageBudget(ctx, budget)
	digest, err := workstate.ExecutionSnapshotDigest(config)
	if err != nil {
		t.Fatal(err)
	}
	request := workstate.Request{ID: "execution-batch-request", Scope: f.Scope, SegmentIDs: f.IDs, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: executionBackendDigest([]byte("input")), MainAttempt: true, MaxMainAttempts: 3, SnapshotDigest: digest}
	for i, id := range f.IDs {
		segment := client.Segment.GetX(ctx, id)
		payload := []byte(fmt.Sprintf(`{"source":"界-%d"}`, i))
		key := workstate.MemberKey{RequestID: request.ID, CaptureEpoch: f.Scope.RetryEpoch, Ordinal: i}
		request.InputMembers = append(request.InputMembers, workstate.InputMember{Key: key, PromotionMember: workstate.PromotionMember{SegmentID: id, WorkID: workstate.WorkIdentity(f.Scope, id), CandidateID: fmt.Sprintf("backend-candidate-%d", i), BaselineVersion: segment.ContentVersion, BaselineStatus: string(segment.Status)}, PromptID: fmt.Sprintf("prompt-%d", i), Index: i, Payload: payload, Bytes: int64(len(payload)), Digest: executionBackendDigest(payload)})
	}
	audit := func() int64 {
		_, actual, err := source.Job(ctx, f.Scope.JobID)
		usage, live := root.Usage(owner, workstate.StorageIdentity(f.Scope.JobID))
		if err != nil || !live || usage.LogicalStorageBytes != actual {
			t.Fatalf("%s inventory actual=%d root=%d live=%v err=%v", persistenceacceptance.Driver(), actual, usage.LogicalStorageBytes, live, err)
		}
		return actual
	}
	if err := store.ReserveRequest(owned, request); err != nil {
		t.Fatal(err)
	}
	audit()
	capture := workstate.CapturedResponse{ResponseHeader: workstate.ResponseHeader{RequestID: request.ID, Scope: f.Scope}, Payload: []byte("paid backend response"), Result: workstate.RequestResult{UsageKnown: true, InputTokens: 12, OutputTokens: 8}}
	if _, err := store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	audit()
	archiveDigest := ""
	for start := 0; start < n; start += 16 {
		end := min(start+16, n)
		publication := workstate.OutputPublication{Key: workstate.MemberKey{RequestID: request.ID, CaptureEpoch: f.Scope.RetryEpoch, Ordinal: start}, Next: end, Complete: end == n}
		for i := start; i < end; i++ {
			payload := []byte(fmt.Sprintf(`{"target":"译文-%d"}`, i))
			memberDigest := executionBackendDigest(payload)
			archiveDigest = executionBackendDigest([]byte(archiveDigest + memberDigest))
			publication.Members = append(publication.Members, workstate.OutputMember{Key: workstate.MemberKey{RequestID: request.ID, CaptureEpoch: f.Scope.RetryEpoch, Ordinal: i}, Payload: payload, Bytes: int64(len(payload)), Digest: memberDigest})
		}
		publication.Digest = archiveDigest
		if _, err := store.PublishOutputs(owned, publication); err != nil {
			t.Fatal(err)
		}
		audit()
	}
	before := audit()
	err = workstate.Transaction(owned, client, func(tx *ent.Client) error {
		_, err := tx.WorkResponseMember.Update().SetPayloadBytes(99999).Save(owned)
		return err
	})
	if err != nil || audit() != before {
		t.Fatalf("false payload_bytes changed physical inventory: %v", err)
	}
	err = workstate.Transaction(owned, client, func(tx *ent.Client) error {
		_, err := tx.WorkResponseMember.Update().Where(workresponsemember.StateEQ("pending")).ClearPayload().SetPayloadBytes(0).Save(owned)
		return err
	})
	if err != nil || audit() >= before {
		t.Fatalf("bulk physical retirement failed: %v", err)
	}
	t.Logf("actual backend=%s N=%d sealSQL=%d confirmSQL=%d stageSQL=%d storage_physical=%d", persistenceacceptance.Driver(), n, seal.SQL, confirmed.SQL, stages.SQL, audit())
}

func TestExecutionStageBatchBackendRows(t *testing.T) {
	client, _, probe := persistenceacceptance.Open(t)
	ctx := t.Context()
	project := client.Project.Create().SetName("stage-batch").SaveX(ctx)
	resource := client.Resource.Create().SetProjectID(project.ID).SetPath("stage.txt").SetStoragePath("stage.txt").SetFormat("txt").SaveX(ctx)
	segment := client.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("source").SaveX(ctx)
	ids := make([]int, 0, 2)
	for i := 0; i < 2; i++ {
		job := client.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetStatus("running").SetRetryEpoch(2).SetPauseRequested(i == 0).SaveX(ctx)
		jr := client.JobResource.Create().SetJobID(job.ID).SetResourceID(resource.ID).SetStatus("running").SaveX(ctx)
		round := client.JobRound.Create().SetJobID(job.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("translate").SetStatus("running").SetSegmentTotal(1).SetSegmentCompleted(1).SetManifestSealed(i == 0).SaveX(ctx)
		client.JobRoundSegment.Create().SetJobRoundID(round.ID).SetSegmentID(segment.ID).ExecX(ctx)
		item := client.WorkItem.Create().SetJobID(job.ID).SetResourceID(resource.ID).SetJobRoundID(round.ID).SetSegmentID(segment.ID).SetRetryEpoch(2).SaveX(ctx)
		for j, state := range []string{"pending_alignment", "ready_to_commit", "completed"} {
			// Both a fresh draft and a corrected ready candidate remain live;
			// the completed historical row must not participate in either count.
			payload := []byte(fmt.Sprintf(`{"dto_version":2,"target":"candidate-%d"}`, j))
			client.WorkCandidate.Create().SetIdentity(fmt.Sprintf("stage-%d-%d", i, j)).SetWorkItemID(item.ID).SetVersion(int64(j + 1)).SetDtoVersion(2).SetSnapshotDigest("stage-fixture").SetMode("translate").SetSourceGeneration(0).SetBaselineVersion(segment.ContentVersion).SetBaselineStatus(string(segment.Status)).SetState(state).SetPayload(payload).SetPayloadBytes(int64(len(payload))).ExecX(ctx)
		}
		for j, request := range []struct {
			state, stage string
			epoch        int64
		}{{"sent", "main", 2}, {"sent", "ruby_alignment", 2}, {"unknown", "main", 2}, {"sent", "ruby_alignment", 1}} {
			client.WorkRequest.Create().SetIdentity(fmt.Sprintf("stage-request-%d-%d", i, j)).SetJobID(job.ID).SetResourceID(resource.ID).SetJobRoundID(round.ID).SetRetryEpoch(request.epoch).SetSegmentIds([]int{segment.ID}).SetStage(request.stage).SetState(request.state).SetBackendID(1).SetBudgetModel("stage_separated").SetInputDigest("stage-fixture").ExecX(ctx)
		}
		ids = append(ids, job.ID)
	}
	probe.Reset(0)
	counts, err := workstate.ReadStageCountsBatch(ctx, client, ids)
	sample := probe.Snapshot("stage_batch_live_backend", nil)
	if err != nil || len(counts) != 2 || sample.SQL != 1 {
		t.Fatalf("backend stage jobs=%d SQL=%d err=%v", len(counts), sample.SQL, err)
	}
	for i, id := range ids {
		row := counts[id]
		wantDraining := 0
		if i == 0 {
			wantDraining = 2
		}
		if row == nil || row.PendingAlignment != 1 || row.ReadyToCommit != 1 || row.MainRequests != 1 || row.AlignmentRequests != 1 || row.UnknownRequests != 1 || row.SavingRequests != 0 || row.ConfirmedWork != 1 || row.DrainingRequests != wantDraining || !row.AsOf.Equal(counts[ids[0]].AsOf) {
			t.Fatalf("job %d stage facts=%+v", id, row)
		}
	}
	t.Logf("actual backend=%s stage_SQL=%d jobs=2 live_candidates=4 history_candidates=2 current_epoch_requests=6 old_epoch_requests=2 sealed/unsealed=1/1", persistenceacceptance.Driver(), sample.SQL)
}
