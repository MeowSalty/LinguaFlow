package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func scheduleCandidate(t *testing.T, index int) *Candidate {
	t.Helper()
	c := newCandidate(Segment{ID: fmt.Sprint(index), Source: "原 hello"}, index, RoundModeTranslate, "text")
	c.Segment.Target = "alpha beta"
	var err error
	c.Alignment, err = ruby.NewAlignmentState(c.Segment.Target, "text", []ruby.Item{{ID: "1", SourceBase: "原", SourceText: "ひら"}}, nil, ruby.ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAlignmentBatchContentLimits(t *testing.T) {
	first, second, third := scheduleCandidate(t, 0), scheduleCandidate(t, 1), scheduleCandidate(t, 2)
	if got := alignmentWords(first); got != 9 {
		t.Fatalf("actual duplicated view and missing text: %d, want 9", got)
	}
	for _, tc := range []struct {
		name                  string
		segments, words, want int
	}{
		{"segments", 2, 0, 2}, {"words", 0, 18, 2}, {"both", 3, 17, 1}, {"oversized_single", 8, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch, full := readyAlignmentBatch([]*Candidate{first, second, third}, AlignmentBatchConfig{BatchSize: tc.segments, MaxWordsPerBatch: tc.words, ProtocolVersion: ruby.BatchProtocolVersion})
			if len(batch) != tc.want || !full {
				t.Fatalf("batch=%d full=%v", len(batch), full)
			}
		})
	}
	first.Alignment.ApplyOutput([]string{"1"}, []ruby.OutputEntry{{ID: "1", Base: "alpha", Text: "reading", Kind: "creative", Occurrence: 1}}, true)
	if got := alignmentWords(first); got != 6 {
		t.Fatalf("verified missing was still charged: %d", got)
	}
	second.PoolIndex = 1
	batch, full := readyAlignmentBatch([]*Candidate{first, second, third}, AlignmentBatchConfig{BatchSize: 2, ProtocolVersion: ruby.BatchProtocolVersion})
	if len(batch) != 2 || batch[1] != third || !full {
		t.Fatal("different pools grouped or noncontiguous members excluded")
	}
	first.ForceSingleAlignment = true
	batch, full = readyAlignmentBatch([]*Candidate{first, third}, AlignmentBatchConfig{BatchSize: 8, ProtocolVersion: ruby.BatchProtocolVersion})
	if len(batch) != 1 || !full {
		t.Fatal("isolated candidate waited for or joined a batch")
	}
}

type scheduleBackend struct {
	call func(context.Context, backend.Request) (*backend.Response, error)
}

func (b *scheduleBackend) Name() string { return "schedule" }
func (b *scheduleBackend) Close() error { return nil }
func (b *scheduleBackend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	return b.call(ctx, req)
}

func TestAlignmentBatchSchedulingBoundaries(t *testing.T) {
	for _, name := range []string{"deadline", "full", "tail", "pause_waiting"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			secondStarted, releaseMain, aligned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce, alignedOnce sync.Once
			defer releaseOnce.Do(func() { close(releaseMain) })
			var mainCalls, alignmentCalls atomic.Int32
			main := &scheduleBackend{call: func(ctx context.Context, req backend.Request) (*backend.Response, error) {
				if mainCalls.Add(1) == 2 {
					close(secondStarted)
					select {
					case <-releaseMain:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				var input struct {
					Segments map[string]prompt.SegmentDetail `json:"segments"`
				}
				if err := json.Unmarshal([]byte(req.User), &input); err != nil {
					return nil, err
				}
				translations := make(map[string]string)
				for id, segment := range input.Segments {
					if segment.Translate {
						translations[id] = "alpha beta"
					}
				}
				data, err := json.Marshal(map[string]any{"translations": translations})
				return &backend.Response{Text: string(data)}, err
			}}
			alignment := &scheduleBackend{call: func(ctx context.Context, req backend.Request) (*backend.Response, error) {
				alignmentCalls.Add(1)
				alignedOnce.Do(func() { close(aligned) })
				var input struct {
					Alignments []struct {
						WorkID      string `json:"work_id"`
						CandidateID string `json:"candidate_id"`
					} `json:"alignments"`
				}
				if err := json.Unmarshal([]byte(req.User), &input); err != nil {
					return nil, err
				}
				rows := []map[string]any{}
				for _, m := range input.Alignments {
					rows = append(rows, map[string]any{"work_id": m.WorkID, "candidate_id": m.CandidateID, "ruby_output": []map[string]any{{"id": "1", "base": "alpha", "text": "reading", "kind": "creative", "occurrence": 1}}})
				}
				data, err := json.Marshal(map[string]any{"alignments": rows})
				return &backend.Response{Text: string(data)}, err
			}}
			doc := newTestDoc(4)
			doc.Format = "text"
			if name == "tail" {
				doc.Segments = doc.Segments[:2]
			}
			for i := range doc.Segments {
				doc.Segments[i].Meta = map[string]any{"ruby_items": []ruby.Item{{ID: "1", SourceBase: "source", SourceText: "reading"}}}
			}
			h := stagedTestHandler(t, r, main)
			h.RubyEnabled, h.RubyProtocolVersion, h.RubyRetryAttempts = true, ruby.ProtocolV2, 1
			h.RubyRetryBackends = []backend.Backend{BindRequestBackend(alignment, 2, 0, r)}
			h.RubyTemplates = prompt.RubyTemplates{JSON: prompt.RubyAlignmentJSONTemplate, Text: prompt.RubyAlignmentTextTemplate, BatchJSON: prompt.RubyAlignmentBatchJSONTemplate, BatchText: prompt.RubyAlignmentBatchTextTemplate}
			h.RubyBatch = AlignmentBatchConfig{BatchSize: 8, Wait: time.Minute, ProtocolVersion: ruby.BatchProtocolVersion}
			if name == "deadline" {
				h.RubyBatch.Wait = 20 * time.Millisecond
			}
			if name == "full" {
				h.RubyBatch.BatchSize = 2
			}
			done := make(chan error, 1)
			go func() {
				_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h}, doc, NewMemoryRoundStore(nil), r, quietLogger(), progress.Nop{})
				done <- err
			}()
			if name != "tail" {
				awaitSignal(t, secondStarted)
			}
			if name == "pause_waiting" {
				// A one-minute timer cannot race this barrier; the second main
				// invocation proves the executor has reached the waiting batch.
				r.Gate.Pause()
				if alignmentCalls.Load() != 0 || r.Admission.Snapshot().AlignmentInflight != 0 {
					t.Fatal("waiting batch consumed an alignment request")
				}
			} else {
				awaitSignal(t, aligned)
			}
			releaseOnce.Do(func() { close(releaseMain) })
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("batch timer or lifecycle did not drain")
			}
			if name == "pause_waiting" && alignmentCalls.Load() != 0 {
				t.Fatal("pause dispatched waiting alignment")
			}
			if name != "pause_waiting" {
				for _, segment := range doc.Segments {
					if segment.Target != controlledTarget {
						t.Fatalf("unconfirmed result: %q", segment.Target)
					}
				}
			}
		})
	}
}

func TestAlignmentBatchControlledHTTP(t *testing.T) {
	for _, tc := range controlledCases[9:] {
		for _, same := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same_backend=%v", tc.name, same), func(t *testing.T) {
				f := newControlledHTTP(t, same, 0)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				doc, err := runControlledPipeline(ctx, f, tc)
				if err != nil {
					t.Fatal(err)
				}
				m := checkControlledRun(t, f, doc, tc)
				if m.alignmentCalls >= controlledSegments || m.maxAlignmentBatch < 2 {
					t.Fatalf("no actual cross-paragraph requests: %+v", m)
				}
				if doc.InputTokens+doc.OutputTokens != int64(m.tokens) {
					t.Fatalf("tokens duplicated per member: document=%d HTTP=%d", doc.InputTokens+doc.OutputTokens, m.tokens)
				}
			})
		}
	}
}
