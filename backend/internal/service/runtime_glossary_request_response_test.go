package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
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
	request := workstate.Request{ID: "ruby-business", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 2, FreezePayload: []byte("freeze"), SnapshotDigest: digest, PromotionMembers: []workstate.PromotionMember{member}, BusinessBound: 1 << 20}
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.HandoffResponse(ctx, workstate.ResponseHandoff{RequestID: request.ID, Scope: scope, FreezePayload: request.FreezePayload, ResponsePayload: []byte("complete response"), SnapshotDigest: digest, Members: request.PromotionMembers, BusinessBound: 1 << 20, Result: workstate.RequestResult{UsageKnown: true, InputTokens: 3}}); err != nil {
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
	in := glossary.ResponseProcess{RequestID: request.ID, InputDigest: "raw-digest", TargetLang: "zh", Rewrite: true, ItemLimit: 1 << 20, BusinessLimit: 1 << 20, Entries: entries, Targets: []glossary.ResponseTarget{{ID: "1", Text: "候选"}}}
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
	commitCalls := 0
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
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Body) != large || len(out.Receipt) > 32<<10 {
		t.Fatalf("body=%d receipt=%d", len(out.Body), len(out.Receipt))
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
	replayed, err := g.ProcessResponse(ctx, in)
	if err != nil || string(replayed.Body) != large || string(replayed.Receipt) != string(out.Receipt) {
		t.Fatalf("replay changed %v", err)
	}
	if owner != nil && owner.usage.PayloadBytes != adopted {
		t.Fatalf("receipt replay accumulated dictionary capacity: %d/%d", owner.usage.PayloadBytes, adopted)
	}
}
