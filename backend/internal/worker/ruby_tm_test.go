package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

type rubyTMEntry struct {
	source, target, sourceLang, targetLang string
}

type rubyCountingTM struct {
	mu          sync.Mutex
	searches    int
	entries     []rubyTMEntry
	observe     func(context.Context, rubyTMEntry) error
	observedErr error
	addErr      error
}

func (m *rubyCountingTM) Search(context.Context, string, string, string) ([]tm.Match, error) {
	m.mu.Lock()
	m.searches++
	m.mu.Unlock()
	return nil, nil
}

func (m *rubyCountingTM) Add(ctx context.Context, source, target, sourceLang, targetLang string) error {
	entry := rubyTMEntry{source, target, sourceLang, targetLang}
	err := m.observe(ctx, entry)
	m.mu.Lock()
	m.entries = append(m.entries, entry)
	m.observedErr = errors.Join(m.observedErr, err)
	m.mu.Unlock()
	return errors.Join(err, m.addErr)
}

func (m *rubyCountingTM) snapshot() (int, []rubyTMEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.searches, slices.Clone(m.entries), m.observedErr
}

// Inject the same memory into the handler and the durable store so this test
// also catches an accidental write while the main response is being prepared.
func executeRubyTMFixture(t *testing.T, f *rubyPipelineFixture, memory tm.TranslationMemory, afterSave func(context.Context, *pipeline.Candidate) error) (pipeline.RunRoundResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, pipeline.NewPauseGate(), pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	ctx = pipeline.WithExecutionRuntime(ctx, runtime)
	eng, err := f.factory.BuildEngine(ctx, f.snapshot, engine.RuntimeResources{TM: memory}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, durable := f.round(t, 0)
	durable.memory = memory
	eng.PrepareDocument(doc, nil)
	store := &rubyPipelineFaultStore{roundStore: durable, afterSave: afterSave}
	return pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
}

func TestRubyTMReceivesOnlyCommittedTargetsAndDoesNotReplay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		warning bool
		failTM  bool
	}{
		{name: "complete"},
		{name: "translate_warning", warning: true},
		{name: "best_effort_failure", failTM: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRubyPipelineFixture(t, 1)
			f.snapshot.TMEnabled = true
			f.persistSnapshot(t)
			f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
				if len(request.Missing) == 2 {
					request.Missing = request.Missing[:1]
				} else if tc.warning && len(request.Missing) == 1 {
					return map[string]any{"ruby_output": []ruby.OutputEntry{}}, nil
				}
				return rubyPipelineResponse(request), nil
			}
			ctx := context.Background()
			before := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(ctx)
			wantTarget := rubyPipelineTarget
			if tc.warning {
				wantTarget = "<ruby>alpha<rt>reading-a</rt></ruby> beta"
			}
			memory := &rubyCountingTM{}
			if tc.failTM {
				memory.addErr = errors.New("injected TM outage")
			}
			// The callback reads through the real SQLite client, after Commit
			// has returned its transaction. An early TM write cannot pass.
			memory.observe = func(ctx context.Context, entry rubyTMEntry) error {
				row, err := f.client.Segment.Get(ctx, before.ID)
				if err != nil {
					return err
				}
				if row.TargetText == nil || *row.TargetText != entry.target || entry.target != wantTarget || row.Status != segment.StatusTranslated || row.ContentVersion != before.ContentVersion+1 {
					return fmt.Errorf("TM write preceded the accepted target: entry=%+v segment=%+v", entry, row)
				}
				warning := slices.ContainsFunc(row.QualityIssues, func(issue qa.QualityIssue) bool {
					return issue.Code == qa.CodeRubyRestoreIncomplete && issue.Severity == qa.SeverityWarning
				})
				if warning != tc.warning {
					return fmt.Errorf("committed warning=%t, want %t: %+v", warning, tc.warning, row.QualityIssues)
				}
				checkpoint, err := f.client.JobRoundSegment.Query().Where(
					jobroundsegment.JobRoundIDEQ(f.roundIDs[0]), jobroundsegment.SegmentIDEQ(before.ID),
				).Only(ctx)
				if err != nil {
					return fmt.Errorf("TM write has no durable checkpoint: %w", err)
				}
				if checkpoint.CommitID == nil || checkpoint.CandidateID == nil || *checkpoint.CommitID != *checkpoint.CandidateID || checkpoint.Outcome != string(pipeline.CommitAccepted) {
					return fmt.Errorf("TM write has an invalid checkpoint: %+v", checkpoint)
				}
				candidate, err := f.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(*checkpoint.CandidateID)).WithWorkItem().Only(ctx)
				if err != nil {
					return err
				}
				if candidate.State != "completed" || candidate.PayloadBytes != 0 || len(candidate.Payload) != 0 || candidate.Edges.WorkItem.State != "resolved" {
					return fmt.Errorf("TM write preceded candidate completion: %+v", candidate)
				}
				round, err := f.client.JobRound.Get(ctx, f.roundIDs[0])
				if err != nil {
					return err
				}
				if round.SegmentCompleted != 1 {
					return fmt.Errorf("TM write preceded progress commit: %d", round.SegmentCompleted)
				}
				return nil
			}
			var savedMu sync.Mutex
			var sawMain, sawPartial bool
			var ready *pipeline.Candidate
			afterSave := func(ctx context.Context, c *pipeline.Candidate) error {
				if _, entries, _ := memory.snapshot(); len(entries) != 0 {
					return permanentStoreError{fmt.Errorf("candidate save wrote TM before Commit: %+v", entries)}
				}
				row, err := f.client.Segment.Get(ctx, before.ID)
				if err != nil {
					return err
				}
				if row.TargetText != nil || row.ContentVersion != before.ContentVersion || row.Status != before.Status {
					return permanentStoreError{fmt.Errorf("candidate save changed the official segment: %+v", row)}
				}
				checkpoints, err := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).Count(ctx)
				if err != nil {
					return err
				}
				if checkpoints != 0 || c.Alignment == nil {
					return permanentStoreError{fmt.Errorf("candidate save checkpoints=%d alignment=%+v", checkpoints, c.Alignment)}
				}
				savedMu.Lock()
				defer savedMu.Unlock()
				sawMain = sawMain || (!c.Ready && c.LogicalAttempt == 0 && len(c.Alignment.Verified) == 0)
				sawPartial = sawPartial || (!c.Ready && c.LogicalAttempt == 1 && len(c.Alignment.Verified) == 1)
				if c.Ready {
					payload, err := c.Encode()
					if err != nil {
						return permanentStoreError{err}
					}
					ready, err = pipeline.DecodeCandidate(payload)
					if err != nil {
						return permanentStoreError{err}
					}
				}
				return nil
			}
			result, err := executeRubyTMFixture(t, f, memory, afterSave)
			if err != nil || len(result.Resolved) != 1 || len(result.Unresolved) != 0 {
				t.Fatalf("translation result=%+v error=%v", result, err)
			}
			savedMu.Lock()
			mainSaved, partialSaved, savedReady := sawMain, sawPartial, ready
			savedMu.Unlock()
			if !mainSaved || !partialSaved || savedReady == nil {
				t.Fatalf("unobserved candidate boundary: main=%t partial=%t ready=%t", mainSaved, partialSaved, savedReady != nil)
			}
			assertMemory := func() {
				t.Helper()
				searches, entries, err := memory.snapshot()
				if err != nil {
					t.Fatalf("TM observed an uncommitted result: %v", err)
				}
				if searches == 0 {
					t.Fatal("counting TM was not used by the main translation handler")
				}
				want := rubyTMEntry{before.SourceText, wantTarget, f.snapshot.SourceLang, f.snapshot.TargetLang}
				if len(entries) != 1 || entries[0] != want {
					t.Fatalf("TM writes=%+v, want exactly %+v", entries, want)
				}
			}
			assertMemory()
			// Reuse the pre-commit DTO so this exercises durable commit-ID
			// lookup even with its now-outdated segment baseline.
			final, accepted, err := pipeline.FinalizeCandidate(savedReady)
			if err != nil || !accepted {
				t.Fatalf("finalize saved candidate: accepted=%t error=%v", accepted, err)
			}
			_, replayStore := f.round(t, 0)
			replayStore.memory = memory
			outcome, err := replayStore.Commit(ctx, savedReady, final)
			if err != nil || outcome != pipeline.CommitExisting {
				t.Fatalf("same commit ID replay: outcome=%s error=%v", outcome, err)
			}
			assertMemory()
			for replay := range 2 {
				if err := f.jobs.PrepareRecovery(ctx); err != nil {
					t.Fatal(err)
				}
				result, err := executeRubyTMFixture(t, f, memory, nil)
				if err != nil || len(result.Resolved) != 1 || len(result.Unresolved) != 0 {
					t.Fatalf("recovery %d result=%+v error=%v", replay, result, err)
				}
				assertMemory()
			}
			main, alignment, _, _ := f.calls()
			if main != 1 || alignment != 2 {
				t.Fatalf("HTTP main/alignment=%d/%d, want 1/2 without replay", main, alignment)
			}
			if count := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(ctx); count != 1 {
				t.Fatalf("acceptance checkpoints=%d, want 1", count)
			}
			f.assertUsage(t, 3)
		})
	}
}
