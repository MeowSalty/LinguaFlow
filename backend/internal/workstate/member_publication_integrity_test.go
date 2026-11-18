package workstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequestinputmember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workresponsemember"
)

// Two members make publication atomicity observable: an invalid last member
// cannot leave the first published, and a damaged first member cannot erase its
// valid sibling. Each caller owns an isolated SQLite database.
func integrityMemberPair(t *testing.T, ctx context.Context, client *ent.Client, store *Store, scope Scope, first *ent.Segment) (Request, CapturedResponse, []Candidate) {
	t.Helper()
	currentExecution(t, ctx, client, scope)
	second := client.Segment.Create().SetResourceID(scope.ResourceID).SetSegmentIndex(1).SetSourceText("second source").SaveX(ctx)
	client.WorkItem.Create().SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetSegmentID(second.ID).SetRetryEpoch(scope.RetryEpoch).ExecX(ctx)
	client.JobRound.UpdateOneID(scope.RoundID).SetSegmentTotal(2).ExecX(ctx)
	r, capture, a := rawRequest(scope, first)
	other, _, b := rawRequest(scope, second)
	b.ID = "stable-second-member"
	member := other.InputMembers[0]
	member.Key.Ordinal, member.PromptID, member.CandidateID = 1, "2", b.ID
	r.SegmentIDs = append(r.SegmentIDs, second.ID)
	r.InputMembers = append(r.InputMembers, member)
	r.InputDigest = payloadDigest(bytes.Join([][]byte{r.InputMembers[0].Payload, member.Payload}, nil))
	return r, capture, []Candidate{a, b}
}

func integrityOutput(r Request, ordinal int, text string) OutputMember {
	p := []byte(fmt.Sprintf(`{"target":%q}`, text))
	return OutputMember{Key: requestMemberKey(r, ordinal), Payload: p, Bytes: int64(len(p)), Digest: payloadDigest(p)}
}

func integrityPublication(r Request, start, end int, complete bool) OutputPublication {
	in := OutputPublication{Key: requestMemberKey(r, start), Next: end, Complete: complete}
	for ordinal := 0; ordinal < end; ordinal++ {
		member := integrityOutput(r, ordinal, fmt.Sprintf("member-%d", ordinal))
		in.Digest = payloadDigest([]byte(in.Digest + member.Digest))
	}
	for ordinal := start; ordinal < end; ordinal++ {
		in.Members = append(in.Members, integrityOutput(r, ordinal, fmt.Sprintf("member-%d", ordinal)))
	}
	return in
}

