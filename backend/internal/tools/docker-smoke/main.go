// Command docker-smoke exercises the two checked-in Compose examples against a
// fresh image built from this checkout. It never uses an existing project or DB.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type smoke struct {
	backend string
	image   string
	env     []string
	secrets []string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Docker smoke FAILED (not verified):", err)
		os.Exit(1)
	}
}

func run() (retErr error) {
	if len(os.Args) != 1 {
		return errors.New("no arguments accepted; run task -t backend/Taskfile.yml docker:smoke")
	}
	backend, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(backend, "deploy", "compose.env.yaml")); err != nil {
		return errors.New("run through task -t backend/Taskfile.yml docker:smoke from the repository root")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("Docker CLI is unavailable; install/start Docker with Linux containers to run this check")
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	suffix := hex.EncodeToString(id[:])
	s := &smoke{backend: backend, image: "linguaflow-smoke:" + suffix}
	// Keep Docker connection settings, but never inherit deployment inputs or a
	// user's Compose project selection. All test secrets are generated below.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if !strings.HasPrefix(upper, "LINGUAFLOW_") && !strings.HasPrefix(upper, "COMPOSE_") {
			s.env = append(s.env, entry)
		}
	}
	s.env = append(s.env, "COMPOSE_DISABLE_ENV_FILE=1")
	platform, err := s.docker(time.Minute, nil, "info", "--format", "{{.OSType}}")
	if err != nil {
		return fmt.Errorf("Docker daemon is unavailable: %w", err)
	}
	if strings.TrimSpace(platform) != "linux" {
		return errors.New("Docker must use Linux containers for the repository image")
	}
	if _, err := s.docker(time.Minute, nil, "compose", "version", "--short"); err != nil {
		return fmt.Errorf("Docker Compose v2 is required: %w", err)
	}
	fmt.Println("Building a temporary image from the current checkout...")
	if _, err := s.docker(20*time.Minute, nil, "build", "-f", filepath.Join(backend, "..", "Dockerfile"), "-t", s.image, filepath.Dir(backend)); err != nil {
		return err
	}
	defer func() {
		_, cleanupErr := s.docker(time.Minute, nil, "image", "rm", s.image)
		retErr = errors.Join(retErr, cleanupErr)
	}()
	for _, mode := range []string{"env", "secrets"} {
		if err := s.scenario("lf-smoke-"+suffix+"-"+mode, mode); err != nil {
			return fmt.Errorf("%s example: %w", mode, err)
		}
	}
	fmt.Println("PASS: both Compose examples initialized, logged in, stored credentials, restarted, and validated stored keys without generating a keyring file.")
	return nil
}

func (s *smoke) docker(timeout time.Duration, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = s.backend
	cmd.Env = append(append([]string(nil), s.env...), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Arguments contain paths and variable names, never secret values.
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, s.redact(string(output)))
	}
	return string(output), nil
}

func (s *smoke) redact(value string) string {
	for _, secret := range s.secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func (s *smoke) randomSecret() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	value := base64.StdEncoding.EncodeToString(data[:])
	s.secrets = append(s.secrets, value)
	return value, nil
}

