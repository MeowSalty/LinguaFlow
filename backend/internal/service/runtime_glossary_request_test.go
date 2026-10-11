package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func requestGlossaryFixture(t *testing.T) (*jobRoundTestEnv, *ent.WorkRequest, *DatabaseGlossary) {
	t.Helper()
	ctx := context.Background()
	env, j, jr, round, item := seedDurableJobWork(t)
	req := env.client.WorkRequest.Create().SetIdentity("inline-main").SetJobID(j.ID).SetResourceID(jr.QueryResource().OnlyIDX(ctx)).SetJobRoundID(round.ID).SetRetryEpoch(7).SetSegmentIds([]int{item.SegmentID}).SetStage("main").SetBackendID(1).SetBudgetModel("stage_separated").SetInputDigest("inline-input").SetState("received").SaveX(ctx)
	g, err := NewDatabaseGlossary(ctx, env.client, env.project)
	if err != nil {
		t.Fatal(err)
	}
	return env, req, g
}

func TestRequestGlossaryReplayPreservesOriginalConflictDecisions(t *testing.T) {
	ctx := context.Background()
	env, req, g := requestGlossaryFixture(t)
	existing := env.client.GlossaryEntry.Create().SetProjectID(env.project.ID).SetSourceKey("sdk").SetSource("SDK").SetTarget("原规范").SaveX(ctx)
	entries := []glossary.Entry{{Source: "API", Target: "接口"}, {Source: "SDK", Target: "候选词"}}
	result, err := glossary.AddForRequest(ctx, g, req.Identity, entries...)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Added) != 1 || len(result.Skipped) != 1 || result.Skipped[0].Existing.Target != "原规范" {
		t.Fatalf("first result=%+v", result)
	}
	added := env.client.GlossaryEntry.Query().Where(glossaryentry.ProjectIDEQ(env.project.ID), glossaryentry.SourceKeyEQ("api")).OnlyX(ctx)
	env.client.GlossaryEntry.UpdateOneID(existing.ID).SetTarget("新规范").ExecX(ctx)
	env.client.GlossaryEntry.UpdateOneID(added.ID).SetTarget("手动编辑").ExecX(ctx)
	restarted, err := NewDatabaseGlossary(ctx, env.client, env.project)
	if err != nil {
		t.Fatal(err)
	}
	env.client.Job.UpdateOneID(req.JobID).SetStatus(JobStatusCancelled).ExecX(ctx)
	for _, target := range []*DatabaseGlossary{g, restarted} {
		replayed, err := target.AddForRequest(ctx, req.Identity, entries...)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result, replayed) {
			t.Fatalf("replay changed decisions: %+v", replayed)
		}
		matches, err := target.Lookup(ctx, "API SDK", "en", "zh")
		if err != nil || len(matches) != 2 {
			t.Fatalf("cache=%+v %v", matches, err)
		}
		if matches[0].Target != "手动编辑" || matches[1].Target != "新规范" {
			t.Fatal("receipt replay reintroduced old glossary contents")
		}
	}
	if len(env.client.WorkRequest.GetX(ctx, req.ID).GlossaryReceipt) == 0 || env.client.GlossaryEntry.Query().CountX(ctx) != 2 {
		t.Fatal("glossary receipt missing or duplicate entry added")
	}
}

func TestRequestGlossaryRollbackDoesNotMutateRuntimeCache(t *testing.T) {
	ctx := context.Background()
	env, req, g := requestGlossaryFixture(t)
	fail := true
	injected := errors.New("glossary receipt fault")
	env.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := m.Field("glossary_receipt"); ok && fail {
				return nil, injected
			}
			return next.Mutate(ctx, m)
		})
	})
	result, err := g.AddForRequest(ctx, req.Identity, glossary.Entry{Source: "API", Target: "接口"})
	if !errors.Is(err, injected) || len(result.Added) != 0 {
		t.Fatalf("failed add=%+v %v", result, err)
	}
	if env.client.GlossaryEntry.Query().CountX(ctx) != 0 || len(env.client.WorkRequest.GetX(ctx, req.ID).GlossaryReceipt) != 0 {
		t.Fatal("partial glossary escaped rollback")
	}
	if matches, _ := g.Lookup(ctx, "API", "en", "zh"); len(matches) != 0 {
		t.Fatal("uncommitted glossary leaked into runtime cache")
	}
	fail = false
	result, err = g.AddForRequest(ctx, req.Identity, glossary.Entry{Source: "API", Target: "接口"})
	if err != nil || len(result.Added) != 1 {
		t.Fatalf("retry=%+v %v", result, err)
	}
}

func TestRequestGlossaryRejectsChangedIdentityOrScope(t *testing.T) {
	ctx := context.Background()
	env, req, g := requestGlossaryFixture(t)
	entries := []glossary.Entry{{Source: "API", Target: "接口"}}
	if _, err := g.AddForRequest(ctx, req.Identity, entries...); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddForRequest(ctx, req.Identity, glossary.Entry{Source: "API", Target: "别的输入"}); !errors.Is(err, workstate.ErrManifest) {
		t.Fatalf("changed input=%v", err)
	}
	otherProject := createTestProject(t, env.client, "other glossary", env.user.ID)
	other, err := NewDatabaseGlossary(ctx, env.client, otherProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.AddForRequest(ctx, req.Identity, entries...); !errors.Is(err, workstate.ErrManifest) {
		t.Fatalf("foreign receipt=%v", err)
	}
}

func TestRequestGlossaryRejectsNewEffectAfterCancelOrSourceChange(t *testing.T) {
	for _, cause := range []string{"cancel", "source"} {
		t.Run(cause, func(t *testing.T) {
			ctx := context.Background()
			env, req, g := requestGlossaryFixture(t)
			if cause == "cancel" {
				env.client.Job.UpdateOneID(req.JobID).SetStatus(JobStatusCancelled).ExecX(ctx)
			} else {
				env.client.Resource.UpdateOneID(req.ResourceID).AddSourceGeneration(1).ExecX(ctx)
			}
			if _, err := g.AddForRequest(ctx, req.Identity, glossary.Entry{Source: "API", Target: "接口"}); err == nil {
				t.Fatal("late response acquired new glossary effect")
			}
			if env.client.GlossaryEntry.Query().CountX(ctx) != 0 || len(env.client.WorkRequest.GetX(ctx, req.ID).GlossaryReceipt) != 0 {
				t.Fatal("rejected side effect was persisted")
			}
		})
	}
}

func TestConcurrentRequestGlossaryReplayAddsOnce(t *testing.T) {
	ctx := context.Background()
	env, req, left := requestGlossaryFixture(t)
	right, err := NewDatabaseGlossary(ctx, env.client, env.project)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result glossary.AddResult
		err    error
	}
	done := make(chan outcome, 2)
	for _, g := range []*DatabaseGlossary{left, right} {
		go func(g *DatabaseGlossary) {
			result, err := g.AddForRequest(ctx, req.Identity, glossary.Entry{Source: "API", Target: "接口"})
			done <- outcome{result, err}
		}(g)
	}
	first, second := <-done, <-done
	if first.err != nil || second.err != nil || !reflect.DeepEqual(first.result, second.result) || len(first.result.Added) != 1 || env.client.GlossaryEntry.Query().CountX(ctx) != 1 {
		t.Fatalf("concurrent adds=%+v %+v", first, second)
	}
}
