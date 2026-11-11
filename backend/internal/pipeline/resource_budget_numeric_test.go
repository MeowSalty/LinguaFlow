package pipeline

import (
	"bytes"
	"math"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestCandidateFloat32MetadataCanonicalBeforeTypedRestore(t *testing.T) {
	for _, number := range []float32{0x1p64, math.MaxFloat32, math.SmallestNonzeroFloat32, -math.SmallestNonzeroFloat32, 0.1, float32(math.Copysign(0, -1))} {
		candidate := &Candidate{DTOVersion: CandidateDTOVersion, ID: "candidate", WorkID: "work", Version: 9007199254740993, Mode: "translate", Segment: Segment{DBID: 9007199254740993, Meta: map[string]any{"value": number}}}
		kinds := metadataTypes(candidate.Segment.Meta)
		wire, err := candidate.EncodeBounded(1<<20, 0)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := workstate.CanonicalCandidateJSON(wire, 1<<20)
		// Generic SQL canonicalization sorts object keys; DTO struct order is
		// different. Its byte length, numeric values and canonical replay must
		// already agree before typed metadata restoration is attempted.
		if err != nil || len(canonical) != len(wire) {
			t.Fatalf("Q/hash changed before typed restore for %v: wire=%s canonical=%s err=%v", number, wire, canonical, err)
		}
		again, err := workstate.CanonicalCandidateJSON(canonical, 1<<20)
		if err != nil || !bytes.Equal(again, canonical) {
			t.Fatalf("generic canonical replay changed before typed restore: %v", err)
		}
		decoded, err := DecodeCandidateBounded(canonical, 1<<20, 0)
		if err != nil {
			t.Fatal(err)
		}
		b, err := backend.NewBuildBounds(1<<20, int64(len(wire)))
		if err != nil {
			t.Fatal(err)
		}
		if err := restoreMetadataTypes(&decoded.Segment, kinds, b); err != nil {
			t.Fatal(err)
		}
		if got, ok := decoded.Segment.Meta["value"].(float32); !ok || got != number {
			t.Fatalf("typed float32 value changed: %v/%v", got, number)
		}
		if decoded.Version != candidate.Version || decoded.Segment.DBID != candidate.Segment.DBID {
			t.Fatal("typed integer identity passed through float")
		}
		reencoded, err := decoded.EncodeBounded(1<<20, 0)
		if err != nil || !bytes.Equal(reencoded, wire) {
			t.Fatalf("typed restore changed canonical Q for %v: err=%v", number, err)
		}
	}
}
