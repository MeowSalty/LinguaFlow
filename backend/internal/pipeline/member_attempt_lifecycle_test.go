package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type finiteRecoveryStore struct {
	*MemoryRoundStore
	seed      bool
	mode      string
	mu        sync.Mutex
	deadlines []time.Time
}

func (s *finiteRecoveryStore) note(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadlines = append(s.deadlines, persistence.Deadline(ctx))
}
func (s *finiteRecoveryStore) PublishOutputs(ctx context.Context, in OutputPublication) (MainResponseHeader, error) {
	if s.seed {
		return MainResponseHeader{}, terminalRuntimeStoreError{errors.New("fixture capture saved before restart")}
	}
	s.note(ctx)
	if s.mode == "checkpoint" && in.Complete {
		<-ctx.Done()
		return MainResponseHeader{}, ctx.Err()
	}
	return s.MemoryRoundStore.PublishOutputs(ctx, in)
}
func (s *finiteRecoveryStore) PublishBusiness(ctx context.Context, id string, out glossary.ResponseBusiness, install func()) (MainResponseHeader, error) {
	s.note(ctx)
	return s.MemoryRoundStore.PublishBusiness(ctx, id, out, install)
}
func (s *finiteRecoveryStore) PromoteMember(ctx context.Context, key MemberKey, c *Candidate, next WorkCursor, failure MemberOutcome) (MainResponseHeader, error) {
	s.note(ctx)
	if s.mode == "promotion" && key.Ordinal == 1 {
		<-ctx.Done()
		return MainResponseHeader{}, ctx.Err()
	}
	return s.MemoryRoundStore.PromoteMember(ctx, key, c, next, failure)
}

func TestMemberRecoveryAttemptKeepsOneDeadlineAcrossCheckpointAndPromotion(t *testing.T) {
	for _, mode := range []string{"checkpoint", "promotion"} {
		t.Run(mode, func(t *testing.T) {
			const n = 32
			first := stagedTestRuntime(t, DefaultCandidateLimits())
			store := &finiteRecoveryStore{MemoryRoundStore: NewMemoryRoundStore(nil), seed: true, mode: mode}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			translations := map[string]string{}
			for i := 0; i < n; i++ {
				translations[fmt.Sprint(i+1)] = fmt.Sprintf("saved-%d", i)
			}
			payload, err := json.Marshal(map[string]any{"translations": translations})
			if err != nil {
				t.Fatal(err)
			}
			main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: string(payload), Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
			}}
			doc := newTestDoc(n)
			h := stagedTestHandler(t, first, main)
			h.BatchSize = 0
			h.MaxWordsPerBatch = 4000
			if _, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: h}, doc, store, first, quietLogger(), nil); !storageFailure(err) {
				t.Fatalf("missing initial saved capture fault: %v", err)
			}
			if err := first.CloseDocument(doc); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			store.seed = false
			second := stagedTestRuntime(t, DefaultCandidateLimits())
			second.Policy.HandoffBudget = time.Second
			doc = newTestDoc(n)
			doc.InputTokens = 17
			doc.OutputTokens = 3
			h = stagedTestHandler(t, second, main)
			h.BatchSize = 0
			h.MaxWordsPerBatch = 4000
			_, err = RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: h}, doc, store, second, quietLogger(), nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("recovery did not stop at finite response deadline: %v", err)
			}
			headers, _, err := store.ResponseHeaders(t.Context(), 0, 16)
			if err != nil || len(headers) != 1 || main.calls.Load() != 1 {
				t.Fatalf("recovery lost reliable capture or resent model: %v", err)
			}
			header := headers[0]
			if mode == "checkpoint" && (header.ArchiveCursor != 16 || header.Cursor != 0 || header.Phase != "capture_pending") {
				t.Fatalf("checkpoint was not preserved: %+v", header)
			}
			if mode == "promotion" && (header.ArchiveCursor != n || header.Cursor != 1 || header.Phase != "business_ready") {
				t.Fatalf("promotion was not preserved: %+v", header)
			}
			store.mu.Lock()
			deadlines := append([]time.Time(nil), store.deadlines...)
			store.mu.Unlock()
			if len(deadlines) < 2 || deadlines[0].IsZero() {
				t.Fatal("missing bounded recovery saves")
			}
			for _, d := range deadlines {
				if !d.Equal(deadlines[0]) {
					t.Fatalf("response attempt refreshed deadline: first=%v got=%v", deadlines[0], d)
				}
			}
			if err := second.CloseDocument(doc); err != nil {
				t.Fatal(err)
			}
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
			store.mode = "complete"
			third := stagedTestRuntime(t, DefaultCandidateLimits())
			doc = newTestDoc(n)
			doc.InputTokens = 17
			doc.OutputTokens = 3
			t.Cleanup(func() {
				if err := third.CloseDocument(doc); err != nil {
					t.Error(err)
				}
			})
			h = stagedTestHandler(t, third, main)
			h.BatchSize = 0
			h.MaxWordsPerBatch = 4000
			result, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: h}, doc, store, third, quietLogger(), nil)
			if err != nil || len(result.Resolved) != n || main.calls.Load() != 1 || doc.InputTokens != 17 || doc.OutputTokens != 3 {
				t.Fatalf("new finite attempt could not resume: resolved=%d calls=%d err=%v", len(result.Resolved), main.calls.Load(), err)
			}
			for i, seg := range doc.Segments {
				if seg.Target != fmt.Sprintf("saved-%d", i) || seg.Status != "translated" || !seg.RoundWork.Completed || seg.TargetIsNull {
					t.Fatalf("recovery skipped confirmed body at %d: %+v", i, seg)
				}
			}
		})
	}
}

