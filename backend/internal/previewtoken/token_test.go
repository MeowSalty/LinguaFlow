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

func TestApplyClaimsResolvedCodesRoundTrip(t *testing.T) {
	codec := NewCodec("test-secret", time.Minute)
	want := ApplyClaims{
		SegmentID:     4,
		Kind:          KindRevision,
		ResolvedCodes: []string{"calque", "omission"},
	}
	token, _, err := codec.Encode(want)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got, err := codec.Decode(token)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(got.ResolvedCodes) != 2 || got.ResolvedCodes[0] != "calque" || got.ResolvedCodes[1] != "omission" {
		t.Fatalf("ResolvedCodes = %+v, want [calque omission]", got.ResolvedCodes)
	}

	// 翻译令牌不携带 ResolvedCodes：omitempty 编码省略字段，解码后为空；
	// 旧令牌（无 rc 字段）同样解码为空，退化为旧行为。
	token2, _, err := codec.Encode(ApplyClaims{SegmentID: 5, Kind: KindTranslate})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got2, err := codec.Decode(token2)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(got2.ResolvedCodes) != 0 {
		t.Fatalf("ResolvedCodes = %+v, want empty for translate token", got2.ResolvedCodes)
	}
}
