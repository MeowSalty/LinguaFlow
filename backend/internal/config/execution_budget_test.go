package config

import (
	"fmt"
	"strings"
	"testing"
)

// Explicit fixture inputs are not deployment defaults.
func withExecutionBudget(in ServerInputs) ServerInputs {
	if in.Environment == nil {
		in.Environment = make(map[string]string)
	}
	for _, f := range serverFields {
		if strings.HasPrefix(f.Key, "server.execution_budget.") {
			in.Environment[f.Environment] = "1048576"
		}
	}
	return in
}

func TestExecutionBudgetRequiredInBothModes(t *testing.T) {
	for _, mode := range []string{ModeServer, ModeLocal} {
		for _, f := range serverFields {
			if !strings.HasPrefix(f.Key, "server.execution_budget.") {
				continue
			}
			for _, bad := range []string{"missing", "0", "-1", "9223372036854775808"} {
				t.Run(fmt.Sprintf("%s/%s/%s", mode, f.Key, bad), func(t *testing.T) {
					in := deploymentInputs(t)
					in.Mode = mode
					if bad == "missing" {
						delete(in.Environment, f.Environment)
					} else {
						in.Environment[f.Environment] = bad
					}
					if _, err := ResolveServerConfig(in); err == nil {
						t.Fatal("invalid execution policy accepted")
					}
				})
			}
		}
	}
	if err := ValidateExecutionBudget(DefaultServerConfig().ExecutionBudget); err == nil {
		t.Fatal("production defaults unexpectedly authorize execution")
	}
}

func TestExecutionBudgetFieldsAndRoundTrip(t *testing.T) {
	in := deploymentInputs(t)
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Fields {
		if !strings.HasPrefix(f.Key, "server.execution_budget.") {
			continue
		}
		if f.Modes != "serve/local" || f.Effect != "restart" || !strings.HasPrefix(f.Source, "env:") {
			t.Fatalf("bad field contract: %+v", f)
		}
	}
	body, err := EncodeServerDocument(r.Config, r.Log)
	if err != nil {
		t.Fatal(err)
	}
	withDocument(t, &in, string(body))
	for _, f := range serverFields {
		if strings.HasPrefix(f.Key, "server.execution_budget.") {
			delete(in.Environment, f.Environment)
		}
	}
	got, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.ExecutionBudget != r.Config.ExecutionBudget {
		t.Fatal("execution policy lost on document round trip")
	}
}

func TestMaintenanceDoesNotAuthorizeExecution(t *testing.T) {
	in := deploymentInputs(t)
	for _, f := range serverFields {
		if strings.HasPrefix(f.Key, "server.execution_budget.") {
			delete(in.Environment, f.Environment)
		}
	}
	r, err := ResolveMaintenanceServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateServerConfig(&r.Config); err == nil {
		t.Fatal("maintenance inputs authorized Server")
	}
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("serve loader omitted execution authorization")
	}
	body, err := EncodeServerDocument(r.Config, r.Log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "execution_budget") {
		t.Fatal("absent authority serialized as invalid zero policy")
	}
}
