package execution

import (
	"errors"
	"testing"
)

func TestRubyConcurrencyPresenceAndSnapshotCompatibility(t *testing.T) {
	for _, tt := range []struct {
		input   *int
		want    int
		invalid bool
	}{{nil, 1, false}, {new(0), 0, true}, {new(-1), 0, true}, {new(3), 3, false}} {
		got, err := ResolveRubyRetryConcurrency(tt.input)
		if (err != nil) != tt.invalid || got != tt.want {
			t.Fatalf("got=%d err=%v case=%+v", got, err, tt)
		}
	}
	in := validExecutionInput()
	in.RubyRetry = &ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: in.Rounds[0].Backend, MaxAttempts: 1}
	s, err := Resolve(in)
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != 4 || s.DefaultsVersion != 4 || s.ResponseFormatVersion != 1 || s.RubyProtocolVersion != 2 || s.RubyValidatorVersion != 1 || s.RubyBatchProtocolVersion != 1 || s.RubyRetry.Concurrency != 1 {
		t.Fatalf("missing frozen defaults: %+v", s)
	}
	s.RubyRetry.Concurrency = 0
	if ValidateSpec(s) == nil {
		t.Fatal("new snapshot silently supplied A on restore")
	}
	for _, version := range []int{1, 2, 3} {
		s.SchemaVersion, s.DefaultsVersion = version, version
		if err := ValidateSpec(s); !errors.Is(err, ErrUnsupportedExecutionFormat) {
			t.Fatalf("old snapshot executed: version=%d error=%v", version, err)
		}
		if _, err := ConcurrencyModelForSpec(s); !errors.Is(err, ErrUnsupportedExecutionFormat) {
			t.Fatalf("old concurrency interpreted: %v", err)
		}
	}
	if err := ValidateProfile(DefaultProfile()); err != nil {
		t.Fatalf("profile version changed with execution version: %v", err)
	}
}
