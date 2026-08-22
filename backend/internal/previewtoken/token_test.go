package previewtoken

import (
	"testing"
	"time"
)

func TestApplyClaimsKindRoundTrip(t *testing.T) {
	codec := NewCodec("test-secret", time.Minute)
	want := ApplyClaims{
		ActorUserID: 1,
		ProjectID:   2,
		ResourceID:  3,
		SegmentID:   4,
		Kind:        KindRevision,
	}
	token, _, err := codec.Encode(want)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got, err := codec.Decode(token)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.Kind != KindRevision {
		t.Fatalf("Kind = %q, want %q", got.Kind, KindRevision)
	}
}

func TestApplyClaimsKindTranslateRemainsEmpty(t *testing.T) {
	codec := NewCodec("test-secret", time.Minute)
	token, _, err := codec.Encode(ApplyClaims{SegmentID: 1, Kind: KindTranslate})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got, err := codec.Decode(token)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.Kind != KindTranslate {
		t.Fatalf("Kind = %q, want empty translation kind", got.Kind)
	}
}
