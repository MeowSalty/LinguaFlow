package config

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func budgetFields() []serverField {
	var fields []serverField
	for _, field := range serverFields {
		if strings.HasPrefix(field.Key, "server.execution_budget.") {
			fields = append(fields, field)
		}
	}
	return fields
}

func budgetDocument(field serverField, value string) string {
	parts := strings.Split(field.Key, ".")
	return fmt.Sprintf("kind: server\nversion: 1\nserver:\n  execution_budget:\n    %s:\n      %s: %s\n", parts[2], parts[3], value)
}

func TestOptionalBudgetFieldsSourcesAndValues(t *testing.T) {
	for _, mode := range []string{ModeServer, ModeLocal} {
		for _, field := range budgetFields() {
			for _, source := range []string{"yaml", "env"} {
				for _, value := range []string{"missing", "1", "9223372036854775807", "0", "-1", "9223372036854775808", "", " ", "null", "1.5", "true", "'1'"} {
					t.Run(mode+"/"+field.Key+"/"+source+"/"+value, func(t *testing.T) {
						in := deploymentInputs(t)
						in.Mode = mode
						if value != "missing" {
							if source == "yaml" {
								withDocument(t, &in, budgetDocument(field, value))
							} else {
								in.Environment[field.Environment] = value
							}
						}
						resolved, err := ResolveServerConfig(in)
						valid := value == "missing" || value == "1" || value == "9223372036854775807"
						if !valid {
							if err == nil || !strings.Contains(err.Error(), field.Key) {
								t.Fatalf("invalid input accepted or wrong field: %v", err)
							}
							return
						}
						if err != nil {
							t.Fatal(err)
						}
						explanation := explainField(t, resolved, field.Key)
						if value == "missing" {
							if field.read(resolved) != nil || explanation.Value != "not configured (unlimited)" || explanation.Source != "not configured" {
								t.Fatalf("absence lost: %+v", explanation)
							}
						} else {
							if explanation.Value != value || !strings.HasPrefix(explanation.Source, map[string]string{"yaml": "file:", "env": "env:"}[source]) {
								t.Fatalf("value/source lost: %+v", explanation)
							}
						}
						for _, other := range budgetFields() {
							if other.Key != field.Key && other.read(resolved) != nil {
								t.Fatalf("single field populated %s", other.Key)
							}
						}
					})
				}
			}
		}
	}
}

