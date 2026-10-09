package execution

import "testing"

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
	if s.SchemaVersion != 2 || s.DefaultsVersion != 2 || s.RubyProtocolVersion != 2 || s.RubyValidatorVersion != 1 || s.RubyRetry.Concurrency != 1 {
		t.Fatalf("missing frozen defaults: %+v", s)
	}
	s.RubyRetry.Concurrency = 0
	if ValidateSpec(s) == nil {
		t.Fatal("new snapshot silently supplied A on restore")
	}
	s.SchemaVersion = 1
	s.DefaultsVersion = 1
	s.RubyProtocolVersion = 0
	s.RubyValidatorVersion = 0
	if err := ValidateSpec(s); err != nil {
		t.Fatalf("legacy snapshot rejected: %v", err)
	}
	model, err := ConcurrencyModelForSpec(s)
	if err != nil || model != LegacyRoundShared || s.RubyRetry.Concurrency != 0 || EffectiveRubyProtocolVersion(s) != 1 {
		t.Fatalf("legacy changed: %s %v", model, err)
	}
	if err := ValidateProfile(DefaultProfile()); err != nil {
		t.Fatalf("profile version changed with execution version: %v", err)
	}
}
