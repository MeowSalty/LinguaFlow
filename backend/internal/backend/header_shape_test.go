package backend_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func TestBoundedCandidateHeaderActualShape(t *testing.T) {
	want := pipeline.CandidateHeader{ID: "candidate-1", WorkID: "work-1", ParentRequestID: "request-1", Mode: "json", Format: "html", Version: 7, RetryEpoch: 2, Bytes: 1234, Index: 3, Pool: 1, MainAttempt: 2, LogicalAttempt: 3, NetworkAttempt: 4, Words: 12, Protocol: 2, Validator: 1, Next: time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC), Ready: true, ForceSingle: true, Missing: true}
	enc, err := backend.NewBuildBounds(pipeline.CandidateHeaderLimit, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := enc.EncodeJSON(want, pipeline.CandidateHeaderLimit)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := backend.NewBuildBounds(pipeline.CandidateHeaderLimit, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got pipeline.CandidateHeader
	if err = dec.DecodeJSON(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("header differs: %#v", got)
	}
	if _, err = pipeline.DecodeCandidateHeader(data); err != nil {
		t.Fatal(err)
	}
	t.Logf("bytes=%d nodes=%+v", len(data), dec.Snapshot())
}
