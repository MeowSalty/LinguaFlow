package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const budgetPrefix = "LINGUAFLOW_EXECUTION_BUDGET_"

type budgetCase struct {
	name       string
	shell      map[string]string
	file       map[string]string
	explicit   bool
	expected   map[string]string
	invalidKey string
}

func budgetCases() []budgetCase {
	actor := budgetPrefix + "ACTOR_MAX_PAYLOAD_BYTES"
	instance := budgetPrefix + "INSTANCE_MAX_QUEUED_DESCRIPTORS"
	cases := []budgetCase{
		{name: "omitted"},
		{name: "actor-shell", shell: map[string]string{actor: "1048576"}, expected: map[string]string{actor: "1048576"}},
		{name: "instance-shell", shell: map[string]string{instance: "256"}, expected: map[string]string{instance: "256"}},
		{name: "mixed-shell", shell: map[string]string{actor: "1048576", instance: "256"}, expected: map[string]string{actor: "1048576", instance: "256"}},
		{name: "dotenv", file: map[string]string{actor: "1048576"}, expected: map[string]string{actor: "1048576"}},
		{name: "env-file", file: map[string]string{instance: "256"}, explicit: true, expected: map[string]string{instance: "256"}},
		{name: "shell-over-file", shell: map[string]string{actor: "2097152"}, file: map[string]string{actor: "1048576", instance: "256"}, expected: map[string]string{actor: "2097152", instance: "256"}},
		{name: "empty-shell-over-file", shell: map[string]string{actor: ""}, file: map[string]string{actor: "1048576"}, expected: map[string]string{actor: ""}, invalidKey: actor},
		{name: "empty-dotenv", file: map[string]string{actor: ""}, expected: map[string]string{actor: ""}, invalidKey: actor},
	}
	for _, invalid := range []struct{ name, value string }{
		{"zero", "0"}, {"negative", "-1"}, {"noninteger", "1.5"}, {"null-text", "null"}, {"overflow", "9223372036854775808"},
	} {
		cases = append(cases, budgetCase{name: invalid.name, shell: map[string]string{actor: invalid.value}, expected: map[string]string{actor: invalid.value}, invalidKey: actor})
	}
	return cases
}

// Every case uses a separate temporary project directory: .env resolution must
// never read or modify a deployment file in the user's checkout.
func (s *smoke) verifyBudgetCases(project, mode string, deploymentEnv []string) error {
	for i, test := range budgetCases() {
		if err := s.verifyBudgetCase(fmt.Sprintf("%s-%d", project, i), mode, deploymentEnv, test); err != nil {
			return fmt.Errorf("%s budget source %s: %w", mode, test.name, err)
		}
	}
	return nil
}

