package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type stagedConcurrencyBackend struct {
	entered chan int
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
	revise  bool
	inspect func(backend.Request) error
}

func (*stagedConcurrencyBackend) Name() string { return "controlled-concurrency" }
func (*stagedConcurrencyBackend) Close() error { return nil }
func (b *stagedConcurrencyBackend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	if b.inspect != nil {
		if err := b.inspect(req); err != nil {
			return nil, err
		}
	}
	type segment struct {
		ID        string `json:"id"`
		Source    string `json:"source"`
		Translate bool   `json:"translate"`
	}
	var input struct {
		Segments json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal([]byte(req.User), &input); err != nil {
		return nil, err
	}
	var segments []segment
	if b.revise {
		if err := json.Unmarshal(input.Segments, &segments); err != nil {
			return nil, err
		}
	} else {
		var translated map[string]segment
		if err := json.Unmarshal(input.Segments, &translated); err != nil {
			return nil, err
		}
		for id, seg := range translated {
			seg.ID = id
			segments = append(segments, seg)
		}
	}
	n := b.active.Add(1)
	defer b.active.Add(-1)
	for old := b.peak.Load(); n > old; old = b.peak.Load() {
		if b.peak.CompareAndSwap(old, n) {
			break
		}
	}
	b.entered <- len(segments)
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	translations := map[string]string{}
	var revisions []map[string]string
	for _, seg := range segments {
		target := "accepted-" + seg.Source
		if seg.Translate {
			translations[seg.ID] = target
		}
		revisions = append(revisions, map[string]string{"id": seg.ID, "target": target})
	}
	var output any = map[string]any{"translations": translations}
	if b.revise {
		output = map[string]any{"revisions": revisions}
	}
	payload, err := json.Marshal(output)
	return &backend.Response{Text: string(payload)}, err
}

func TestStagedConcurrencyPreservesProtectedContextWordBudget(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 4}})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultCandidateLimits()
	// The fair member cap derives from S/C; S=17 and C=4 preserves n=4.
	limits.Segments = 17
	runtime := NewExecutionRuntime(a, limits, nil)
	defer runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	doc := newTestDoc(23)
	for i := range doc.Segments {
		doc.Segments[i].Source = "source"
		if i%3 == 2 {
			doc.Segments[i].Source += " `one two three four five six seven eight`"
		}
	}
	var rawContext, protectedTarget atomic.Bool
	var requests atomic.Int32
	violations := make(chan error, len(doc.Segments))
	remote := &stagedConcurrencyBackend{entered: make(chan int, len(doc.Segments)), release: make(chan struct{})}
	close(remote.release)
	remote.inspect = func(req backend.Request) error {
		var input struct {
			Segments map[string]struct {
				Source    string `json:"source"`
				Translate bool   `json:"translate"`
			} `json:"segments"`
		}
		if err := json.Unmarshal([]byte(req.User), &input); err != nil {
			return err
		}
		requests.Add(1)
		targets, words := 0, 0
		for _, seg := range input.Segments {
			words += CountWords(seg.Source)
			if seg.Translate {
				targets++
				if strings.Contains(seg.Source, "__LF_") {
					protectedTarget.Store(true)
				}
			} else if strings.Contains(seg.Source, "`one two") {
				rawContext.Store(true)
			}
		}
		if targets > 4 || targets > 1 && words > 20 {
			err := fmt.Errorf("actual request has %d targets and %d words, static cap=4 word cap=20", targets, words)
			violations <- err
			return err
		}
		return nil
	}
	h := &TranslateHandler{Backend: BindRequestBackend(remote, 1, 0, runtime), Renderer: newTestRenderer(t), BatchSize: 10, MaxWordsPerBatch: 20,
		Protector: &protect.CodeProtector{}, Context: ContextConfig{Enabled: true, Before: 1, After: 1}, Logger: quietLogger()}
	store := NewMemoryRoundStore(nil)
	result, err := RunRound(ctx, Round{Concurrency: 4, Handler: h, Runtime: runtime, Store: store}, doc, nil, quietLogger(), nil)
	select {
	case violation := <-violations:
		t.Fatal(violation)
	default:
	}
	if err != nil || len(result.Resolved) != len(doc.Segments) || len(result.Unresolved) != 0 {
		t.Fatalf("protected context round: %+v error=%v", result, err)
	}
	if requests.Load() < 2 || !rawContext.Load() || !protectedTarget.Load() {
		t.Fatalf("fixture did not exercise shortened batches: requests=%d raw_context=%v protected_target=%v", requests.Load(), rawContext.Load(), protectedTarget.Load())
	}
	if h.BatchSize != 10 || h.MaxWordsPerBatch != 20 {
		t.Fatal("scheduler mutated the handler's configured constraints")
	}
}

