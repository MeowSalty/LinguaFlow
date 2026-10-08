package qa

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQualityIssueDecisionTimeJSON(t *testing.T) {
	local := time.Date(2026, 9, 29, 12, 30, 5, 123456789, time.FixedZone("user", 8*60*60))
	issue := QualityIssue{Code: "example", DecidedAt: &local, Note: "2026-09-29T12:30:05+08:00"}
	encoded, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"decided_at":"2026-09-29T04:30:05.123456789Z"`) ||
		!strings.Contains(string(encoded), `"disposition":"pending"`) {
		t.Fatalf("unexpected issue JSON: %s", encoded)
	}
	if issue.DecidedAt.Location() != local.Location() {
		t.Fatal("marshal mutated the caller's timestamp")
	}
	for _, disposition := range []string{"", `,"disposition":null`, `,"disposition":""`, `,"disposition":"dismissed"`} {
		var got QualityIssue
		if err := json.Unmarshal([]byte(`{"decided_at":"2026-09-29T12:30:05.123456789+08:00"`+disposition+`}`), &got); err != nil {
			t.Fatal(err)
		}
		if got.DecidedAt == nil || got.DecidedAt.Location() != time.UTC || !got.DecidedAt.Equal(local) {
			t.Fatalf("decoded instant = %v", got.DecidedAt)
		}
		want := DispositionPending
		if strings.Contains(disposition, "dismissed") {
			want = DispositionDismissed
		}
		if got.Disposition != want {
			t.Fatalf("disposition = %q, want %q", got.Disposition, want)
		}
	}
	var decoded QualityIssue
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Note != issue.Note {
		t.Fatalf("user note changed: %#v, %v", decoded, err)
	}
	if err := json.Unmarshal([]byte(`{"disposition":"unknown"}`), &decoded); err == nil {
		t.Fatal("invalid disposition was accepted")
	}
	if err := json.Unmarshal([]byte(`{"decided_at":null}`), &decoded); err != nil || decoded.DecidedAt != nil {
		t.Fatalf("nullable time changed: %#v, %v", decoded, err)
	}
}