func TestMemberRuntimeCloseKeepsFailedOwnerForRetry(t *testing.T) {
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "close-retry"}); err != nil {
		t.Fatal(err)
	}
	store := &closeBorrowingStore{memoryPromotionBoundaryStore: &memoryPromotionBoundaryStore{MemoryRoundStore: NewMemoryRoundStore(nil), boundary: "before_write", cause: permanentCommitFailure{errors.New("fixture save failure")}}}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	doc := newTestDoc(1)
	t.Cleanup(func() {
		if err := r.CloseDocument(doc); err != nil {
			t.Error(err)
		}
	})
	probe := &windowBatchProbe{batchSize: 0}
	if _, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: probe.handler(t, r)}, doc, store, r, quietLogger(), nil); !errors.Is(err, store.cause) {
		t.Fatal(err)
	}
	err = r.Close()
	var lifecycle *backend.ProviderAllocationLifecycleError
	if !errors.As(err, &lifecycle) || len(r.mainOwners) != 1 {
		t.Fatalf("Close discarded outstanding owner or lost error: %v", err)
	}
	store.borrower = nil
	store.allocation.Release()
	store.bounds.CloseRetired()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if len(r.mainOwners) != 0 || len(store.requests) != 1 {
		t.Fatal("Close retry kept live attempt or deleted independent durable facts")
	}
}

type closeBorrowingStore struct {
	*memoryPromotionBoundaryStore
	borrower   []byte
	allocation *backend.OwnedAllocation
	bounds     *backend.BuildBounds
}

func (s *closeBorrowingStore) PromoteMember(ctx context.Context, key MemberKey, c *Candidate, next WorkCursor, failure MemberOutcome) (MainResponseHeader, error) {
	var err error
	s.bounds, err = backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return MainResponseHeader{}, err
	}
	s.allocation, err = s.bounds.OwnAllocation(1, 1)
	if err != nil {
		return MainResponseHeader{}, err
	}
	s.borrower = make([]byte, 1)
	return s.memoryPromotionBoundaryStore.PromoteMember(ctx, key, c, next, failure)
}

func TestMemberInvalidModelContentFinishesAndRetriesWithoutLosingSibling(t *testing.T) {
	for _, body := range []string{"not JSON", `{"translations":3}`, `{"unrelated":true}`, `{"translations":{"1":123,"2":"good"}}`} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart_%t", body, restart), func(t *testing.T) {
				store := &finiteRecoveryStore{MemoryRoundStore: NewMemoryRoundStore(nil), seed: restart, mode: "complete"}
				t.Cleanup(func() {
					if err := store.Close(); err != nil {
						t.Error(err)
					}
				})
				first := stagedTestRuntime(t, DefaultCandidateLimits())
				main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
					return &backend.Response{Text: body, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
				}}
				doc := newTestDoc(2)
				h := stagedTestHandler(t, first, main)
				h.BatchSize = 0
				h.MaxWordsPerBatch = 4000
				result, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: h}, doc, store, first, quietLogger(), nil)
				active := first
				if restart {
					if !storageFailure(err) {
						t.Fatalf("missing checkpoint restart fault: %v", err)
					}
					if err := first.CloseDocument(doc); err != nil {
						t.Fatal(err)
					}
					if err := first.Close(); err != nil {
						t.Fatal(err)
					}
					store.seed = false
					active = stagedTestRuntime(t, DefaultCandidateLimits())
					doc = newTestDoc(2)
					doc.InputTokens = 17
					doc.OutputTokens = 3
					h = stagedTestHandler(t, active, main)
					h.BatchSize = 0
					h.MaxWordsPerBatch = 4000
					result, err = RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: h}, doc, store, active, quietLogger(), nil)
				}
				if err != nil || main.calls.Load() != 1 || doc.InputTokens != 17 || doc.OutputTokens != 3 {
					t.Fatalf("bad capture remained stuck: result=%+v calls=%d err=%v", result, main.calls.Load(), err)
				}
				for _, r := range store.requests {
					if r.response.Cursor != 2 || r.response.Phase == "capture_pending" || r.capture != nil {
						t.Fatalf("invalid content never exited durable pending: %+v", r.response)
					}
				}
				if body == `{"translations":{"1":123,"2":"good"}}` && (len(result.Resolved) != 1 || doc.Segments[1].Target != "good") {
					t.Fatal("invalid member discarded its valid sibling")
				}
				if err := active.CloseDocument(doc); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	// A malformed response is a paid content failure and may use the next
	// configured business pool. It is not an infrastructure retry of capture.
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	store := NewMemoryRoundStore(nil)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	var calls int
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		calls++
		body := "not JSON"
		if calls == 2 {
			body = `{"translations":{"1":"retry-good"}}`
		}
		return &backend.Response{Text: body, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
	}}
	doc := newTestDoc(1)
	h := stagedTestHandler(t, r, main)
	h.BatchSize = 0
	h.MaxWordsPerBatch = 4000
	result, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: h, Retry: backend.RetryPolicy{MaxAttempts: 1}}, doc, store, r, quietLogger(), nil)
	if err != nil || calls != 2 || len(result.Resolved) != 1 || doc.Segments[0].Target != "retry-good" || doc.InputTokens != 34 || doc.OutputTokens != 6 {
		t.Fatalf("content business retry lost: calls=%d result=%+v err=%v", calls, result, err)
	}
	if err := r.CloseDocument(doc); err != nil {
		t.Fatal(err)
	}
}

