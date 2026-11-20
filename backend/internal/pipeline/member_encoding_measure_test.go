package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
)

func measuredMemberBounds(t *testing.T) (*backend.BuildBounds, *int64) {
	t.Helper()
	var live int64
	ctx := backend.WithBuildReservation(context.Background(), func(n int64) error {
		live += n
		return nil
	}, func(n int64) { live -= n })
	b, err := backend.NewBuildBoundsFromContext(ctx, 1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.CloseRetired)
	return b, &live
}

func measuredCandidate(t *testing.T, b *backend.BuildBounds) *Candidate {
	t.Helper()
	c, err := newCandidateChecked(Segment{DBID: 7, Source: "source", Target: "translated \"value\"", Meta: map[string]any{"position": 3, "file": "book.xhtml"}}, 0, "translate", "epub", "candidate", b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMeasuredCandidateSealPreservesPayloadHeaderAndOwnership(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		name := "default"
		if reuse {
			name = "measured"
		}
		t.Run(name, func(t *testing.T) {
			b, live := measuredMemberBounds(t)
			c := measuredCandidate(t, b)
			// Both encoders emit this timestamp; their zero-time omitempty
			// conventions differ and would compare different representations.
			c.NextAttemptAt = time.Date(2026, time.November, 20, 1, 2, 3, 0, time.UTC)
			sum := sha256.Sum256([]byte(c.BaselineTarget))
			payload, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			measured, err := b.MeasureJSON(c)
			if err != nil || measured != int64(len(payload)) {
				t.Fatal("candidate measurement differs from canonical oracle", measured, err)
			}
			if reuse {
				err = c.sealAndCompactMeasured(b, measured, measured)
			} else {
				err = c.SealAndCompact(b, measured)
			}
			wantMeasurements := int64(3) // Caller, default payload and header.
			if reuse {
				wantMeasurements = 2 // Caller payload and independent header.
			}
			if err != nil || !bytes.Equal(c.PreparedPayload, payload) || c.StoredBytes != measured || b.Snapshot().JSONMeasurements != wantMeasurements {
				t.Fatalf("payload/real measurements: err=%v snapshot=%+v", err, b.Snapshot())
			}
			header, err := json.Marshal(HeaderForCandidate(c))
			if err != nil || !bytes.Equal(c.PreparedHeader, header) || c.BaselineDigest != hex.EncodeToString(sum[:]) || !c.HeaderOnly {
				t.Fatal("header, digest or compact identity changed", err)
			}
			if b.Snapshot().LiveBytes != *live || *live < int64(cap(c.PreparedPayload)+cap(c.PreparedHeader)) {
				t.Fatal("encoded payload/header lost their actual backing")
			}
			c = nil
			b.CloseRetired()
			if *live != 0 {
				t.Fatal("joined measured candidate retained ownership")
			}
		})
	}
}

func TestMeasuredCandidateSealRejectsDriftAndItemLimit(t *testing.T) {
	for _, name := range []string{"grow_after_measure", "shrink_after_measure", "item_limit"} {
		t.Run(name, func(t *testing.T) {
			b, live := measuredMemberBounds(t)
			c := measuredCandidate(t, b)
			measured, err := b.MeasureJSON(c)
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(1 << 20)
			switch name {
			case "grow_after_measure":
				if err := b.ReserveText(int64(len(c.Segment.Target)+len("changed")), int64(len(c.Segment.Target)+len("changed"))); err != nil {
					t.Fatal(err)
				}
				c.Segment.Target += "changed"
			case "shrink_after_measure":
				c.Segment.Target = ""
			case "item_limit":
				limit = measured - 1
			}
			err = c.sealAndCompactMeasured(b, measured, limit)
			if err == nil || c.PreparedPayload != nil || c.PreparedHeader != nil || b.Snapshot().JSONMeasurements != 1 {
				t.Fatal("drift/limit published or remeasured a candidate", err)
			}
			if name == "item_limit" && !errors.Is(err, backend.ErrBuildCapacity) {
				t.Fatal("item limit lost its capacity error", err)
			}
			c = nil
			b.CloseRetired()
			if *live != 0 {
				t.Fatal("rejected measured encoding retained ownership")
			}
		})
	}
}

func TestMeasuredArchivedOutputKeepsItemLimitAndReplacementFacts(t *testing.T) {
	for _, name := range []string{"normal", "oversize_replaced", "replacement_too_large"} {
		t.Run(name, func(t *testing.T) {
			b, live := measuredMemberBounds(t)
			key := MemberKey{RequestID: "request", Ordinal: 7, CaptureEpoch: 2}
			selected := archivedOutput{Target: "translated", Outcome: MemberOutcome{Key: key, Accepted: true}}
			limit := int64(600)
			if name != "normal" {
				selected.Target = strings.Repeat("x", 1000)
			}
			if name == "replacement_too_large" {
				limit = 8
			}
			original, err := json.Marshal(selected)
			if err != nil {
				t.Fatal(err)
			}
			want := selected
			measurements := int64(1)
			if int64(len(original)) > limit {
				outcome := MemberOutcome{Key: key, Reason: responsecontract.MemberTooLarge, Limit: limit, Observed: int64(len(original))}
				want = archivedOutput{Failure: outcome.String(), Outcome: outcome}
				measurements = 2
			}
			oracle, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := encodeArchivedOutput(b, selected, key, limit)
			if name == "replacement_too_large" {
				if !errors.Is(err, backend.ErrBuildCapacity) || payload != nil {
					t.Fatal("oversize replacement escaped the original item limit", err)
				}
			} else if err != nil || !bytes.Equal(payload, oracle) || int64(len(payload)) > limit {
				t.Fatal("archive payload or replacement identity differs from oracle", err)
			}
			if b.Snapshot().JSONMeasurements != measurements || b.Snapshot().LiveBytes != *live {
				t.Fatal("archive hid a measurement or lost allocation ownership", b.Snapshot())
			}
			payload = nil
			b.CloseRetired()
			if *live != 0 {
				t.Fatal("joined archived output retained ownership")
			}
		})
	}
}