func (s *smoke) verifyBudgetCase(project, mode string, deploymentEnv []string, test budgetCase) (retErr error) {
	dir, err := os.MkdirTemp("", "linguaflow-budget-compose-")
	if err != nil {
		return err
	}
	var created []string
	defer func() {
		for _, path := range created {
			retErr = errors.Join(retErr, os.Remove(path))
		}
		retErr = errors.Join(retErr, os.Remove(dir))
	}()
	write := func(name string, content []byte) error {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0600); err != nil {
			return err
		}
		created = append(created, path)
		return nil
	}
	content, err := os.ReadFile(filepath.Join(s.backend, "deploy", "compose."+mode+".yaml"))
	if err != nil {
		return err
	}
	if err := write("compose.yaml", content); err != nil {
		return err
	}
	env := mergeEnvironment(deploymentEnv, []string{"COMPOSE_DISABLE_ENV_FILE=0"})
	for key, value := range test.shell {
		env = mergeEnvironment(env, []string{key + "=" + value})
	}
	base := []string{"compose", "--project-directory", dir, "--project-name", project, "-f", filepath.Join(dir, "compose.yaml")}
	if test.file != nil {
		name := ".env"
		if test.explicit {
			name = "budget.env"
			base = append(base, "--env-file", filepath.Join(dir, name))
		}
		var keys []string
		for key := range test.file {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var contents strings.Builder
		for _, key := range keys {
			fmt.Fprintf(&contents, "%s=%s\n", key, test.file[key])
		}
		if err := write(name, []byte(contents.String())); err != nil {
			return err
		}
	}
	compose := func(args ...string) (string, error) {
		return s.dockerAt(2*time.Minute, dir, env, append(append([]string(nil), base...), args...)...)
	}
	defer func() {
		_, err := compose("down", "--volumes", "--remove-orphans", "--timeout", "15")
		retErr = errors.Join(retErr, err)
	}()
	rendered, err := compose("config", "--format", "json")
	if err != nil {
		return err
	}
	var config struct {
		Services map[string]struct {
			Environment map[string]*string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(rendered), &config); err != nil {
		return fmt.Errorf("decode Compose config: %w", err)
	}
	service, ok := config.Services["linguaflow"]
	if !ok {
		return errors.New("rendered Compose config has no linguaflow service")
	}
	if err := assertBudgetEnvironment(service.Environment, test.expected); err != nil {
		return fmt.Errorf("rendered environment: %w", err)
	}
	if _, err := compose("create", "--pull", "never"); err != nil {
		return err
	}
	id, err := compose("ps", "--all", "--quiet", "linguaflow")
	if err != nil {
		return err
	}
	if id = strings.TrimSpace(id); id == "" || strings.ContainsAny(id, "\r\n") {
		return errors.New("expected exactly one created container")
	}
	inspected, err := s.dockerAt(time.Minute, dir, env, "inspect", "--format", "{{json .Config.Env}}", id)
	if err != nil {
		return err
	}
	var containerEnv []string
	if err := json.Unmarshal([]byte(inspected), &containerEnv); err != nil {
		return fmt.Errorf("decode container environment: %w", err)
	}
	actual := make(map[string]*string)
	for _, entry := range containerEnv {
		key, value, ok := strings.Cut(entry, "=")
		if !ok && strings.HasPrefix(key, budgetPrefix) {
			return fmt.Errorf("container has unresolved budget variable %s", key)
		}
		actual[key] = &value
	}
	if err := assertBudgetEnvironment(actual, test.expected); err != nil {
		return fmt.Errorf("container environment: %w", err)
	}
	_, checkErr := compose("run", "--rm", "--no-deps", "--pull", "never", "linguaflow", "config", "check", "--mode", "serve")
	if test.invalidKey != "" {
		scope, field, _ := strings.Cut(strings.ToLower(strings.TrimPrefix(test.invalidKey, budgetPrefix)), "_")
		fieldKey := "server.execution_budget." + scope + "." + field
		if checkErr == nil || (!strings.Contains(checkErr.Error(), test.invalidKey) && !strings.Contains(checkErr.Error(), fieldKey)) {
			return fmt.Errorf("expected application config error naming %s, got %v", test.invalidKey, checkErr)
		}
	} else if checkErr != nil {
		return fmt.Errorf("application config check: %w", checkErr)
	}
	// In addition to parsing partial policies, exercise real serve startup with
	// an actor-only and an instance-only policy against each local example.
	if test.name == "actor-shell" || test.name == "instance-shell" {
		if _, err := compose("up", "--detach", "--pull", "never"); err != nil {
			return err
		}
		port, err := compose("port", "linguaflow", "8080")
		if err != nil {
			return err
		}
		host, number, err := net.SplitHostPort(strings.TrimSpace(port))
		if err != nil || host != "127.0.0.1" {
			return fmt.Errorf("unexpected partial-policy HTTP binding %q", strings.TrimSpace(port))
		}
		if err := waitReady("http://" + net.JoinHostPort(host, number)); err != nil {
			return fmt.Errorf("partial-policy serve startup: %w", err)
		}
	}
	fmt.Printf("PASS: %s %s (render, container environment, application config).\n", mode, test.name)
	return nil
}

// A null entry in normalized Compose output remains an unresolved pass-through
// value; only container inspection establishes that it was not injected.
func assertBudgetEnvironment(actual map[string]*string, expected map[string]string) error {
	for key, value := range actual {
		if !strings.HasPrefix(key, budgetPrefix) || value == nil {
			continue
		}
		want, ok := expected[key]
		if !ok || *value != want {
			return fmt.Errorf("unexpected budget variable %s=%q", key, *value)
		}
	}
	for key, want := range expected {
		if value := actual[key]; value == nil || *value != want {
			return fmt.Errorf("budget variable %s was not preserved as %q", key, want)
		}
	}
	return nil
}

// Replace an existing variable rather than relying on duplicate entries being
// resolved consistently by Go, Windows, and the Compose subprocess.
func mergeEnvironment(base, overrides []string) []string {
	result := append([]string(nil), base...)
	for _, override := range overrides {
		name, _, _ := strings.Cut(override, "=")
		filtered := result[:0]
		for _, entry := range result {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(key, name) {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, override)
	}
	return result
}