func TestOptionalBudgetGroupsAndEarlyTypeErrors(t *testing.T) {
	for _, body := range []string{"execution_budget: {}", "execution_budget:\n    actor: {}\n    instance: {}"} {
		in := deploymentInputs(t)
		withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  "+body+"\n")
		if _, err := ResolveServerConfig(in); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{"execution_budget: null", "execution_budget:", "execution_budget:\n    actor: null", "execution_budget:\n    instance:", "execution_budget: &budget {}\n  authentication: *budget"} {
		in := deploymentInputs(t)
		withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  "+body+"\n")
		if _, err := ResolveServerConfig(in); err == nil {
			t.Fatalf("invalid object accepted: %s", body)
		}
	}
	field := budgetFields()[0]
	for _, value := range []string{"broken", "null", "", "9223372036854775808", "&cap 1\n      max_payload_bytes: *cap"} {
		in := deploymentInputs(t)
		withDocument(t, &in, budgetDocument(field, value))
		in.Environment[field.Environment] = "1"
		if _, err := ResolveServerConfig(in); err == nil {
			t.Fatalf("higher source hid malformed YAML %q", value)
		}
	}
}

func TestOptionalBudgetFinalValidationAndPartialOverride(t *testing.T) {
	field := budgetFields()[0]
	for _, values := range []struct {
		yaml, env string
		valid     bool
	}{{"0", "1", true}, {"-1", "1", true}, {"1", "0", false}, {"1", "-1", false}} {
		in := deploymentInputs(t)
		withDocument(t, &in, budgetDocument(field, values.yaml))
		in.Environment[field.Environment] = values.env
		resolved, err := ResolveServerConfig(in)
		if values.valid {
			if err != nil {
				t.Fatal(err)
			}
			if got := explainField(t, resolved, field.Key); got.Value != "1" || !strings.HasPrefix(got.Source, "env:") {
				t.Fatalf("wrong final source: %+v", got)
			}
		} else if err == nil {
			t.Fatalf("invalid final value %s accepted", values.env)
		}
	}
	in := deploymentInputs(t)
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  execution_budget:\n    actor:\n      max_payload_bytes: 123\n      max_build_workers: 2\n    instance:\n      max_queued_descriptors: 4\n")
	in.Environment["LINGUAFLOW_EXECUTION_BUDGET_ACTOR_MAX_PAYLOAD_BYTES"] = "321"
	resolved, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := resolved.Config.ExecutionBudget.RuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.Actor.MaxPayloadBytes != 321 || policy.Actor.MaxBuildWorkers != 2 || policy.Instance.MaxQueuedDescriptors != 4 || policy.Instance.MaxPayloadBytes != 0 {
		t.Fatalf("partial overrides replaced other dimensions: %+v", policy)
	}
}

func TestOptionalBudgetPolicyCopiesAndFieldPointers(t *testing.T) {
	omitted, err := (ExecutionBudgetConfig{}).RuntimePolicy()
	if err != nil || omitted != (resourcebudget.Policy{}) {
		t.Fatalf("zero configuration: %+v %v", omitted, err)
	}
	first, second := &ResolvedServer{}, &ResolvedServer{}
	fields := budgetFields()
	if len(fields) != 16 {
		t.Fatalf("expected sixteen independently registered limits, got %d", len(fields))
	}
	for _, field := range fields {
		if field.Type != "optional_integer64" || field.read(first) != nil {
			t.Fatalf("typed nil exposed by %s", field.Key)
		}
		field.write(first, int64(math.MaxInt64))
		field.write(second, int64(math.MaxInt64))
	}
	before, err := first.Config.ExecutionBudget.RuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if before.Actor.MaxPayloadBytes != math.MaxInt64 || before.Instance.MaxBuildWorkers != math.MaxInt64 {
		t.Fatalf("large values changed: %+v", before)
	}
	*first.Config.ExecutionBudget.Actor.MaxPayloadBytes = 3
	if *first.Config.ExecutionBudget.Instance.MaxPayloadBytes != math.MaxInt64 || *second.Config.ExecutionBudget.Actor.MaxPayloadBytes != math.MaxInt64 || before.Actor.MaxPayloadBytes != math.MaxInt64 {
		t.Fatal("parsed fields or converted policy share mutable values")
	}
	for _, field := range fields {
		for _, value := range []int64{0, -1} {
			resolved := &ResolvedServer{}
			field.write(resolved, value)
			if policy, err := resolved.Config.ExecutionBudget.RuntimePolicy(); err == nil || policy != (resourcebudget.Policy{}) || !strings.Contains(err.Error(), field.Key) {
				t.Fatalf("invalid direct construction %s: %+v %v", field.Key, policy, err)
			}
		}
	}
}

func TestOptionalBudgetDocumentRoundTrip(t *testing.T) {
	for _, scenario := range []string{"omitted", "partial", "finite"} {
		t.Run(scenario, func(t *testing.T) {
			in := deploymentInputs(t)
			for i, field := range budgetFields() {
				if scenario == "finite" || scenario == "partial" && (i == 1 || i == 10) {
					in.Environment[field.Environment] = fmt.Sprint(i + 1)
				}
			}
			resolved, err := ResolveServerConfig(in)
			if err != nil {
				t.Fatal(err)
			}
			data, err := EncodeServerDocument(resolved.Config, resolved.Log)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "omitted" && strings.Contains(string(data), "execution_budget") {
				t.Fatal("omitted limits emitted")
			}
			withDocument(t, &in, string(data))
			for _, field := range budgetFields() {
				delete(in.Environment, field.Environment)
			}
			restored, err := ResolveServerConfig(in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored.Config.ExecutionBudget, resolved.Config.ExecutionBudget) {
				t.Fatal("optional values lost during export/reload")
			}
			if strings.Contains(fmt.Sprint(restored.Fields), resolved.Config.JWTSecret) {
				t.Fatal("secret leaked by explain")
			}
			for _, field := range budgetFields() {
				explanation := explainField(t, restored, field.Key)
				if field.read(restored) == nil && explanation.Source != "not configured" {
					t.Fatalf("omitted field gained source: %+v", explanation)
				}
				if field.read(restored) != nil && !strings.HasPrefix(explanation.Source, "file:") {
					t.Fatalf("loaded field source lost: %+v", explanation)
				}
			}
		})
	}
	for _, value := range []int64{0, -1} {
		cfg := *DefaultServerConfig()
		cfg.ExecutionBudget.Actor.MaxPayloadBytes = &value
		if _, err := EncodeServerDocument(cfg, LogConfig{}); err == nil {
			t.Fatal("encoder published invalid explicit limit")
		}
	}
}

func TestMaintenanceOptionalBudgetKeepsValidationScope(t *testing.T) {
	for _, mode := range []string{ModeLocal, ModeServer} {
		in := deploymentInputs(t)
		in.Mode = mode
		resolved, err := ResolveMaintenanceServerConfig(in)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateServerConfig(&resolved.Config); err != nil {
			t.Fatal(err)
		}
		field := budgetFields()[0]
		for _, value := range []string{"0", "-1"} {
			in.Environment[field.Environment] = value
			if _, err := ResolveMaintenanceServerConfig(in); err == nil {
				t.Fatal("maintenance accepted explicit invalid budget")
			}
		}
		delete(in.Environment, field.Environment)
		in.Environment["LINGUAFLOW_HOST"] = ""
		if _, err := ResolveMaintenanceServerConfig(in); err == nil {
			t.Fatal("maintenance stopped validating host")
		}
	}
	in := deploymentInputs(t)
	delete(in.Environment, "LINGUAFLOW_JWT_SECRET")
	if _, err := ResolveMaintenanceServerConfig(in); err == nil {
		t.Fatal("serve maintenance stopped requiring its secret")
	}
}
