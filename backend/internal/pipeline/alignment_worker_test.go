package pipeline

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

type alignmentCommitProbe struct {
	*MemoryRoundStore
	runtime    *ExecutionRuntime
	commitErr  error
	finishErr  error
	mu         sync.Mutex
	records    []string
	atCommit   []string
	capacity   backend.RequestAdmissionSnapshot
	parseSlots int
	body       string
	atSave     []string
	saved      backend.RequestAdmissionSnapshot
	saveParse  int
}

func (s *alignmentCommitProbe) Save(ctx context.Context, c *Candidate) error {
	if c.LastAlignmentRequestID != "" {
		s.mu.Lock()
		s.atSave = slices.Clone(s.records)
		s.mu.Unlock()
		s.saved = s.runtime.Admission.Snapshot()
		s.saveParse = len(s.runtime.parse)
	}
	return s.MemoryRoundStore.Save(ctx, c)
}

func (s *alignmentCommitProbe) Record(_ context.Context, _ string, record RequestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.State == "completed" && s.finishErr != nil {
		return terminalRuntimeStoreError{s.finishErr}
	}
	s.records = append(s.records, record.State)
	return nil
}

func (s *alignmentCommitProbe) Commit(ctx context.Context, c *Candidate, result TranslatedSegment) (CommitOutcome, error) {
	s.mu.Lock()
	s.atCommit = slices.Clone(s.records)
	s.mu.Unlock()
	s.capacity = s.runtime.Admission.Snapshot()
	s.parseSlots = len(s.runtime.parse)
	s.body = result.TargetText
	full, err := s.MemoryRoundStore.Candidate(ctx, c.ID, s.runtime.Window.limits.ItemBytes)
	if err != nil || full.Segment.Target != "alpha" || len(full.Alignment.Verified) != 1 {
		return "", terminalRuntimeStoreError{errors.New("alignment rewrote durable plain candidate before final commit")}
	}
	w := s.runtime.Window.Snapshot()
	p := s.runtime.Admission.PipelineSnapshot()
	if w.Segments < 1 || w.Bytes < 1 || p.CandidateBytes+p.GrowthBytes < int64(w.Segments)*s.runtime.Window.limits.ItemBytes {
		return "", terminalRuntimeStoreError{errors.New("candidate S/W/fullQ released before final commit")}
	}
	if s.commitErr != nil {
		return "", terminalRuntimeStoreError{s.commitErr}
	}
	return s.MemoryRoundStore.Commit(ctx, c, result)
}

func TestStagedAlignmentWorkerResponseEndsAtDurableSaveCandidateCreditReachesCommit(t *testing.T) {
	for _, failure := range []string{"none", "commit", "completion_record"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			runtime := stagedTestRuntime(t, DefaultCandidateLimits())
			store := &alignmentCommitProbe{MemoryRoundStore: NewMemoryRoundStore(nil), runtime: runtime}
			injected := errors.New("storage failed at " + failure)
			if failure == "commit" {
				store.commitErr = injected
			}
			if failure == "completion_record" {
				store.finishErr = injected
			}
			c := newCandidate(Segment{ID: "one", Source: "source", Status: "pending"}, 0, RoundModeTranslate, "text")
			c.Segment.Target = "alpha"
			var err error
			c.Alignment, err = PrepareAlignment("alpha", "text", []ruby.Item{{ID: "1", SourceBase: "source"}}, nil, ruby.ProtocolV2, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Window.Restore(c.ID, c.StoredBytes); err != nil {
				t.Fatal(err)
			}
			remote := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: `{"ruby_output":[{"id":"1","base":"alpha","text":"reading","kind":"creative","occurrence":1}]}`}, nil
			}}
			round := Round{Handler: &TranslateHandler{
				RubyRetryBackends: []backend.Backend{BindRequestBackend(remote, 1, 0, runtime)},
				RubyRetryAttempts: 1, RubyTemplates: testRubyTemplates(),
			}}
			result := processCandidate(ctx, round, c, store, runtime, quietLogger(), progress.Nop{})
			if failure == "none" && result.err != nil || failure != "none" && !errors.Is(result.err, injected) {
				t.Fatalf("commit/finish error propagation: %v", result.err)
			}
			if !slices.Equal(store.atSave, []string{"sent", "received"}) || store.saved.PendingResults != 1 || store.saved.AlignmentInflight != 0 || store.saveParse != 0 {
				t.Fatalf("response owner ended before durable candidate: records=%v capacity=%+v parsing=%d", store.atSave, store.saved, store.saveParse)
			}
			if failure == "completion_record" {
				if len(store.atCommit) != 0 {
					t.Fatal("failed response completion nevertheless committed segment")
				}
			} else if !slices.Equal(store.atCommit, []string{"sent", "received", "completed"}) || store.capacity.PendingResults != 0 || store.capacity.AlignmentInflight != 0 || store.parseSlots != 0 || store.body != "<ruby>alpha<rt>reading</rt></ruby>" {
				t.Fatalf("final commit boundary changed: records%v capacity%+v parsing%d body%q", store.atCommit, store.capacity, store.parseSlots, store.body)
			}
			wantRecords := []string{"sent", "received"}
			if failure != "completion_record" {
				wantRecords = append(wantRecords, "completed")
			}
			if !slices.Equal(store.records, wantRecords) {
				t.Fatalf("failed processing claimed completed request: %v", store.records)
			}
			state, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantAccepted := 1
			if failure != "none" {
				wantAccepted = 0
			}
			if len(state.Completed) != wantAccepted || (result.completed != nil) != (wantAccepted == 1) {
				t.Fatalf("confirmation outcome differs from transaction: completed=%v result=%+v", state.Completed, result)
			}
			if failure != "none" {
				full, err := store.Candidate(ctx, c.ID, runtime.Window.limits.ItemBytes)
				if err != nil || !full.Ready || len(full.Alignment.Verified) != 1 || full.Segment.Target != "alpha" {
					t.Fatalf("durable response/candidate lost on terminal write failure: %+v %v", full, err)
				}
			}
			if capacity := runtime.Admission.Snapshot(); capacity.PendingResults != 0 || capacity.AlignmentInflight != 0 || len(runtime.parse) != 0 {
				t.Fatalf("completion cleanup leaked capacity: %+v", capacity)
			}
		})
	}
}
