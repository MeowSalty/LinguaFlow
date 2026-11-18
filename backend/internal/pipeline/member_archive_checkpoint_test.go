package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type memberCheckpointFaultStore struct {
	*MemoryRoundStore
	mode    string
	faulted atomic.Bool
	writes  atomic.Int64
}

func (s *memberCheckpointFaultStore) PublishOutputs(ctx context.Context, in OutputPublication) error {
	s.writes.Add(1)
	boundary := in.Next == 16
	if s.mode == "last_checkpoint_ack" {
		boundary = in.Complete
	}
	if boundary && s.faulted.CompareAndSwap(false, true) {
		if s.mode == "before_checkpoint" {
			return terminalRuntimeStoreError{errors.New("archive checkpoint before write")}
		}
		if err := s.MemoryRoundStore.PublishOutputs(ctx, in); err != nil {
			return err
		}
		if s.mode == "retry_checkpoint_ack" {
			return errors.New("archive checkpoint ACK lost")
		}
		return terminalRuntimeStoreError{errors.New("archive checkpoint ACK lost before restart")}
	}
	return s.MemoryRoundStore.PublishOutputs(ctx, in)
}

func TestMemberArchiveCheckpointRecovery(t *testing.T) {
	for _, mode := range []string{"before_checkpoint", "after_checkpoint", "retry_checkpoint_ack", "last_checkpoint_ack"} {
		t.Run(mode, func(t *testing.T) {
			const n = 32
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			runtimes := []*ExecutionRuntime{r}
			defer r.Close()
			doc := newTestDoc(n)
			defer r.CloseDocument(doc)
			store := &memberCheckpointFaultStore{MemoryRoundStore: NewMemoryRoundStore(nil), mode: mode}
			defer store.Close()
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
			h := stagedTestHandler(t, r, main)
			h.BatchSize, h.MaxWordsPerBatch = 0, 4000
			result, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: h}, doc, store, r, quietLogger(), progress.Nop{})
			if mode != "retry_checkpoint_ack" {
				var storage *StorageError
				if !errors.As(err, &storage) {
					t.Fatalf("expected durable checkpoint fault, got %v", err)
				}
				headers, _, load := store.ResponseHeaders(t.Context(), 0, 16)
				if load != nil || len(headers) != 1 {
					t.Fatalf("capture missing: %v", load)
				}
				header := headers[0]
				want := 16
				if mode == "before_checkpoint" {
					want = 0
				}
				if mode == "last_checkpoint_ack" {
					want = n
				}
				if header.ArchiveCursor != want || header.Cursor != 0 || main.calls.Load() != 1 {
					t.Fatalf("checkpoint lost: %+v calls=%d", header, main.calls.Load())
				}
				if want < n {
					if header.Phase != "capture_pending" || header.ResponseBytes == 0 {
						t.Fatalf("capture retired before complete publication: %+v", header)
					}
					if _, e := store.LoadOutputMember(t.Context(), MemberKey{RequestID: header.ID, CaptureEpoch: header.CaptureEpoch}, false, 1<<20); e == nil {
						t.Fatal("incomplete collection became consumable")
					}
				} else if header.Phase != "outputs_ready" || header.ResponseBytes != 0 {
					t.Fatalf("complete publication kept raw: %+v", header)
				}
				paidInput, paidOutput := doc.InputTokens, doc.OutputTokens
				r.CloseDocument(doc)
				r.Close()
				// Reopen the canonical source after retiring its previous backing.
				// Paid usage is a retained fact, not another provider invocation.
				doc = newTestDoc(n)
				doc.InputTokens, doc.OutputTokens = paidInput, paidOutput
				r2 := stagedTestRuntime(t, DefaultCandidateLimits())
				runtimes = append(runtimes, r2)
				defer r2.Close()
				defer r2.CloseDocument(doc)
				h2 := stagedTestHandler(t, r2, main)
				h2.BatchSize, h2.MaxWordsPerBatch = 0, 4000
				result, err = RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: h2}, doc, store, r2, quietLogger(), progress.Nop{})
				if got := r2.Window.Snapshot(); got.Bytes != 0 || got.Segments != 0 {
					t.Fatalf("restored inventory remains: %+v", got)
				}
			}
			if err != nil || len(result.Resolved) != n || main.calls.Load() != 1 || doc.InputTokens != 17 || doc.OutputTokens != 3 {
				t.Fatalf("resume: resolved=%d calls=%d usage=%d/%d err=%v", len(result.Resolved), main.calls.Load(), doc.InputTokens, doc.OutputTokens, err)
			}
			for i, seg := range doc.Segments {
				if seg.Target != fmt.Sprintf("saved-%d", i) {
					t.Fatalf("member %d lost", i)
				}
			}
			for _, runtime := range runtimes {
				owned := runtime.Admission.OwnedPayloadSnapshot()
				for _, category := range []string{"freeze", "raw", "wire", "candidate"} {
					if owned.Current[category] != 0 {
						t.Fatalf("joined checkpoint retained %s backing: %+v", category, owned)
					}
				}
				if err := runtime.CloseDocument(doc); err != nil {
					t.Fatal(err)
				}
				runtime.Close()
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			for _, runtime := range runtimes {
				if owned := runtime.Admission.OwnedPayloadSnapshot(); owned.Total != 0 {
					t.Fatalf("last checkpoint store reference remains: %+v", owned)
				}
			}
		})
	}
}
