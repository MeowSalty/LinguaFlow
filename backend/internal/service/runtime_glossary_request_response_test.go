package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestRubyHandoffGlossaryBusinessAtomicAndReplay(t *testing.T) {
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
	g, err := NewDatabaseGlossary(ctx, env.client, env.project)
	if err != nil {
		t.Fatal(err)
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
	fail = false
	out, err := g.ProcessResponse(ctx, in)
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
	env.client.GlossaryEntry.UpdateOneID(old.ID).SetTarget("manual later").ExecX(ctx)
	replayed, err := g.ProcessResponse(ctx, in)
	if err != nil || string(replayed.Body) != large || string(replayed.Receipt) != string(out.Receipt) {
		t.Fatalf("replay changed %v", err)
	}
}