func TestStagedRoundConcurrencyUsesStaticWindowBudget(t *testing.T) {
	defaults := DefaultCandidateLimits()
	cases := []struct {
		name                       string
		mode                       string
		concurrency, batch, count  int
		roundIndex, wantConcurrent int
		limits                     CandidateLimits
	}{
		{"translate_serial", RoundModeTranslate, 1, 10, 35, 0, 1, defaults},
		{"translate_zero_concurrency", RoundModeTranslate, 0, 10, 35, 0, 1, defaults},
		{"translate_negative_concurrency", RoundModeTranslate, -4, 10, 35, 0, 1, defaults},
		{"translate_default_batch", RoundModeTranslate, 4, 10, 35, 0, 4, defaults},
		{"translate_two", RoundModeTranslate, 2, 10, 35, 0, 2, defaults},
		{"translate_large_batch", RoundModeTranslate, 4, 50, 65, 0, 2, defaults},
		{"translate_single_segments", RoundModeTranslate, 4, 1, 19, 0, 4, defaults},
		{"translate_window_bound", RoundModeTranslate, 20, 10, 35, 0, 4, defaults},
		{"translate_c32_handoff", RoundModeTranslate, 32, 50, 256, 0, 32, defaults},
		{"revise_c32_handoff", RoundModeRevise, 32, 50, 256, 0, 32, defaults},
		{"translate_max_int_concurrency", RoundModeTranslate, int(^uint(0) >> 1), 10, 35, 0, 35, defaults},
		{"translate_fewer_segments", RoundModeTranslate, 8, 10, 3, 0, 1, defaults},
		{"translate_second_round", RoundModeTranslate, 4, 10, 35, 1, 4, defaults},
		{"revise_default_batch", RoundModeRevise, 4, 10, 35, 0, 4, defaults},
		{"revise_second_round", RoundModeRevise, 2, 20, 35, 1, 2, defaults},
		{"small_byte_window", RoundModeTranslate, 4, 10, 9, 0, 4, CandidateLimits{Segments: 8, Bytes: 2 << 20, ItemBytes: 1 << 20}},
		{"small_segment_window", RoundModeTranslate, 4, 10, 9, 0, 1, CandidateLimits{Segments: 2, Bytes: 16 << 20, ItemBytes: 1 << 20}},
		{"singleton_exception", RoundModeTranslate, 4, 10, 3, 0, 1, CandidateLimits{Segments: 4, Bytes: 1, ItemBytes: 1 << 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1, tc.roundIndex: max(1, tc.concurrency)}})
			if err != nil {
				t.Fatal(err)
			}
			runtime := NewExecutionRuntime(a, tc.limits, nil)
			defer runtime.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			remote := &stagedConcurrencyBackend{entered: make(chan int, tc.count), release: make(chan struct{}), revise: tc.mode == RoundModeRevise}
			bound := BindRequestBackend(remote, 1, tc.roundIndex, runtime)
			doc := newTestDoc(tc.count)
			var handler RoundHandler = &TranslateHandler{Backend: bound, Renderer: newTestRenderer(t), BatchSize: tc.batch, RoundIndex: tc.roundIndex, Logger: quietLogger()}
			if remote.revise {
				for i := range doc.Segments {
					doc.Segments[i].Target = "old translation"
					doc.Segments[i].Status = "translated"
					doc.Segments[i].Issues = []qa.QualityIssue{{Code: "calque", Message: "revise this"}}
				}
				handler = &ReviseHandler{Backend: bound, Renderer: newReviseRenderer(t), BatchSize: tc.batch, RoundIndex: tc.roundIndex, IssueCodes: []string{"calque"}, Logger: quietLogger()}
			}
			store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: tc.roundIndex + 1}
			type outcome struct {
				result RunRoundResult
				err    error
			}
			done := make(chan outcome, 1)
			var releaseOnce sync.Once
			joined := false
			cleanup := func() {
				releaseOnce.Do(func() { close(remote.release) })
				cancel()
				if !joined {
					<-done
					joined = true
				}
			}
			defer cleanup()
			fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
			go func() {
				result, err := RunRound(ctx, Round{Concurrency: tc.concurrency, Handler: handler, Runtime: runtime, Store: store}, doc, nil, quietLogger(), nil)
				done <- outcome{result, err}
			}()
			// Each provider call holds its network slot until all expected calls
			// arrive. A serial scheduler cannot pass by merely finishing quickly.
			barrier := time.NewTimer(3 * time.Second)
			defer barrier.Stop()
			for entered := 0; entered < tc.wantConcurrent; entered++ {
				select {
				case n := <-remote.entered:
					if n < 1 || n > tc.batch {
						fail("request size %d exceeds configured maximum %d", n, tc.batch)
					}
				case <-barrier.C:
					fail("only %d provider calls started concurrently, want %d", entered, tc.wantConcurrent)
				}
			}
			if got := a.Snapshot().MainInflight[tc.roundIndex]; got != tc.wantConcurrent {
				fail("admitted requests=%d want=%d", got, tc.wantConcurrent)
			}
			window := runtime.Window.Snapshot()
			if window.Segments > tc.limits.Segments || (window.Bytes > tc.limits.Bytes && window.Segments != 1) {
				fail("window budget exceeded: %+v limits=%+v", window, tc.limits)
			}
			releaseOnce.Do(func() { close(remote.release) })
			var got outcome
			select {
			case got = <-done:
				joined = true
			case <-ctx.Done():
				fail("staged round did not complete")
			}
			if got.err != nil || len(got.result.Resolved) != tc.count || len(got.result.Unresolved) != 0 {
				t.Fatalf("incomplete round: %+v error=%v", got.result, got.err)
			}
			if peak := int(remote.peak.Load()); peak != tc.wantConcurrent {
				t.Fatalf("provider peak=%d want=%d", peak, tc.wantConcurrent)
			}
			t.Logf("configured C=%d, measured provider peak=%d, committed members=%d", tc.concurrency, remote.peak.Load(), len(got.result.Resolved))
			for i, seg := range doc.Segments {
				if want := "accepted-" + seg.Source; seg.Target != want {
					t.Errorf("segment %d target=%q want=%q", i, seg.Target, want)
				}
			}
			state, err := store.Load(ctx)
			if err != nil || len(state.Completed) != tc.count {
				t.Fatalf("committed members=%d want=%d error=%v", len(state.Completed), tc.count, err)
			}
			if window := runtime.Window.Snapshot(); window.Segments != 0 || window.Bytes != 0 {
				t.Fatalf("retained window: %+v", window)
			}
			if state := a.Snapshot(); state.MainInflight[tc.roundIndex] != 0 || state.PendingResults != 0 {
				t.Fatalf("retained admission: %+v", state)
			}
			if pending, queued := runtime.Preparation.Snapshot(); pending != 0 || queued != 0 {
				t.Fatalf("retained preparation: pending=%d queued=%d", pending, queued)
			}
		})
	}
}

