package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRubyHandoffGlossaryBusinessAtomicAndReplay(t *testing.T) {
	t.Run("database", func(t *testing.T) { testGlossaryBusinessAtomicAndReplay(t, false, "") })
	t.Run("execution_receiver", func(t *testing.T) { testGlossaryBusinessAtomicAndReplay(t, true, "") })
}

func TestRubyHandoffGlossaryDictionaryCommitRetryAndLostACK(t *testing.T) {
	for _, mode := range []string{"retry_not_committed", "committed_ack_lost", "committed_ack_lost_cancelled"} {
		t.Run(mode, func(t *testing.T) { testGlossaryBusinessAtomicAndReplay(t, true, mode) })
	}
}

func TestMemberBusinessUnknownACKKeepsCanonicalIdentityBeforeRefund(t *testing.T) {
	testGlossaryBusinessAtomicAndReplay(t, false, "committed_identity_changed")
}

func TestMemberPendingCacheAbsenceJoinsOriginalWriter(t *testing.T) {
	testGlossaryBusinessAtomicAndReplay(t, false, "pending_cache_writer")
}

func testGlossaryBusinessAtomicAndReplay(t *testing.T, wrapped bool, commitFault string) {
	ctx := context.Background()
	env := newJobRoundTestEnv(t, nil)
	j, resources, rounds := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning, segmentCount: 1, rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning}}}})
	jr, round := resources[0], rounds[0][0]
	resource := jr.QueryResource().OnlyX(ctx)
	seg := createTestSegment(t, env.client, resource.ID, 0, "raw source", nil)
	scope := workstate.Scope{JobID: j.ID, ResourceID: resource.ID, JobResourceID: jr.ID, RoundID: round.ID, SourceGeneration: resource.SourceGeneration, SourceRevisionID: resource.CurrentSourceRevisionID}
	store := workstate.NewStore(env.client)
	if _, err := store.SealRound(ctx, scope, []int{seg.ID}); err != nil {
		t.Fatal(err)
	}
	digest, err := workstate.ExecutionSnapshotDigest(j.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	member := workstate.PromotionMember{SegmentID: seg.ID, WorkID: workstate.WorkIdentity(scope, seg.ID), CandidateID: "candidate", BaselineVersion: seg.ContentVersion, BaselineTarget: seg.TargetText, BaselineStatus: string(seg.Status), SourceGeneration: scope.SourceGeneration, SourceRevisionID: scope.SourceRevisionID}
	digestBytes := func(p []byte) string { h := sha256.Sum256(p); return hex.EncodeToString(h[:]) }
	key := workstate.MemberKey{RequestID: "ruby-business", Ordinal: 0}
	inputPayload := []byte(`{"source":"raw source"}`)
	request := workstate.Request{ID: "ruby-business", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 2, SnapshotDigest: digest, InputMembers: []workstate.InputMember{{Key: key, PromotionMember: member, PromptID: "1", Payload: inputPayload, Digest: digestBytes(inputPayload)}}, BusinessBound: 1 << 20}
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	capture := []byte("complete response")
	if _, err := store.CaptureResponse(ctx, workstate.CapturedResponse{ResponseHeader: workstate.ResponseHeader{RequestID: request.ID, Scope: scope, SnapshotDigest: digest}, Payload: capture, Result: workstate.RequestResult{UsageKnown: true, InputTokens: 3}}); err != nil {
		t.Fatal(err)
	}
	model := []byte(`{"text":"候选"}`)
	if _, err := store.PublishOutputs(ctx, workstate.OutputPublication{Key: key, Members: []workstate.OutputMember{{Key: key, Payload: model, Bytes: int64(len(model)), Digest: digestBytes(model)}}, Next: 1, Digest: digestBytes(model), Complete: true}); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("规范", 8000)
	old := env.client.GlossaryEntry.Create().SetProjectID(env.project.ID).SetSourceKey("sdk").SetSource("SDK").SetTarget(large).SetNotes(strings.Repeat("history note", 10000)).SaveX(ctx)
	var g glossary.ResponseProcessor
	var owner *ExecutionInputLease
	if wrapped {
		root := inputTestRoot(t)
		ctx = inputContext(root, env.project)
		loaded, lease, err := LoadExecutionGlossary(ctx, env.client, env.project, true)
		if err != nil {
			t.Fatal(err)
		}
		var ok bool
		g, ok = loaded.(glossary.ResponseProcessor)
		if !ok {
			t.Fatal("execution glossary lost atomic response receiver")
		}
		owner = lease
		t.Cleanup(lease.Close)
	} else {
		loaded, err := NewDatabaseGlossary(ctx, env.client, env.project)
		if err != nil {
			t.Fatal(err)
		}
		g = loaded
	}
	var before int64
	if owner != nil {
		before = owner.usage.PayloadBytes
	}
	entries := []glossary.Entry{{Source: "SDK", Target: "候选"}}
	for i := 0; i < 20; i++ {
		entries = append(entries, glossary.Entry{Source: strings.Repeat("A", i+1), Target: "term", Notes: strings.Repeat("n", 2048)})
	}
	in := glossary.ResponseProcess{RequestID: request.ID, InputDigest: digestBytes(capture), TargetLang: "zh", Rewrite: true, ItemLimit: 1 << 20, BusinessLimit: 1 << 20, Entries: entries}
	in.VisitTargets = func(ctx context.Context, visit func(glossary.ResponseTarget) error) error {
		return visit(glossary.ResponseTarget{Ordinal: 0, ID: "1", Text: "候选"})
	}
	in.WriteTarget = func(ctx context.Context, target glossary.ResponseTarget) error {
		payload, err := json.Marshal(target)
		if err != nil {
			return err
		}
		return store.StageCorrectedMember(ctx, workstate.OutputMember{Key: key, Payload: payload, Bytes: int64(len(payload)), Digest: digestBytes(payload)})
	}
	in.Publish = func(ctx context.Context, out glossary.ResponseBusiness, _ *responsecontract.PreparedPublication) error {
		return store.PublishMemberBusiness(ctx, request.ID, out.Receipt)
	}
	fault := errors.New("receipt fault")
	fail := true
	env.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := m.Field("glossary_receipt"); ok && fail {
				return nil, fault
			}
			return next.Mutate(ctx, m)
		})
	})
	if _, err := g.ProcessResponse(ctx, in); !errors.Is(err, fault) {
		t.Fatalf("fault %v", err)
	}
	if count := env.client.GlossaryEntry.Query().CountX(ctx); count != 1 {
		t.Fatalf("rollback count %d", count)
	}
	if owner != nil && owner.usage.PayloadBytes != before {
		t.Fatalf("rollback retained dictionary growth: %d/%d", owner.usage.PayloadBytes, before)
	}
	fail = false
	if commitFault == "pending_cache_writer" {
		database := g.(*DatabaseGlossary)
		row, err := responseBusinessRequest(ctx, env.client, request.ID, env.project.ID)
		if err != nil {
			t.Fatal(err)
		}
		root, err := resourcebudget.New(resourcebudget.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		cacheScope := resourcebudget.Scope{ActorID: 1, CallID: "pending-cache"}
		cacheBytes := int64(unsafe.Sizeof(glossary.Entry{})) + int64(len("pending")+len("entry"))
		if err := root.Claim(cacheScope, "pending-cache", resourcebudget.Vector{PayloadBytes: cacheBytes}); err != nil {
			t.Fatal(err)
		}
		cache := []glossary.Entry{{Source: strings.Clone("pending"), Target: strings.Clone("entry")}}
		rollbacks := 0
		database.pendingCache = &pendingGlossaryCache{proof: responseBusinessProof{identity: workstate.ResponseIdentityFromRow(row), projectID: env.project.ID, inputDigest: in.InputDigest}, entries: cache, settlement: glossaryCacheSettlement{rollback: func() error {
			clear(cache)
			cache = nil
			rollbacks++
			return root.Release(cacheScope, "pending-cache", resourcebudget.Vector{PayloadBytes: cacheBytes})
		}}}
		t.Cleanup(func() {
			if database.pendingCache != nil {
				if err := database.resolvePendingCache(context.Background()); err != nil {
					t.Error(err)
				}
			}
		})
		tx, err := env.client.Tx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := workstate.LockJob(ctx, tx.Client(), j.ID); err != nil {
			t.Fatal(err)
		}
		blocked, finish := context.WithTimeout(ctx, 100*time.Millisecond)
		err = database.resolvePendingCache(blocked)
		finish()
		if !errors.Is(err, context.DeadlineExceeded) || rollbacks != 0 || database.pendingCache == nil || root.Snapshot(1).Instance.PayloadBytes != cacheBytes {
			t.Fatalf("unjoined writer was mistaken for absent receipt: rollbacks=%d err=%v", rollbacks, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := database.resolvePendingCache(ctx); err != nil {
			t.Fatal(err)
		}
		if rollbacks != 1 || database.pendingCache != nil || cache != nil || root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
			t.Fatal("proved rollback retained actual pending cache")
		}
		return
	}
	commitCalls := 0
	var storage *workstate.StorageBudget
	var storageRoot *resourcebudget.RootBudget
	var commitCharge int64
	if commitFault == "committed_identity_changed" {
		actor := env.client.User.Query().FirstX(ctx)
		env.client.Job.UpdateOneID(j.ID).SetCreatedByID(actor.ID).ExecX(ctx)
		var err error
		storageRoot, err = resourcebudget.New(resourcebudget.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		source := workstate.InventorySource{DB: env.client, Driver: "sqlite", ActorID: actor.ID, ItemBytes: 1 << 20}
		if err = storageRoot.Rebuild(ctx, source); err != nil {
			t.Fatal(err)
		}
		if err = storageRoot.RebuildActor(ctx, actor.ID, source); err != nil {
			t.Fatal(err)
		}
		storageScope := resourcebudget.Scope{ActorID: actor.ID, JobID: j.ID}
		if err = storageRoot.Claim(storageScope, workstate.RuntimeIdentity(j.ID), resourcebudget.Vector{LogicalStorageBytes: 8 << 20}); err != nil {
			t.Fatal(err)
		}
		storage = &workstate.StorageBudget{Root: storageRoot, Scope: storageScope, Source: source}
		workstate.InstallStorageHooks(env.client)
		ctx = workstate.WithStorageBudget(ctx, storage)
		t.Cleanup(func() {
			_, _ = env.client.ExecContext(context.Background(), "UPDATE work_requests SET retry_epoch=0 WHERE identity=?", request.ID)
			if err := storage.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()
	if commitFault != "" {
		env.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				if _, ok := m.Field("glossary_receipt"); ok {
					tx, err := m.(*ent.WorkRequestMutation).Tx()
					if err != nil {
						return nil, err
					}
					tx.OnCommit(func(next ent.Committer) ent.Committer {
						return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
							commitCalls++
							if commitCalls == 1 && commitFault == "retry_not_committed" {
								return &pgconn.PgError{Code: "40001", Message: "fixture retry before commit"}
							}
							if err := next.Commit(ctx, tx); err != nil {
								return err
							}
							if commitFault == "committed_identity_changed" {
								commitCharge = storageRoot.Snapshot(storage.Scope.ActorID).Instance.LogicalStorageBytes
								if _, err := env.client.ExecContext(context.Background(), "UPDATE work_requests SET retry_epoch=1 WHERE identity=?", request.ID); err != nil {
									return err
								}
								return errors.New("committed business acknowledgement lost")
							}
							if strings.HasPrefix(commitFault, "committed_ack_lost") {
								if commitFault == "committed_ack_lost_cancelled" {
									cancelProcess()
								}
								return errors.New("fixture commit ACK lost")
							}
							return nil
						})
					})
				}
				return next.Mutate(ctx, m)
			})
		})
	}
	out, err := g.ProcessResponse(processCtx, in)
	if commitFault == "committed_identity_changed" {
		if !errors.Is(err, workstate.ErrManifest) {
			t.Fatalf("business unknown ACK accepted changed epoch: %v", err)
		}
		retained := storageRoot.Snapshot(storage.Scope.ActorID).Instance
		if retained.LogicalStorageBytes != commitCharge {
			t.Fatalf("business unknown ACK refunded: commit=%d retained=%d", commitCharge, retained.LogicalStorageBytes)
		}
		if err := storage.Settle(ctx); !errors.Is(err, workstate.ErrManifest) {
			t.Fatalf("business settlement accepted changed identity: %v", err)
		}
		if err := storage.Close(); !errors.Is(err, workstate.ErrManifest) {
			t.Fatalf("business close accepted changed identity: %v", err)
		}
		if storageRoot.Snapshot(storage.Scope.ActorID).Instance != retained || env.client.UsageRecord.Query().CountX(ctx) != 1 {
			t.Fatal("business failed identity refunded or repeated usage")
		}
		if _, err := env.client.ExecContext(ctx, "UPDATE work_requests SET retry_epoch=0 WHERE identity=?", request.ID); err != nil {
			t.Fatal(err)
		}
		if err := storage.Settle(ctx); err != nil {
			t.Fatal("restored business identity did not settle", err)
		}
		in.VisitTargets = func(context.Context, func(glossary.ResponseTarget) error) error {
			t.Fatal("unknown ACK replay revisited members")
			return nil
		}
		out, err = g.ProcessResponse(ctx, in)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer out.Release()
	corrected, loadErr := store.LoadOutputMember(ctx, key, true, 1<<20)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	defer corrected.Release()
	var final glossary.ResponseTarget
	if err := json.Unmarshal(corrected.Payload, &final); err != nil {
		t.Fatal(err)
	}
	if final.Text != large || len(out.Receipt) > glossary.ResponseReceiptLimit {
		t.Fatalf("body=%d receipt=%d", len(final.Text), len(out.Receipt))
	}
	var receipt glossary.ResponseReceipt
	if err := json.Unmarshal(out.Receipt, &receipt); err != nil || receipt.Version != 2 || receipt.Added != 20 || receipt.Skipped != 1 {
		t.Fatalf("receipt %+v %v", receipt, err)
	}
	var adopted int64
	if owner != nil {
		adopted = owner.usage.PayloadBytes
		if adopted <= before {
			t.Fatal("committed dictionary has no retained owner")
		}
		// One final dictionary copy and twenty private nodes/texts remain. A
		// rejected attempt cannot retain a second copy through successful retry.
		var expected int64
		expected, err = backend.AllocationBytes(20, int64(unsafe.Sizeof(glossary.Entry{})))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries[1:] {
			expected += int64(unsafe.Sizeof(glossary.EntrySnapshot{})) + int64(len(entry.Source)+len(entry.Target)+len(entry.Notes))
		}
		if adopted-before != expected {
			t.Fatalf("retained dictionary growth=%d want=%d", adopted-before, expected)
		}
	}
	if commitFault != "" {
		want := 1
		if commitFault == "retry_not_committed" {
			want = 2
		}
		if commitCalls != want {
			t.Fatalf("commit calls=%d want=%d", commitCalls, want)
		}
	}
	env.client.GlossaryEntry.UpdateOneID(old.ID).SetTarget("manual later").ExecX(ctx)
	in.VisitTargets = func(context.Context, func(glossary.ResponseTarget) error) error {
		t.Fatal("receipt replay visited model members")
		return nil
	}
	replayed, err := g.ProcessResponse(ctx, in)
	defer replayed.Release()
	if err != nil || string(replayed.Receipt) != string(out.Receipt) {
		t.Fatalf("replay changed %v", err)
	}
	if owner != nil && owner.usage.PayloadBytes != adopted {
		t.Fatalf("receipt replay accumulated dictionary capacity: %d/%d", owner.usage.PayloadBytes, adopted)
	}
}
