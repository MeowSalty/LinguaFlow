package qa

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIssueDispositionMarshal(t *testing.T) {
	cases := []struct {
		in   IssueDisposition
		want string
	}{
		{DispositionPending, `"pending"`},
		{DispositionDismissed, `"dismissed"`},
	}
	for _, c := range cases {
		out, err := json.Marshal(c.in)
		if err != nil {
			t.Fatalf("marshal %q: %v", c.in, err)
		}
		if string(out) != c.want {
			t.Errorf("marshal %q = %s, want %s", c.in, out, c.want)
		}
	}
}

func TestIssueDispositionUnmarshal(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		var d IssueDisposition
		if err := json.Unmarshal([]byte(`"pending"`), &d); err != nil {
			t.Fatal(err)
		}
		if d != DispositionPending {
			t.Errorf("got %q, want %q", d, DispositionPending)
		}
	})
	t.Run("dismissed", func(t *testing.T) {
		var d IssueDisposition
		if err := json.Unmarshal([]byte(`"dismissed"`), &d); err != nil {
			t.Fatal(err)
		}
		if d != DispositionDismissed {
			t.Errorf("got %q, want %q", d, DispositionDismissed)
		}
	})
	t.Run("null_legacy", func(t *testing.T) {
		var d IssueDisposition
		if err := json.Unmarshal([]byte(`null`), &d); err != nil {
			t.Fatal(err)
		}
		if d != DispositionPending {
			t.Errorf("got %q, want %q", d, DispositionPending)
		}
	})
	t.Run("empty_string_legacy", func(t *testing.T) {
		var d IssueDisposition
		if err := json.Unmarshal([]byte(`""`), &d); err != nil {
			t.Fatal(err)
		}
		if d != DispositionPending {
			t.Errorf("got %q, want %q", d, DispositionPending)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		var d IssueDisposition
		if err := json.Unmarshal([]byte(`"foo"`), &d); err == nil {
			t.Fatal("want error for invalid disposition, got nil")
		}
	})
}

func TestQualityIssueJSONRoundTrip(t *testing.T) {
	t.Run("pending_marshal", func(t *testing.T) {
		issue := QualityIssue{Code: "x", Message: "m", Disposition: DispositionPending}
		out, err := json.Marshal(issue)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"disposition":"pending"`) {
			t.Errorf("output %s missing disposition:pending", out)
		}
	})
	t.Run("dismissed_marshal", func(t *testing.T) {
		issue := QualityIssue{Code: "x", Message: "m", Disposition: DispositionDismissed}
		out, err := json.Marshal(issue)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"disposition":"dismissed"`) {
			t.Errorf("output %s missing disposition:dismissed", out)
		}
	})
	t.Run("missing_field_legacy", func(t *testing.T) {
		var issue QualityIssue
		if err := json.Unmarshal([]byte(`{"segment_index":0,"severity":"","code":"x","message":"m"}`), &issue); err != nil {
			t.Fatal(err)
		}
		if !issue.IsPending() {
			t.Errorf("IsPending = false, want true")
		}
		if issue.Disposition != DispositionPending {
			t.Errorf("Disposition = %q, want %q", issue.Disposition, DispositionPending)
		}
	})
	t.Run("empty_string_legacy", func(t *testing.T) {
		var issue QualityIssue
		if err := json.Unmarshal([]byte(`{"segment_index":0,"severity":"","code":"x","message":"m","disposition":""}`), &issue); err != nil {
			t.Fatal(err)
		}
		if !issue.IsPending() {
			t.Errorf("IsPending = false, want true")
		}
	})
}