func TestStagedAdjacentRoundsUseTheirOwnConcurrencyBudgets(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 2, 1: 4}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	defer runtime.Close()
	doc := newTestDoc(35)
	for roundIndex, concurrency := range []int{2, 4} {
		t.Run([]string{"translate", "revise"}[roundIndex], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			remote := &stagedConcurrencyBackend{entered: make(chan int, len(doc.Segments)), release: make(chan struct{}), revise: roundIndex == 1}
			bound := BindRequestBackend(remote, 1, roundIndex, runtime)
			var handler RoundHandler = &TranslateHandler{Backend: bound, Renderer: newTestRenderer(t), BatchSize: 10, RoundIndex: roundIndex, Logger: quietLogger()}
			if remote.revise {
				for i := range doc.Segments {
					doc.Segments[i].Issues = []qa.QualityIssue{{Code: "calque", Message: "revise this"}}
				}
				handler = &ReviseHandler{Backend: bound, Renderer: newReviseRenderer(t), BatchSize: 10, RoundIndex: roundIndex, IssueCodes: []string{"calque"}, Logger: quietLogger()}
			}
			store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: roundIndex + 1}
			done := make(chan error, 1)
			joined := false
			var releaseOnce sync.Once
			defer func() {
				releaseOnce.Do(func() { close(remote.release) })
				cancel()
				if !joined {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("adjacent round did not join")
					}
				}
			}()
			go func() {
				result, err := RunRound(ctx, Round{Concurrency: concurrency, Handler: handler, Runtime: runtime, Store: store}, doc, nil, quietLogger(), nil)
				if err == nil && len(result.Resolved) != len(doc.Segments) {
					err = errors.New("adjacent round left unresolved segments")
				}
				done <- err
			}()
			barrier := time.NewTimer(3 * time.Second)
			defer barrier.Stop()
			for n := 0; n < concurrency; n++ {
				select {
				case <-remote.entered:
				case <-barrier.C:
					t.Fatalf("round %d started %d concurrent requests, want %d", roundIndex, n, concurrency)
				}
			}
			if got := a.Snapshot().MainInflight[roundIndex]; got != concurrency {
				t.Fatalf("round %d admission=%d want=%d", roundIndex, got, concurrency)
			}
			releaseOnce.Do(func() { close(remote.release) })
			err := awaitRuntimeResult(t, done)
			joined = true
			if err != nil {
				t.Fatal(err)
			}
			if remote.peak.Load() != int32(concurrency) || runtime.Window.Snapshot().Segments != 0 || a.Snapshot().PendingResults != 0 {
				t.Fatal("adjacent round changed concurrency or retained capacity")
			}
		})
		if t.Failed() {
			break
		}
	}
}