func TestMemberInlineGlossaryWithoutRewritePublishesOnlyPublicFacts(t *testing.T) {
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	g := glossary.NewMemory()
	if _, err := g.Add(t.Context(), glossary.Entry{Source: "thread pool", Target: "existing"}); err != nil {
		t.Fatal(err)
	}
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"proposed","2":"proposed"},"glossary":[{"source":"thread pool","target":"proposed"},{"source":"new term","target":"new target"}]}`, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
	}}
	h := stagedTestHandler(t, r, main)
	h.BatchSize = 0
	h.MaxWordsPerBatch = 4000
	h.Glossary = g
	h.InlineBootstrap = true
	h.InlineConflictStrategy = "skip"
	store := NewMemoryRoundStore(nil)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	doc := newTestDoc(2)
	t.Cleanup(func() {
		if err := r.CloseDocument(doc); err != nil {
			t.Error(err)
		}
	})
	var corrections, outputReads atomic.Int64
	ctx := backend.WithMemberWorkRecorder(t.Context(), func(e backend.MemberWorkEvent) {
		if e.Operation == "correction_write" {
			corrections.Add(e.Rows)
		}
		if e.Operation == "output_read" {
			outputReads.Add(e.Rows)
		}
	})
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h}, doc, store, r, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 2 || main.calls.Load() != 1 || corrections.Load() != 0 || outputReads.Load() != 2 {
		t.Fatalf("nonrewrite public publication failed or read private targets: result=%+v read=%d corrections=%d err=%v", result, outputReads.Load(), corrections.Load(), err)
	}
	for _, seg := range doc.Segments {
		if seg.Target != "proposed" {
			t.Fatal("nonrewrite changed model target")
		}
	}
	entries, lookupErr := g.Lookup(t.Context(), "new term", "en", "zh")
	if lookupErr != nil || len(entries) != 1 || entries[0].Target != "new target" {
		t.Fatal("new public entry was not installed")
	}
	for _, request := range store.requests {
		var receipt glossary.ResponseReceipt
		if request.response.BusinessBound != 0 || json.Unmarshal(request.response.Receipt, &receipt) != nil || receipt.Added != 1 || receipt.Skipped != 1 {
			t.Fatalf("nonrewrite receipt/bound lost: %+v", request.response)
		}
	}
}

func TestMemberMemoryPublicIntegrityRejectsLengthAndSameLengthDamage(t *testing.T) {
	for _, kind := range []string{"length", "same_length"} {
		t.Run(kind, func(t *testing.T) {
			store := NewMemoryRoundStore(nil)
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			header := memberFixtureReserve(t, t.Context(), store, "public-integrity", 4096, memberFixtureInputs(2))
			header = memberFixtureOutputs(t, t.Context(), store, header, []archivedOutput{{Target: "first"}, {Target: "second"}}, []glossary.Entry{{Source: "aa", Target: "bb"}})
			r := store.requests[header.ID]
			if kind == "length" {
				r.response.PublicBytes++
			} else {
				for i := range r.publicBacking.payload {
					if r.publicBacking.payload[i] == 'b' {
						r.publicBacking.payload[i] = 'c'
						break
					}
				}
			}
			m, err := store.LoadResponsePublic(t.Context(), header.ID, 4096)
			if err == nil || len(m.Payload) != 0 || m.Release != nil {
				t.Fatal("damaged public payload became visible")
			}
			if _, err := store.PublishBusiness(t.Context(), header.ID, glossary.ResponseBusiness{}, func() { t.Fatal("corrupt public facts installed dictionary") }); err == nil {
				t.Fatal("corrupt public facts published business")
			}
			if r.response.Phase != "outputs_ready" || len(r.outputs) != 2 {
				t.Fatal("public damage discarded valid sibling members")
			}
		})
	}
}
