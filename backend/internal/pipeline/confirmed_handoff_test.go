package pipeline

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type confirmedHandoffBarrierStore struct {
	*MemoryRoundStore
	committed, release chan struct{}
}

func (s *confirmedHandoffBarrierStore) PrepareCandidateCommit(_ context.Context, b *backend.BuildBounds, _ *Candidate, result TranslatedSegment) (TranslatedSegment, error) {
	if err := b.CloneInto([]qa.QualityIssue{{Code: "fixture", Message: "confirmed warning", Disposition: qa.DispositionPending}}, &result.Issues); err != nil {
		return result, err
	}
	if err := b.CloneInto(map[string]string{"[P1]": "protected"}, &result.Protected); err != nil {
		return result, err
	}
	return result, nil
}

// The first resource has crossed the durable boundary but cannot deliver its
// terminal event until a second resource has failed and closed shared admission.
func TestConfirmedHandoffSurvivesOtherResourceStorageFailure(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	doc := newTestDoc(1)
	doc.Segments[0].ContentVersion = 7
	doc.Segments[0].TargetIsNull = true
	doc.Segments[0].Meta = map[string]any{"pos_lines": []int{2, 3}, "line": 9, "extension": map[string]any{"opaque": true}}
	defer r.CloseDocument(doc)
	store := &confirmedHandoffBarrierStore{MemoryRoundStore: NewMemoryRoundStore(nil), committed: make(chan struct{}), release: make(chan struct{})}
	defer store.Close()
	var release sync.Once
	defer release.Do(func() { close(store.release) })
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"confirmed first"}}`}, nil
	}}
	type completion struct {
		result RunRoundResult
		err    error
	}
	done := make(chan completion, 1)
	joined := false
	ctx = backend.WithMemberWorkRecorder(ctx, func(event backend.MemberWorkEvent) {
		if event.Phase == "confirmation" && event.Operation == "commit_ack" {
			close(store.committed)
			<-store.release
		}
	})
	defer func() {
		cancel()
		release.Do(func() { close(store.release) })
		if !joined {
			<-done
			joined = true
		}
	}()
	go func() {
		result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, doc, store, r, quietLogger(), nil)
		done <- completion{result, err}
	}()
	awaitSignal(t, store.committed)
	if doc.Segments[0].Target != "" || r.Admission.BuildBytes("candidate:"+firstConfirmedID(store)) == 0 {
		t.Fatal("projection was installed early or lost its live owner")
	}
	cause := permanentCommitFailure{errors.New("other resource storage failed")}
	failed := &controlCommitStore{MemoryRoundStore: NewMemoryRoundStore(nil), cause: cause}
	defer failed.Close()
	other := newTestDoc(1)
	defer r.CloseDocument(other)
	otherCtx, otherCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer otherCancel()
	_, err := RunStagedRound(otherCtx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, other, failed, r, quietLogger(), nil)
	if !errors.Is(err, cause) || !storageFailure(err) {
		t.Fatalf("other storage cause lost: %v", err)
	}
	if err := r.beginDispatch(ctx); !errors.Is(err, backend.ErrAdmissionClosed) {
		t.Fatalf("other failure did not close shared admission: %v", err)
	}
	release.Do(func() { close(store.release) })
	select {
	case out := <-done:
		joined = true
		if out.err != nil && !errors.Is(out.err, backend.ErrAdmissionClosed) {
			t.Fatalf("first result=%+v err=%v", out.result, out.err)
		}
	case <-ctx.Done():
		t.Fatal("committed owner did not join")
	}
	seg := doc.Segments[0]
	if seg.Target != "confirmed first" || seg.Status != "translated" || !seg.RoundWork.Completed || seg.TargetIsNull || seg.ContentVersion != 7 || !reflect.DeepEqual(seg.Meta["pos_lines"], []int{2, 3}) || seg.Meta["line"] != 9 || !reflect.DeepEqual(seg.Meta["extension"], map[string]any{"opaque": true}) || len(seg.Issues) != 1 || seg.Issues[0].Message != "confirmed warning" || seg.Protected["[P1]"] != "protected" {
		t.Fatalf("durable confirmation lost its projection: %+v", seg)
	}
	if other.Segments[0].Target != "" || main.calls.Load() != 2 {
		t.Fatalf("failed result applied or model repeated: failed=%q calls=%d", other.Segments[0].Target, main.calls.Load())
	}
	if r.Admission.BuildBytes("candidate:"+firstConfirmedID(store)) != 0 {
		t.Fatal("confirmed owner was not transferred")
	}
}

func firstConfirmedID(s *confirmedHandoffBarrierStore) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.confirmed {
		return id
	}
	return ""
}