func (s *smoke) scenario(project, mode string) (retErr error) {
	fmt.Printf("Checking %s Compose example...\n", mode)
	jwt, err := s.randomSecret()
	if err != nil {
		return err
	}
	master, err := s.randomSecret()
	if err != nil {
		return err
	}
	password, err := s.randomSecret()
	if err != nil {
		return err
	}
	providerSecret, err := s.randomSecret()
	if err != nil {
		return err
	}
	env := []string{
		"LINGUAFLOW_IMAGE=" + s.image,
		"LINGUAFLOW_HTTP_PORT=0",
		"LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME=smoke-admin",
		"LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL=smoke-admin@example.invalid",
	}
	if mode == "env" {
		env = append(env, "LINGUAFLOW_JWT_SECRET="+jwt, "LINGUAFLOW_CREDENTIALS_MASTER_KEY="+master, "LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD="+password)
	} else {
		dir, err := os.MkdirTemp("", "linguaflow-docker-smoke-")
		if err != nil {
			return err
		}
		// Remove only the three known generated files and the newly created empty
		// directory. No recursive deletion or user-provided paths are involved.
		var created []string
		defer func() {
			for _, path := range created {
				_ = os.Chmod(path, 0600)
				retErr = errors.Join(retErr, os.Remove(path))
			}
			retErr = errors.Join(retErr, os.Remove(dir))
		}()
		for _, item := range []struct{ name, variable, value string }{
			{"jwt", "LINGUAFLOW_JWT_SECRET_PATH", jwt},
			{"master", "LINGUAFLOW_CREDENTIALS_MASTER_KEY_PATH", master},
			{"password", "LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD_PATH", password},
		} {
			path := filepath.Join(dir, item.name)
			if err := os.WriteFile(path, []byte(item.value+"\n"), 0600); err != nil {
				return err
			}
			created = append(created, path)
			// Test regular Docker secret permissions under the image's appuser.
			// Compose file secrets are bind-mounted and may preserve host mode.
			if err := os.Chmod(path, 0444); err != nil {
				return err
			}
			env = append(env, item.variable+"="+path)
		}
	}
	compose := func(args ...string) (string, error) {
		base := []string{"compose", "--project-name", project, "-f", filepath.Join(s.backend, "deploy", "compose."+mode+".yaml")}
		return s.docker(2*time.Minute, env, append(base, args...)...)
	}
	defer func() {
		if retErr != nil {
			if logs, err := compose("logs", "--no-color", "--tail", "80"); err == nil {
				fmt.Fprintln(os.Stderr, s.redact(logs))
			}
		}
		// The project ID was freshly generated above and is never configurable.
		_, cleanupErr := compose("down", "--volumes", "--remove-orphans", "--timeout", "15")
		retErr = errors.Join(retErr, cleanupErr)
	}()
	if _, err := compose("config", "--quiet"); err != nil {
		return err
	}
	if _, err := compose("up", "--detach", "--pull", "never"); err != nil {
		return err
	}
	port, err := compose("port", "linguaflow", "8080")
	if err != nil {
		return err
	}
	host, number, err := net.SplitHostPort(strings.TrimSpace(port))
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("unexpected HTTP binding %q", strings.TrimSpace(port))
	}
	baseURL := "http://" + net.JoinHostPort(host, number)
	if err := waitReady(baseURL); err != nil {
		return err
	}
	token, err := s.login(baseURL, password)
	if err != nil {
		return err
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := request(baseURL, "POST", "/api/v1/credentials", token, map[string]string{"provider": "openai", "secret": providerSecret}, http.StatusCreated, &created); err != nil {
		return err
	}
	if created.ID <= 0 {
		return errors.New("credential creation returned no ID")
	}
	if _, err := compose("restart", "--timeout", "15", "linguaflow"); err != nil {
		return err
	}
	// Startup ValidateKeys decrypts every stored credential before becoming ready.
	if err := waitReady(baseURL); err != nil {
		return fmt.Errorf("restart with persisted credential: %w", err)
	}
	token, err = s.login(baseURL, password)
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			ID int `json:"id"`
		} `json:"items"`
	}
	if err := request(baseURL, "GET", "/api/v1/credentials", token, nil, http.StatusOK, &list); err != nil {
		return err
	}
	found := false
	for _, item := range list.Items {
		found = found || item.ID == created.ID
	}
	if !found {
		return errors.New("created credential was not persisted across restart")
	}
	if _, err := compose("exec", "-T", "linguaflow", "sh", "-c", "test ! -e /app/data/credentials-keyring.json"); err != nil {
		return fmt.Errorf("master-key mode unexpectedly created a keyring file: %w", err)
	}
	fmt.Printf("PASS: %s example (initialization, login, encrypted credential, restart, no keyring file).\n", mode)
	return nil
}

func (s *smoke) login(baseURL, password string) (string, error) {
	var session struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := request(baseURL, "POST", "/api/v1/auth/login", "", map[string]string{"username": "smoke-admin", "password": password}, http.StatusOK, &session); err != nil {
		return "", err
	}
	if session.AccessToken == "" {
		return "", errors.New("administrator login returned no access token")
	}
	s.secrets = append(s.secrets, session.AccessToken)
	if session.RefreshToken != "" {
		s.secrets = append(s.secrets, session.RefreshToken)
	}
	return session.AccessToken, nil
}

func waitReady(baseURL string) error {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if err := request(baseURL, "GET", "/health/ready", "", nil, http.StatusOK, nil); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("service did not become ready within one minute")
}

func request(baseURL, method, path, token string, body any, expected int, target any) error {
	var input []byte
	if body != nil {
		var err error
		input, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequest(method, baseURL+path, bytes.NewReader(input))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		return fmt.Errorf("%s %s returned HTTP %d, expected %d", method, path, response.StatusCode, expected)
	}
	if target != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
			return fmt.Errorf("%s %s returned invalid JSON", method, path)
		}
	}
	return nil
}