func integrityPublish(t *testing.T, ctx context.Context, store *Store, r Request, capture CapturedResponse) {
	t.Helper()
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureResponse(ctx, capture); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishOutputs(ctx, integrityPublication(r, 0, 2, true)); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishMemberBusiness(ctx, r.ID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMemberPublicationInvalidLastInputRollsBack(t *testing.T) {
	for _, fault := range []string{"epoch", "ordinal", "duplicate_prompt", "digest"} {
		t.Run(fault, func(t *testing.T) {
			ctx, client, store, scope, first := fixture(t)
			r, _, _ := integrityMemberPair(t, ctx, client, store, scope, first)
			switch fault {
			case "epoch":
				r.InputMembers[1].Key.CaptureEpoch++
			case "ordinal":
				r.InputMembers[1].Key.Ordinal = 0
			case "duplicate_prompt":
				r.InputMembers[1].PromptID = r.InputMembers[0].PromptID
			case "digest":
				r.InputMembers[1].Digest = payloadDigest([]byte("wrong"))
			}
			if err := store.ReserveRequest(ctx, r); !errors.Is(err, ErrManifest) {
				t.Fatalf("invalid %s accepted: %v", fault, err)
			}
			if client.WorkRequest.Query().CountX(ctx) != 0 || client.WorkRequestInputMember.Query().CountX(ctx) != 0 {
				t.Fatal("failed freeze left a published prefix")
			}
			for _, work := range client.WorkItem.Query().AllX(ctx) {
				if work.MainAttempts != 0 || work.MainNetworkAttempts != 0 {
					t.Fatal("failed freeze debited a member")
				}
			}
		})
	}
}

func TestMemberPublicationHeadersRejectBeforePayloadRead(t *testing.T) {
	for _, phase := range []string{"old_format", "unpublished_input"} {
		t.Run(phase, func(t *testing.T) {
			ctx, client, store, scope, first := fixture(t)
			r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
			if err := store.ReserveRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			if err := store.CaptureResponse(ctx, capture); err != nil {
				t.Fatal(err)
			}
			row := client.WorkRequest.Query().OnlyX(ctx)
			if phase == "old_format" {
				client.WorkRequest.UpdateOneID(row.ID).SetResponseFormatVersion(1).ExecX(ctx)
			} else {
				client.WorkRequest.UpdateOneID(row.ID).SetPromotionState("inputs_pending").ExecX(ctx)
			}
			var payloadWork int64
			recorded := backend.WithMemberWorkRecorder(ctx, func(e backend.MemberWorkEvent) {
				if e.Operation == "input_read" || e.Operation == "output_read" || e.Operation == "capture_read" || e.Operation == "hash" {
					payloadWork += e.Bytes
				}
			})
			frame, err := store.LoadInputMember(recorded, requestMemberKey(r, 0), 1<<20)
			if frame.Release != nil {
				frame.Release()
			}
			if !errors.Is(err, ErrManifest) {
				t.Fatalf("unpublished/old input accepted: %v", err)
			}
			if phase == "old_format" {
				if _, _, err = store.LoadResponseHeaders(recorded, scope.JobID, 0, 16); !errors.Is(err, ErrManifest) {
					t.Fatalf("old header entered recovery: %v", err)
				}
				captureFrame, captureErr := store.LoadCapture(recorded, r.ID, 1<<20)
				if captureFrame.Release != nil {
					captureFrame.Release()
				}
				if !errors.Is(captureErr, ErrManifest) {
					t.Fatalf("old capture accepted: %v", captureErr)
				}
			}
			if payloadWork != 0 {
				t.Fatalf("header rejection reached payload work: %d", payloadWork)
			}
			after := client.WorkRequest.GetX(ctx, row.ID)
			if !bytes.Equal(after.CapturePayload, capture.Payload) || client.WorkRequestInputMember.Query().CountX(ctx) != 2 || client.UsageRecord.Query().CountX(ctx) != 1 {
				t.Fatal("rejection destroyed durable facts")
			}
		})
	}
}

func TestMemberPublicationCheckpointUnknownACKAndFinalVisibility(t *testing.T) {
	ctx, client, store, scope, first, faults := memberDeltaFixture(t)
	r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureResponse(ctx, capture); err != nil {
		t.Fatal(err)
	}
	firstPage := integrityPublication(r, 0, 1, false)
	faults.loseACK.Store(true)
	if err := store.PublishOutputs(ctx, firstPage); err != nil {
		t.Fatal(err)
	}
	store = NewStore(client)
	if err := store.PublishOutputs(ctx, firstPage); err != nil {
		t.Fatal(err)
	}
	row := client.WorkRequest.Query().OnlyX(ctx)
	if row.ArchiveCursor != 1 || row.PromotionCursor != 0 || !bytes.Equal(row.CapturePayload, capture.Payload) || client.WorkResponseMember.Query().CountX(ctx) != 1 {
		t.Fatal("checkpoint ACK replay lost/duplicated a prefix")
	}
	if _, err := store.LoadOutputMember(ctx, requestMemberKey(r, 0), false, 1<<20); !errors.Is(err, ErrManifest) {
		t.Fatalf("partial collection became readable: %v", err)
	}
	bad := integrityPublication(r, 1, 2, true)
	bad.Members[0].Key.CaptureEpoch++
	if err := store.PublishOutputs(ctx, bad); !errors.Is(err, ErrManifest) {
		t.Fatalf("wrong epoch accepted: %v", err)
	}
	if client.WorkResponseMember.Query().CountX(ctx) != 1 || client.WorkRequest.GetX(ctx, row.ID).ArchiveCursor != 1 {
		t.Fatal("failed final page was partially published")
	}
	faults.loseACK.Store(true)
	if err := store.PublishOutputs(ctx, integrityPublication(r, 1, 2, true)); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishOutputs(ctx, integrityPublication(r, 1, 2, true)); err != nil {
		t.Fatal(err)
	}
	row = client.WorkRequest.GetX(ctx, row.ID)
	if row.ArchiveCursor != 2 || row.CaptureBytes != 0 || len(row.CapturePayload) != 0 || client.WorkResponseMember.Query().CountX(ctx) != 2 || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("final publication did not retire raw exactly once")
	}
	corrected := integrityOutput(r, 0, "corrected")
	if err := store.StageCorrectedMember(ctx, corrected); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOutputMember(ctx, corrected.Key, true, 1<<20); !errors.Is(err, ErrManifest) {
		t.Fatalf("unpublished corrected result readable: %v", err)
	}
	if err := store.PublishMemberBusiness(ctx, r.ID, nil); err != nil {
		t.Fatal(err)
	}
	frame, err := store.LoadOutputMember(ctx, corrected.Key, true, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame.Payload, corrected.Payload) {
		t.Fatal("published correction lost")
	}
	frame.Release()
	frame.Release()
	for _, key := range []MemberKey{{RequestID: r.ID, Ordinal: -1}, {RequestID: r.ID, Ordinal: 2}, {RequestID: r.ID, Ordinal: 0, CaptureEpoch: 1}} {
		if _, err := store.LoadOutputMember(ctx, key, true, 1<<20); !errors.Is(err, ErrManifest) {
			t.Fatalf("invalid member key accepted: %+v %v", key, err)
		}
	}
}

func TestMemberPublicationDamageKeepsValidSibling(t *testing.T) {
	for _, fault := range []string{"input_length", "input_digest", "output_length", "output_digest", "corrected_length", "corrected_digest", "source", "baseline", "candidate_identity"} {
		t.Run(fault, func(t *testing.T) {
			ctx, client, store, scope, first := fixture(t)
			r, capture, candidates := integrityMemberPair(t, ctx, client, store, scope, first)
			if err := store.ReserveRequest(ctx, r); err != nil {
				t.Fatal(err)
			}
			if err := store.CaptureResponse(ctx, capture); err != nil {
				t.Fatal(err)
			}
			if err := store.PublishOutputs(ctx, integrityPublication(r, 0, 2, true)); err != nil {
				t.Fatal(err)
			}
			if err := store.StageCorrectedMember(ctx, integrityOutput(r, 0, "corrected")); err != nil {
				t.Fatal(err)
			}
			if err := store.PublishMemberBusiness(ctx, r.ID, nil); err != nil {
				t.Fatal(err)
			}
			inputRow := client.WorkRequestInputMember.Query().Where(workrequestinputmember.OrdinalEQ(0)).OnlyX(ctx)
			outputRow := client.WorkResponseMember.Query().Where(workresponsemember.OrdinalEQ(0)).OnlyX(ctx)
			var err error
			switch fault {
			case "input_length", "input_digest":
				update := client.WorkRequestInputMember.UpdateOneID(inputRow.ID)
				if fault == "input_length" {
					update.SetPayloadBytes(inputRow.PayloadBytes + 1)
				} else {
					update.SetPayloadDigest(payloadDigest([]byte("bad")))
				}
				update.ExecX(ctx)
				frame, e := store.LoadInputMember(ctx, requestMemberKey(r, 0), 1<<20)
				err = e
				if frame.Release != nil {
					frame.Release()
				}
			case "output_length", "output_digest", "corrected_length", "corrected_digest":
				update := client.WorkResponseMember.UpdateOneID(outputRow.ID)
				switch fault {
				case "output_length":
					update.SetPayloadBytes(outputRow.PayloadBytes + 1)
				case "output_digest":
					update.SetPayloadDigest(payloadDigest([]byte("bad")))
				case "corrected_length":
					update.SetCorrectedBytes(outputRow.CorrectedBytes + 1)
				case "corrected_digest":
					update.SetCorrectedDigest(payloadDigest([]byte("bad")))
				}
				update.ExecX(ctx)
				frame, e := store.LoadOutputMember(ctx, requestMemberKey(r, 0), fault == "corrected_length" || fault == "corrected_digest", 1<<20)
				err = e
				if frame.Release != nil {
					frame.Release()
				}
			default:
				candidate := candidates[0]
				if fault == "source" {
					client.WorkRequestInputMember.UpdateOneID(inputRow.ID).SetSourceGeneration(scope.SourceGeneration + 1).ExecX(ctx)
				}
				if fault == "baseline" {
					candidate.BaselineVersion++
				}
				if fault == "candidate_identity" {
					candidate.WorkID = "other-member"
				}
				err = store.PromoteMember(ctx, MemberPromotion{Scope: scope, Key: requestMemberKey(r, 0), Candidate: &candidate})
			}
			if err == nil {
				t.Fatalf("damaged %s accepted", fault)
			}
			frame, err := store.LoadOutputMember(ctx, requestMemberKey(r, 1), true, 1<<20)
			if err != nil {
				t.Fatalf("damage lost valid sibling: %v", err)
			}
			if !bytes.Equal(frame.Payload, integrityOutput(r, 1, "member-1").Payload) {
				t.Fatal("valid sibling changed")
			}
			frame.Release()
			row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx)
			if row.PromotionCursor != 0 || row.RemainingMembers != 2 || client.WorkCandidate.Query().CountX(ctx) != 0 || client.UsageRecord.Query().CountX(ctx) != 1 {
				t.Fatal("damage rejection advanced or recounted durable work")
			}
		})
	}
}
