package main

import (
	"reflect"
	"testing"
)

func TestBudgetEnvironmentPreservesOmissionAndExplicitEmpty(t *testing.T) {
	key := budgetPrefix + "ACTOR_MAX_PAYLOAD_BYTES"
	other := budgetPrefix + "INSTANCE_MAX_PAYLOAD_BYTES"
	empty, positive := "", "1048576"
	for _, test := range []struct {
		name     string
		actual   map[string]*string
		expected map[string]string
		valid    bool
	}{
		{"omitted", nil, nil, true},
		{"unresolved-render", map[string]*string{key: nil}, nil, true},
		{"explicit-empty", map[string]*string{key: &empty}, map[string]string{key: ""}, true},
		{"empty-is-not-omitted", map[string]*string{key: &empty}, nil, false},
		{"partial", map[string]*string{key: &positive, other: nil}, map[string]string{key: positive}, true},
		{"missing-configured-value", nil, map[string]string{key: positive}, false},
		{"unexpected-default", map[string]*string{other: &positive}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := assertBudgetEnvironment(test.actual, test.expected); (err == nil) != test.valid {
				t.Fatalf("valid=%t: %v", test.valid, err)
			}
		})
	}
}

func TestMergeEnvironmentOverridesWithoutMutatingBase(t *testing.T) {
	base := []string{"PATH=tools", "COMPOSE_DISABLE_ENV_FILE=1", "ACTOR=123", "actor=456"}
	before := append([]string(nil), base...)
	got := mergeEnvironment(base, []string{"COMPOSE_DISABLE_ENV_FILE=0", "ACTOR="})
	want := []string{"PATH=tools", "COMPOSE_DISABLE_ENV_FILE=0", "ACTOR="}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(base, before) {
		t.Fatalf("override result=%q base=%q", got, base)
	}
}
